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

// gatedPlayback is like barrierPlayback, but additionally signals when a
// second gated Prepare enters while the first is still blocked, and records
// the maximum number of gated Prepares in flight. The embedded fake is
// guarded so a duplicate Prepare (the failure mode) cannot corrupt it.
type gatedPlayback struct {
	*fakePlayback
	entered       chan struct{}
	secondEntered chan struct{}
	release       chan struct{}
	enteredOnce   sync.Once
	secondOnce    sync.Once
	fakeMu        sync.Mutex
	calls         atomic.Int32
	inFlight      atomic.Int32
	maxInFlight   atomic.Int32
	blockAfter    int32
}

func newGatedPlayback(blockAfter int32) *gatedPlayback {
	return &gatedPlayback{
		fakePlayback:  &fakePlayback{},
		entered:       make(chan struct{}),
		secondEntered: make(chan struct{}),
		release:       make(chan struct{}),
		blockAfter:    blockAfter,
	}
}

func (g *gatedPlayback) prepareFake(ctx context.Context, u *core.User, k core.RatingKey, c core.ClientCaps) (*core.PlaySession, error) {
	g.fakeMu.Lock()
	defer g.fakeMu.Unlock()
	return g.fakePlayback.Prepare(ctx, u, k, c)
}

func (g *gatedPlayback) Prepare(ctx context.Context, u *core.User, k core.RatingKey, c core.ClientCaps) (*core.PlaySession, error) {
	if g.calls.Add(1) <= g.blockAfter {
		return g.prepareFake(ctx, u, k, c)
	}
	cur := g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	for {
		if max := g.maxInFlight.Load(); cur <= max || g.maxInFlight.CompareAndSwap(max, cur) {
			break
		}
	}
	if cur >= 2 {
		g.secondOnce.Do(func() { close(g.secondEntered) })
	}
	g.enteredOnce.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.prepareFake(ctx, u, k, c)
}

func (g *gatedPlayback) PrepareChannel(ctx context.Context, u *core.User, k core.RatingKey, c core.ClientCaps, group string) (*core.PlaySession, error) {
	g.fakeMu.Lock()
	g.groups = append(g.groups, group)
	g.fakeMu.Unlock()
	ps, err := g.Prepare(ctx, u, k, c)
	if ps != nil {
		ps.PlaybackGroup = group
	}
	return ps, err
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

// Two concurrent prepares of the same (not yet prepared) programme on the
// same session must collapse into one effective Prepare: prepareMu
// serializes them, the first publishes exactly one track, and the second
// reuses that track instead of preparing again. Without prepareMu both
// prepares would be in flight at the barrier at once (signalled via
// secondEntered / maxInFlight) and would each call Prepare, so the
// assertions below fail.
func TestConcurrentPreparesSameProgramPrepareOnce(t *testing.T) {
	now := time.Unix(7, 0).UTC()
	pb := newGatedPlayback(1) // the tune's own Prepare passes; the next blocks
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
	ss, err := s.getSession(context.Background(), info.ID, u)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := s.programmes(context.Background(), c, now, now.Add(24*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) < 2 {
		t.Fatalf("programmes = %d, want at least 2", len(ps))
	}
	target := ps[1]
	if info.Programme == nil || target.ID == info.Programme.ID {
		t.Fatalf("target programme %q is the already-tuned programme", target.ID)
	}

	type result struct {
		tr  *track
		err error
	}
	results := make(chan result, 2)
	prepare := func() {
		tr, err := s.prepareTrack(context.Background(), ss, target)
		results <- result{tr: tr, err: err}
	}

	// First preparer: reaches the barrier and stays in flight, holding
	// prepareMu for the whole preparation.
	go prepare()
	select {
	case <-pb.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first concurrent prepare did not reach the barrier")
	}

	// Second preparer of the same programme: with prepareMu it must wait
	// and never enter Prepare while the first is in flight; without
	// prepareMu it enters immediately and trips secondEntered.
	go prepare()
	duplicateInFlight := false
	select {
	case <-pb.secondEntered:
		duplicateInFlight = true
	case <-time.After(500 * time.Millisecond):
	}

	close(pb.release)
	var got [2]result
	for i := range got {
		select {
		case got[i] = <-results:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent prepare did not finish after barrier release")
		}
	}
	for i, r := range got {
		if r.err != nil {
			t.Fatalf("concurrent prepare %d: %v", i, r.err)
		}
		if r.tr == nil {
			t.Fatalf("concurrent prepare %d returned no track", i)
		}
	}
	if duplicateInFlight {
		t.Fatal("two Prepares of the same programme were in flight at once (prepareMu not serializing)")
	}
	if max := pb.maxInFlight.Load(); max != 1 {
		t.Fatalf("max gated Prepares in flight = %d, want 1", max)
	}
	// Exactly one effective Prepare for the target programme: the tune's
	// Prepare plus one for the target, no more.
	if calls := pb.calls.Load(); calls != 2 {
		t.Fatalf("Prepare calls = %d, want 2 (tune + one shared prepare of the target)", calls)
	}
	if len(pb.prepared) != 2 {
		t.Fatalf("prepared playback sessions = %d, want 2 (no duplicate/staged leak)", len(pb.prepared))
	}
	if len(pb.stopped) != 0 {
		t.Fatalf("playback sessions stopped as discarded staged duplicates = %v, want none", pb.stopped)
	}
	if len(tc.requests) != 2 || len(tc.hls) != 2 {
		t.Fatalf("StartHLS calls = %d (sessions %d), want 2 (tune + one shared track)", len(tc.requests), len(tc.hls))
	}
	if got[0].tr != got[1].tr {
		t.Fatalf("concurrent prepares returned different tracks (%p vs %p); second did not reuse the first", got[0].tr, got[1].tr)
	}

	ss.mu.Lock()
	total, matching := len(ss.tracks), 0
	for _, tr := range ss.tracks {
		if tr.programme.ID == target.ID {
			matching++
		}
	}
	ss.mu.Unlock()
	if total != 2 || matching != 1 {
		t.Fatalf("published tracks = %d total / %d for target, want 2 / 1", total, matching)
	}
}
