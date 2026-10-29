package livetv

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type cancelStartTranscoder struct {
	fakeTranscoder
	entered chan struct{}
}

func (f *cancelStartTranscoder) StartHLS(ctx context.Context, _ core.HLSRequest) (core.HLSSession, error) {
	close(f.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestStopCancelsBlockedHLSStart(t *testing.T) {
	pb := &fakePlayback{}
	tc := &cancelStartTranscoder{entered: make(chan struct{})}
	s := testService(t, Options{Now: func() time.Time { return time.Unix(7, 0).UTC() }, Playback: pb, Transcoder: tc})
	c := testChannel()
	if _, err := s.SaveChannel(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	done := make(chan error, 1)
	go func() { _, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{}); done <- err }()
	select {
	case <-tc.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("start not entered")
	}
	views := s.ViewingSessions()
	if len(views) != 1 {
		t.Fatalf("sessions: %d", len(views))
	}
	if err := s.Stop(context.Background(), views[0].ID, u); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("tune: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StartHLS did not observe Stop")
	}
	if len(pb.stopped) != 1 {
		t.Fatalf("playback stops: %v", pb.stopped)
	}
}

type lifetimeTranscoder struct {
	fakeTranscoder
	started chan context.Context
}

func (f *lifetimeTranscoder) StartHLS(ctx context.Context, r core.HLSRequest) (core.HLSSession, error) {
	f.started <- ctx
	return f.fakeTranscoder.StartHLS(ctx, r)
}

func TestHLSLifetimeOutlivesTuneRequest(t *testing.T) {
	pb := &fakePlayback{}
	tc := &lifetimeTranscoder{started: make(chan context.Context, 1)}
	s := testService(t, Options{Now: func() time.Time { return time.Unix(7, 0).UTC() }, Playback: pb, Transcoder: tc})
	c := testChannel()
	if _, err := s.SaveChannel(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	req, cancel := context.WithCancel(context.Background())
	info, err := s.Tune(req, u, c.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}
	lifetime := <-tc.started
	cancel()
	if lifetime.Err() != nil {
		t.Fatal("tune completion canceled encoder")
	}
	if err := s.Stop(context.Background(), info.ID, u); err != nil {
		t.Fatal(err)
	}
	if lifetime.Err() == nil {
		t.Fatal("Stop did not cancel encoder lifetime")
	}
}

func TestIPTVStopCancelsBlockedHLSStart(t *testing.T) {
	tc := &cancelIPTVTranscoder{cancelStartTranscoder: cancelStartTranscoder{entered: make(chan struct{})}}
	s, u, channel := stagedIPTVFixture(t, tc)
	done := make(chan error, 1)
	go func() { _, err := s.Tune(context.Background(), u, channel.ID, core.ClientCaps{}); done <- err }()
	select {
	case <-tc.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("start not entered")
	}
	views := s.ViewingSessions()
	if len(views) != 1 {
		t.Fatalf("sessions: %d", len(views))
	}
	if err := s.Stop(context.Background(), views[0].ID, u); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("tune: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StartHLS did not observe Stop")
	}
	s.inputRelay.mu.Lock()
	defer s.inputRelay.mu.Unlock()
	if len(s.inputRelay.bySess) != 0 {
		t.Fatal("staged input grant leaked")
	}
}

type cancelIPTVTranscoder struct{ cancelStartTranscoder }

func (f *cancelIPTVTranscoder) Probe(context.Context, string) (*core.MediaInfo, error) {
	info := stagedIPTVInfo()
	return &info, nil
}
