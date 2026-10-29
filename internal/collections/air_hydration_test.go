package collections

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
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
	s := &Service{db: db, api: newTMDBAPI(f, func() time.Time { return now })}
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
