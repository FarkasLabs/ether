package livetv

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
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

// blockingFailoverPlayback blocks inside Playback.Failover until released.
type blockingFailoverPlayback struct {
	*fakePlayback
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func (b *blockingFailoverPlayback) Failover(ctx context.Context, id string, _ time.Duration) (*core.PlaySession, error) {
	b.enteredOnce.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &core.PlaySession{ID: id, Info: core.MediaInfo{Duration: time.Hour}}, nil
}

// blockingSwapHLS blocks inside SwapInput until released.
type blockingSwapHLS struct {
	*fakeHLS
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func (h *blockingSwapHLS) SwapInput(ctx context.Context, url string, info core.MediaInfo, at time.Duration) error {
	h.enteredOnce.Do(func() { close(h.entered) })
	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return h.fakeHLS.SwapInput(ctx, url, info, at)
}

type blockingSwapTranscoder struct {
	*fakeTranscoder
	hls *blockingSwapHLS
}

func (t *blockingSwapTranscoder) StartHLS(_ context.Context, r core.HLSRequest) (core.HLSSession, error) {
	t.requests = append(t.requests, r)
	return t.hls, nil
}

// While Failover is blocked in Playback.Failover, and again while it is
// blocked in SwapInput, serving a segment and looking the session up must
// not wait behind that I/O (previously both ran under ss.mu).
func TestFailoverDoesNotBlockSegmentOrSession(t *testing.T) {
	now := time.Unix(7, 0).UTC()
	pb := &blockingFailoverPlayback{fakePlayback: &fakePlayback{}, entered: make(chan struct{}), release: make(chan struct{})}
	swapHLS := &blockingSwapHLS{fakeHLS: &fakeHLS{id: "swap"}, entered: make(chan struct{}), release: make(chan struct{})}
	tc := &blockingSwapTranscoder{fakeTranscoder: &fakeTranscoder{}, hls: swapHLS}
	s := testService(t, Options{Now: func() time.Time { return now }, Playback: pb, Transcoder: tc})
	c := testChannel()
	if _, err := s.SaveChannel(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	info, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}

	assertResponsive := func(stage string) {
		t.Helper()
		segmentDone := make(chan int, 1)
		go func() {
			rec := httptest.NewRecorder()
			s.ServeSegment(rec, httptest.NewRequest("GET", "/", nil), info.ID, info.Programme.Start.UnixMilli(), 1, u)
			segmentDone <- rec.Code
		}()
		select {
		case code := <-segmentDone:
			if code != 200 {
				t.Fatalf("segment during %s: %d", stage, code)
			}
		case <-time.After(time.Second):
			t.Fatalf("ServeSegment blocked behind %s I/O", stage)
		}
		sessionDone := make(chan error, 1)
		go func() {
			_, err := s.getSession(context.Background(), info.ID, u)
			sessionDone <- err
		}()
		select {
		case err := <-sessionDone:
			if err != nil {
				t.Fatalf("getSession during %s: %v", stage, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("getSession blocked behind %s I/O", stage)
		}
	}

	failoverDone := make(chan error, 1)
	go func() { failoverDone <- s.Failover(context.Background(), info.ID, u) }()
	select {
	case <-pb.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Failover did not reach Playback.Failover")
	}
	assertResponsive("Playback.Failover")
	close(pb.release)

	select {
	case <-swapHLS.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Failover did not reach SwapInput")
	}
	assertResponsive("HLS SwapInput")
	close(swapHLS.release)

	select {
	case err := <-failoverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Failover did not finish after release")
	}
	if !swapHLS.swapped || swapHLS.offset != 7*time.Second {
		t.Fatalf("swap not applied at programme timestamp: swapped=%v offset=%v", swapHLS.swapped, swapHLS.offset)
	}
}

// programmeGate is a stub database/sql driver whose livetv_programmes
// query blocks on exactly the blockOn-th call after construction. Other
// queries answer just enough for Channel/Policy lookups. This makes the
// programme lookup inside Service.Session (the second programmes call,
// after getSession's) block deterministically while later calls pass.
type programmeGate struct {
	mu          sync.Mutex
	calls       int
	blockOn     int
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
	channel     []byte
}

type gateDriver struct{ gate *programmeGate }

func (d gateDriver) Open(string) (driver.Conn, error) { return gateConn{gate: d.gate}, nil }

type gateConnector struct{ gate *programmeGate }

func (c gateConnector) Connect(context.Context) (driver.Conn, error) {
	return gateConn{gate: c.gate}, nil
}
func (c gateConnector) Driver() driver.Driver { return gateDriver{gate: c.gate} }

type gateConn struct{ gate *programmeGate }

func (c gateConn) Prepare(query string) (driver.Stmt, error) {
	return gateStmt{gate: c.gate, query: query}, nil
}
func (c gateConn) Close() error              { return nil }
func (c gateConn) Begin() (driver.Tx, error) { return gateTx{}, nil }

type gateTx struct{}

func (gateTx) Commit() error   { return nil }
func (gateTx) Rollback() error { return nil }

type gateResult struct{}

func (gateResult) LastInsertId() (int64, error) { return 0, nil }
func (gateResult) RowsAffected() (int64, error) { return 1, nil }

type gateStmt struct {
	gate  *programmeGate
	query string
}

func (s gateStmt) Close() error  { return nil }
func (s gateStmt) NumInput() int { return -1 }
func (s gateStmt) Exec([]driver.Value) (driver.Result, error) {
	return gateResult{}, nil
}
func (s gateStmt) Query([]driver.Value) (driver.Rows, error) {
	g := s.gate
	switch {
	case strings.Contains(s.query, "livetv_programmes"):
		g.mu.Lock()
		g.calls++
		block := g.calls == g.blockOn
		g.mu.Unlock()
		if block {
			g.enteredOnce.Do(func() { close(g.entered) })
			<-g.release
		}
		return &gateRows{cols: []string{"data"}}, nil
	case strings.Contains(s.query, "livetv_channels"):
		return &gateRows{cols: []string{"data"}, rows: [][]driver.Value{{g.channel}}}, nil
	default:
		return &gateRows{cols: []string{"data"}}, nil
	}
}

type gateRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

func (r *gateRows) Columns() []string { return r.cols }
func (r *gateRows) Close() error      { return nil }
func (r *gateRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

// While Service.Session is blocked in its programme lookup (DB), serving
// a segment and looking the session up must not wait behind it (previously
// Session held ss.mu across s.programmes).
func TestSessionProgrammeLookupDoesNotBlockSegmentOrSession(t *testing.T) {
	now := time.Unix(7, 0).UTC()
	channel := Channel{ID: "test", Number: "1", Name: "Owner channel", Kind: "iptv", Enabled: true}
	channelJSON, err := json.Marshal(channel)
	if err != nil {
		t.Fatal(err)
	}
	// Session calls programmes twice: once inside getSession, once for its
	// own fresh programme. Block only the second call.
	gate := &programmeGate{blockOn: 2, entered: make(chan struct{}), release: make(chan struct{}), channel: channelJSON}
	db := sql.OpenDB(gateConnector{gate: gate})
	t.Cleanup(func() { db.Close() })
	svcCtx, svcCancel := context.WithCancel(context.Background())
	s := &Service{
		o:          Options{DB: db, Now: func() time.Time { return now }},
		sessions:   map[string]*session{},
		transcodes: map[string]core.HLSSession{},
		ctx:        svcCtx, cancel: svcCancel,
	}
	t.Cleanup(func() { s.Close() })

	prog := Programme{ID: "test-0", ChannelID: "test", Start: time.Unix(0, 0).UTC(), End: time.Unix(12, 0).UTC(), Item: Item{Key: core.MovieKey(1), Title: "First", Rating: "G", DurationMS: 12000}}
	ssCtx, ssCancel := context.WithCancel(context.Background())
	ss := &session{
		info:      SessionInfo{ID: "session-1", UserID: 1, Channel: channel, StartedAt: now},
		user:      core.User{ID: 1},
		last:      now,
		ready:     true,
		resources: map[string]string{},
		tracks:    []*track{{programme: prog, hls: &fakeHLS{id: "session-slow"}, playbackID: "1", count: 3}},
		ctx:       ssCtx, cancel: ssCancel,
	}
	s.sessions[ss.info.ID] = ss
	u := &core.User{ID: 1}

	sessionDone := make(chan error, 1)
	go func() {
		info, err := s.Session(context.Background(), ss.info.ID, u)
		if err == nil && info.ID != ss.info.ID {
			err = errors.New("Session returned another session")
		}
		sessionDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Session did not reach its blocked programme lookup")
	}

	segmentDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.ServeSegment(rec, httptest.NewRequest("GET", "/", nil), ss.info.ID, prog.Start.UnixMilli(), 1, u)
		segmentDone <- rec.Code
	}()
	select {
	case code := <-segmentDone:
		if code != 200 {
			t.Fatalf("segment during Session programme lookup: %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeSegment blocked behind Session programme lookup")
	}

	lookupDone := make(chan error, 1)
	go func() {
		_, err := s.getSession(context.Background(), ss.info.ID, u)
		lookupDone <- err
	}()
	select {
	case err := <-lookupDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("getSession blocked behind Session programme lookup")
	}

	close(gate.release)
	select {
	case err := <-sessionDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Session did not finish after programme lookup release")
	}
}
