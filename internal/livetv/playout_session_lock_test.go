package livetv

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// barrierPlayback blocks Prepare calls after blockAfter on a barrier, so a
// test can hold a tune/playlist preparation in flight deterministically.
type barrierPlayback struct {
	*fakePlayback
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	calls       atomic.Int32
	blockAfter  int32
}

func newBarrierPlayback(blockAfter int32) *barrierPlayback {
	return &barrierPlayback{
		fakePlayback: &fakePlayback{},
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
		blockAfter:   blockAfter,
	}
}

func (b *barrierPlayback) Prepare(ctx context.Context, u *core.User, k core.RatingKey, c core.ClientCaps) (*core.PlaySession, error) {
	if b.calls.Add(1) > b.blockAfter {
		b.enteredOnce.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.fakePlayback.Prepare(ctx, u, k, c)
}

func (b *barrierPlayback) PrepareChannel(ctx context.Context, u *core.User, k core.RatingKey, c core.ClientCaps, g string) (*core.PlaySession, error) {
	b.groups = append(b.groups, g)
	ps, err := b.Prepare(ctx, u, k, c)
	if ps != nil {
		ps.PlaybackGroup = g
	}
	return ps, err
}

func waitEntered(t *testing.T, b *barrierPlayback) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("preparation did not reach the barrier")
	}
}

// While ServePlaylist is blocked preparing the next programme, serving an
// already-prepared segment and looking the session up must not wait behind
// the preparation I/O.
func TestPlaylistPreparationDoesNotBlockSegmentOrSession(t *testing.T) {
	now := time.Unix(7, 0).UTC()
	pb := newBarrierPlayback(1) // the tune's own Prepare passes; the next blocks
	tc := &fakeTranscoder{}
	s := testService(t, Options{Now: func() time.Time { return now }, Playback: pb, Transcoder: tc})
	c := testChannel()
	c.Items = []Item{{Key: core.MovieKey(1), Title: "First", Rating: "G", DurationMS: 12000}, {Key: core.MovieKey(2), Title: "Second", Rating: "PG", DurationMS: 12000}}
	if _, err := s.SaveChannel(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	info, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}

	playlistDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.ServePlaylist(rec, httptest.NewRequest("GET", "/", nil), info.ID, u)
		playlistDone <- rec.Code
	}()
	waitEntered(t, pb)

	segmentDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.ServeSegment(rec, httptest.NewRequest("GET", "/", nil), info.ID, info.Programme.Start.UnixMilli(), 1, u)
		segmentDone <- rec.Code
	}()
	select {
	case code := <-segmentDone:
		if code != 200 {
			t.Fatalf("segment during playlist preparation: %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeSegment blocked behind playlist preparation I/O")
	}

	sessionDone := make(chan error, 1)
	go func() {
		_, err := s.getSession(context.Background(), info.ID, u)
		sessionDone <- err
	}()
	select {
	case err := <-sessionDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("getSession blocked behind playlist preparation I/O")
	}

	close(pb.release)
	select {
	case code := <-playlistDone:
		if code != 200 {
			t.Fatalf("playlist after release: %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playlist did not finish after barrier release")
	}
}

// Stop during a tune whose Prepare is still in flight must complete without
// waiting for the Prepare, and must cancel the Prepare's context (the tune
// then unwinds with context.Canceled and publishes nothing).
func TestStopDuringSlowTuneCancelsAndDoesNotDeadlock(t *testing.T) {
	now := time.Unix(7, 0).UTC()
	pb := newBarrierPlayback(0) // every Prepare blocks
	tc := &fakeTranscoder{}
	s := testService(t, Options{Now: func() time.Time { return now }, Playback: pb, Transcoder: tc})
	c := testChannel()
	if _, err := s.SaveChannel(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	tuneDone := make(chan error, 1)
	go func() {
		_, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
		tuneDone <- err
	}()
	waitEntered(t, pb)

	views := s.ViewingSessions()
	if len(views) != 1 {
		t.Fatalf("pending tune reservations = %d", len(views))
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- s.Stop(context.Background(), views[0].ID, u) }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop blocked behind tune preparation I/O")
	}

	// The barrier is never released: only cancellation of the session-bound
	// Prepare context can unwind the tune.
	select {
	case err := <-tuneDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped tune err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tune kept running after Stop")
	}
	if got := s.ViewingSessions(); len(got) != 0 {
		t.Fatalf("stopped tune retained %d reservations", len(got))
	}
}
