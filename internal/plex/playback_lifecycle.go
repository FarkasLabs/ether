package plex

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/braedonsaunders/ether/internal/core"
)

// Lifecycle operations lock the owner first, then at most one encoder ID.
// Response streaming never holds these gates. ID gates also serialize reuse
// across titles or owners while the table's ownership claim is rechecked.
type playbackLifecycle struct {
	mu    sync.Mutex
	gates map[string]*playbackGate
}
type playbackGate struct {
	token chan struct{}
	refs  int
}

func newPlaybackLifecycle() *playbackLifecycle {
	return &playbackLifecycle{gates: make(map[string]*playbackGate)}
}
func (l *playbackLifecycle) acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	g := l.gates[key]
	if g == nil {
		g = &playbackGate{token: make(chan struct{}, 1)}
		g.token <- struct{}{}
		l.gates[key] = g
	}
	g.refs++
	l.mu.Unlock()
	drop := func() {
		l.mu.Lock()
		g.refs--
		if g.refs == 0 {
			delete(l.gates, key)
		}
		l.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-g.token:
	}
	if err := ctx.Err(); err != nil {
		g.token <- struct{}{}
		drop()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { g.token <- struct{}{}; drop() }) }, nil
}
func (s *Server) lockPlaybackOwner(ctx context.Context, user int64, client string) (func(), error) {
	return s.playbackLifecycle.acquire(ctx, fmt.Sprintf("owner/%d/%s", user, client))
}
func (s *Server) lockPlaybackRequest(w http.ResponseWriter, r *http.Request) (func(), bool) {
	pr := FromContext(r.Context())
	if pr.User == nil {
		writeHTTPError(w, http.StatusUnauthorized)
		return nil, false
	}
	unlock, err := s.lockPlaybackOwner(r.Context(), pr.User.ID, pr.ClientID)
	if err != nil {
		return nil, false
	}
	return unlock, true
}
func (s *Server) lockEncoder(ctx context.Context, id string) (func(), error) {
	return s.playbackLifecycle.acquire(ctx, "encoder/"+id)
}

// The caller holds the owner gate. Never release the shared playback source.
func (s *Server) cleanSupersededEncoders(ctx context.Context, user int64, client string, key core.RatingKey, playbackID string) {
	for _, id := range s.transcodes.forClient(user, client, key) {
		unlock, err := s.lockEncoder(ctx, id)
		if err != nil {
			return
		}
		e, ok := s.transcodes.get(id)
		if ok && e.userID == user && e.clientID == client && e.key == key &&
			e.playbackID == playbackID && s.playbackOwners.stale(user, client, key, playbackID) {
			if s.d.Transcoder != nil {
				s.d.Transcoder.Stop(id)
			}
			s.transcodes.remove(id)
		}
		unlock()
	}
}

// hasStartedVideoCopy reports another started remux/Direct Stream encoder
// for this viewer/client/title (excluding exceptID).
func (s *Server) hasStartedVideoCopy(user int64, client string, key core.RatingKey, exceptID string) bool {
	for _, id := range s.transcodes.forClient(user, client, key) {
		if id == exceptID {
			continue
		}
		e, ok := s.transcodes.get(id)
		if ok && e.started && e.decision.VideoCopy {
			return true
		}
	}
	return false
}

// reconcilePlayEncoders stops sibling encoders for the same viewer/client/title
// after keepID has started. Plex Web often opens a second DASH session= id
// while reusing X-Plex-Session-Identifier, so playbackOwners.stale never
// fires and a tone-map encode can run beside an HEVC remux. Prefer VideoCopy:
// when keepID is a copy, retire encodes (and older copies). Caller holds the
// owner gate and keepID's encoder gate; this never re-locks keepID.
func (s *Server) reconcilePlayEncoders(ctx context.Context, user int64, client string, key core.RatingKey, keepID string) {
	keep, ok := s.transcodes.get(keepID)
	if !ok || !keep.started {
		return
	}
	for _, id := range s.transcodes.forClient(user, client, key) {
		if id == keepID {
			continue
		}
		e, ok := s.transcodes.get(id)
		if !ok || e.userID != user || e.clientID != client || e.key != key || !e.started {
			continue
		}
		// Keep an existing copy when we just started an encode (should be
		// rare; start paths refuse this). Keep only keepID among copies.
		if !keep.decision.VideoCopy && e.decision.VideoCopy {
			continue
		}
		if keep.decision.VideoCopy && e.decision.VideoCopy && id != keepID {
			// fall through: newer copy replaces older copy
		} else if keep.decision.VideoCopy && !e.decision.VideoCopy {
			// fall through: copy replaces encode
		} else if !keep.decision.VideoCopy && !e.decision.VideoCopy {
			// Newer encode replaces older encode for the same play.
		} else {
			continue
		}
		unlock, err := s.lockEncoder(ctx, id)
		if err != nil {
			return
		}
		cur, ok := s.transcodes.get(id)
		if ok && cur.userID == user && cur.clientID == client && cur.key == key && cur.started {
			if s.d.Transcoder != nil {
				s.d.Transcoder.Stop(id)
			}
			s.transcodes.remove(id)
			s.log.Info("stopped sibling transcode session", "kept", keepID, "stopped", id, "key", key,
				"stoppedCopy", cur.decision.VideoCopy)
		}
		unlock()
	}
}
