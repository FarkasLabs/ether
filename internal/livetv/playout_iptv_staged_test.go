package livetv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// stagedIPTVTranscoder serves a fixed probe result and, when entered is set,
// blocks StartHLS so a test can close the real session while the staged
// tune is still in flight. StartHLS itself is the package's fake, so no
// ffmpeg or provider network is involved beyond the loopback test upstream.
type stagedIPTVTranscoder struct {
	fakeTranscoder
	info    core.MediaInfo
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *stagedIPTVTranscoder) Probe(context.Context, string) (*core.MediaInfo, error) {
	info := f.info
	return &info, nil
}

func (f *stagedIPTVTranscoder) StartHLS(ctx context.Context, r core.HLSRequest) (core.HLSSession, error) {
	if f.entered != nil {
		f.once.Do(func() { close(f.entered) })
		<-f.release
	}
	return f.fakeTranscoder.StartHLS(ctx, r)
}

func stagedIPTVFixture(t *testing.T, tc core.Transcoder) (*Service, *core.User, Channel) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		fmt.Fprint(w, "G00000000000000000000000")
	}))
	t.Cleanup(up.Close)
	s := testService(t, Options{HTTPClient: up.Client(), Transcoder: tc})
	channel, err := s.SaveChannel(context.Background(), Channel{Name: "Fixture", Number: "1", Kind: "iptv", Enabled: true, StreamURL: up.URL + "/stream"})
	if err != nil {
		t.Fatal(err)
	}
	return s, &core.User{ID: 1}, channel
}

func stagedIPTVInfo() core.MediaInfo {
	return core.MediaInfo{Container: "mpegts", BitrateKbps: 5000, Streams: []core.StreamInfo{
		{Index: 3, Type: "video", Codec: "h264", Width: 1280, Height: 720},
		{Index: 5, Type: "audio", Codec: "aac", Channels: 2, Default: true},
	}}
}

func liveSessionByID(s *Service, id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func grantOf(s *Service, ss *session) *inputGrant {
	s.inputRelay.mu.Lock()
	defer s.inputRelay.mu.Unlock()
	return s.inputRelay.bySess[ss]
}

func onlyGrant(t *testing.T, s *Service) (*session, *inputGrant) {
	t.Helper()
	s.inputRelay.mu.Lock()
	defer s.inputRelay.mu.Unlock()
	if len(s.inputRelay.bySess) != 1 {
		t.Fatalf("input grants = %d, want exactly one", len(s.inputRelay.bySess))
	}
	for ss, g := range s.inputRelay.bySess {
		return ss, g
	}
	return nil, nil
}

// A successful IPTV tune prepares on a staged session copy: the input grant
// created for the stage must be re-keyed to the real session with its token
// (and thus the URL the transcoder received) unchanged, and Stop must
// revoke that grant and close the published relay.
func TestIPTVStagedPublishTransfersGrantAndStopRevokes(t *testing.T) {
	tc := &stagedIPTVTranscoder{info: stagedIPTVInfo()}
	s, u, channel := stagedIPTVFixture(t, tc)

	info, err := s.Tune(context.Background(), u, channel.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}
	ss := liveSessionByID(s, info.ID)
	if ss == nil {
		t.Fatal("tuned session not registered")
	}
	key, grant := onlyGrant(t, s)
	if key != ss {
		t.Fatal("input grant was not re-keyed from the staged copy to the real session")
	}
	if grant == nil || grant.ctx.Err() != nil {
		t.Fatal("transferred input grant is not live")
	}
	input, err := s.relayInput(ss)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.requests) != 1 || tc.requests[0].InputURL != input {
		t.Fatalf("transcoder input %q does not use the transferred grant URL %q", tc.requests[0].InputURL, input)
	}
	ss.mu.Lock()
	relay := ss.relay
	ss.mu.Unlock()
	if relay == nil || len(tc.hls) != 1 || relay != core.HLSSession(tc.hls[0]) {
		t.Fatal("staged relay was not published on the real session")
	}

	if err := s.Stop(context.Background(), info.ID, u); err != nil {
		t.Fatal(err)
	}
	if grantOf(s, ss) != nil {
		t.Fatal("Stop left the session's input grant registered")
	}
	if grant.ctx.Err() == nil {
		t.Fatal("Stop did not revoke the transferred input grant")
	}
	if _, err := s.relayInput(ss); err == nil {
		t.Fatal("revoked input grant still yields a relay URL")
	}
	if !tc.hls[0].closed {
		t.Fatal("Stop left the published relay open")
	}
}

// Closing the real session between the staged snapshot and the publish must
// discard the staged result: the staged relay is closed and its grant is
// revoked instead of being transferred.
func TestIPTVCloseBetweenSnapshotAndPublishDiscardsStaged(t *testing.T) {
	tc := &stagedIPTVTranscoder{info: stagedIPTVInfo(), entered: make(chan struct{}), release: make(chan struct{})}
	s, u, channel := stagedIPTVFixture(t, tc)

	tuneDone := make(chan error, 1)
	go func() {
		_, err := s.Tune(context.Background(), u, channel.ID, core.ClientCaps{})
		tuneDone <- err
	}()
	select {
	case <-tc.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("staged IPTV tune did not reach relay startup")
	}

	views := s.ViewingSessions()
	if len(views) != 1 {
		t.Fatalf("pending tune reservations = %d", len(views))
	}
	real := liveSessionByID(s, views[0].ID)
	if real == nil {
		t.Fatal("pending tune session not registered")
	}
	if grantOf(s, real) != nil {
		t.Fatal("input grant reached the real session before the staged publish")
	}
	stage, grant := onlyGrant(t, s)
	if stage == real {
		t.Fatal("input grant was keyed to the real session before the staged publish")
	}
	if grant == nil || grant.ctx.Err() != nil {
		t.Fatal("staged input grant is not live during preparation")
	}

	// Close lands after the snapshot (and after the staged grant/relay work
	// started) but before startIPTVSession can publish.
	if err := s.Stop(context.Background(), real.info.ID, u); err != nil {
		t.Fatal(err)
	}
	close(tc.release)

	select {
	case err := <-tuneDone:
		if !errors.Is(err, core.ErrNotFound) && !errors.Is(err, context.Canceled) {
			t.Fatalf("tune closed mid-preparation err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tune did not unwind after its session was closed")
	}

	if len(tc.hls) != 1 || !tc.hls[0].closed {
		t.Fatal("staged relay was not closed when its publish was discarded")
	}
	s.inputRelay.mu.Lock()
	grants, bySess := len(s.inputRelay.grants), len(s.inputRelay.bySess)
	s.inputRelay.mu.Unlock()
	if grants != 0 || bySess != 0 {
		t.Fatalf("discarded staged tune left grants=%d bySess=%d", grants, bySess)
	}
	if grant.ctx.Err() == nil {
		t.Fatal("discarded staged input grant was not revoked")
	}
	if _, err := s.relayInput(stage); err == nil {
		t.Fatal("discarded staged grant still yields a relay URL")
	}
	if liveSessionByID(s, real.info.ID) != nil {
		t.Fatal("closed session was republished by the staged tune")
	}
}
