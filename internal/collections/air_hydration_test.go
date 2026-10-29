package collections

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

type failingAirTMDB struct{ calls atomic.Int64 }

func (f *failingAirTMDB) GetJSON(context.Context, string, url.Values, any) error {
	f.calls.Add(1)
	return errors.New("offline")
}

func TestAirHydrationBudgetAndFailureCooldown(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f := &failingAirTMDB{}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := &Service{db: db, api: newTMDBAPI(f, func() time.Time { return now }), airGate: make(chan struct{}, 1)}
	cs := make([]*cand, 150)
	for i := range cs {
		cs[i] = &cand{e: &entry{key: core.ShowKey(int64(i + 1)), typ: core.TypeShow}}
	}
	unknown, _, err := s.hydrateAirDates(ctx, cs, "latestEpisode", now, true)
	if err != nil || unknown != 150 || f.calls.Load() != 100 {
		t.Fatalf("first: unknown=%d calls=%d err=%v", unknown, f.calls.Load(), err)
	}
	_, _, err = s.hydrateAirDates(ctx, cs, "latestEpisode", now, true)
	if err != nil || f.calls.Load() != 150 {
		t.Fatalf("second: calls=%d err=%v", f.calls.Load(), err)
	}
	_, _, err = s.hydrateAirDates(ctx, cs, "latestEpisode", now, true)
	if err != nil || f.calls.Load() != 150 {
		t.Fatalf("cooldown: calls=%d err=%v", f.calls.Load(), err)
	}
}

type detailsAirTMDB struct{ body string }

func (f detailsAirTMDB) GetJSON(_ context.Context, path string, _ url.Values, out any) error {
	if strings.Contains(path, "/season/") {
		return json.Unmarshal([]byte(`{"episodes":[{"episode_number":1,"air_date":"2026-01-01"},{"episode_number":2,"air_date":"2026-01-08"},{"episode_number":3,"air_date":"2026-11-01"}]}`), out)
	}
	return json.Unmarshal([]byte(f.body), out)
}

func TestAirDetailsExcludeSpecialsAndFuture(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		body              string
		episode, premiere string
	}{
		{`{"last_episode_to_air":{"season_number":0,"episode_number":2,"air_date":"2026-10-28"},"seasons":[{"season_number":1,"episode_count":4,"air_date":"2026-01-01"},{"season_number":2,"episode_count":4,"air_date":"2026-11-01"}]}`, "2026-01-08", "2026-01-01"},
		{`{"last_episode_to_air":{"season_number":2,"episode_number":2,"air_date":"2026-10-28"},"seasons":[{"season_number":1,"episode_count":4,"air_date":"2026-01-01"},{"season_number":2,"episode_count":4,"air_date":""}]}`, "2026-10-28", ""},
	}
	for _, tc := range cases {
		s := &Service{api: newTMDBAPI(detailsAirTMDB{tc.body}, func() time.Time { return now })}
		r, err := s.fetchAirRecord(context.Background(), core.ShowKey(1), now)
		if err != nil {
			t.Fatal(err)
		}
		unix := func(v string) int64 {
			if v == "" {
				return 0
			}
			d, _ := time.Parse("2006-01-02", v)
			return d.Unix()
		}
		if r.episode != unix(tc.episode) || r.premiere != unix(tc.premiere) {
			t.Fatalf("got %+v want episode=%s premiere=%s", r, tc.episode, tc.premiere)
		}
	}
}

// Signal that hydration has reached the cancellation-aware admission select.
type admissionObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *admissionObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestAirHydrationCanceledWhileWaiting(t *testing.T) {
	s := &Service{airGate: make(chan struct{}, 1)}
	s.airGate <- struct{}{} // another evaluation owns admission
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &admissionObservedContext{Context: ctx, observed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, _, err := s.hydrateAirDates(observed, nil, "latestEpisode", time.Now(), false)
		done <- err
	}()
	// The only other holder remains active throughout: cancellation must not
	// need it to finish, nor steal its admission token.
	select {
	case <-observed.observed:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("hydration did not reach admission")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled hydration still waiting for another evaluation")
	}
	if len(s.airGate) != 1 {
		t.Fatal("waiting cancellation released another evaluation's admission")
	}
	<-s.airGate
}
