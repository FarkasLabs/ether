package collections_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

type blockedUpcoming struct{ entered chan struct{} }

func (b blockedUpcoming) GetJSON(ctx context.Context, path string, _ url.Values, _ any) error {
	if path == "/discover/movie" {
		close(b.entered)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestUpcomingCancellation(t *testing.T) {
	b := blockedUpcoming{entered: make(chan struct{})}
	e := newEnv(t, withOpts(collections.WithTMDB(b)))
	sp := spec("Upcoming", "movie", []core.CollectionSource{{Kind: "catalog"}})
	sp.ReleaseMode, sp.LookaheadDays = "upcoming", 30
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { _, err := e.svc.Preview(ctx, sp, 20); done <- err }()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("discovery did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not return")
	}
}

func TestUpcomingPreviewPublishedAndScopedMembership(t *testing.T) {
	now := time.Date(2030, 10, 1, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover/movie" {
			if r.URL.Query().Get("primary_release_date.gte") != "2030-10-01" || r.URL.Query().Get("primary_release_date.lte") != "2030-10-31" {
				t.Errorf("bad dates: %v", r.URL.Query())
			}
			fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":3,"results":[{"id":9001,"title":"Next","release_date":"2030-10-15"},{"id":9002,"title":"Too late","release_date":"2030-11-01"},{"id":9003,"title":"Undated"}]}`)
			return
		}
		fmt.Fprint(w, `{"genres":[]}`)
	}))
	defer server.Close()
	client := tmdb.New("fixture", slog.Default(), tmdb.WithBaseURL(server.URL), tmdb.WithRateLimit(100000, 100000), tmdb.WithMaxRetries(0))
	defer client.Close()
	e := newEnv(t, withOpts(collections.WithTMDB(client), collections.WithNow(func() time.Time { return now })))
	sp := spec("Look Ahead", "movie", []core.CollectionSource{{Kind: "catalog"}})
	sp.ReleaseMode, sp.LookaheadDays = "upcoming", 30
	sp.Targets.Users = core.UserTargets{IDs: []int64{owner.ID}}
	preview, err := e.svc.Preview(e.ctx, sp, 20)
	if err != nil || preview.Total != 1 || preview.Items[0].Key != core.MovieKey(9001) {
		t.Fatalf("preview %+v %v", preview, err)
	}
	saved, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	ck := core.SmartKey(mustInt(t, saved.ID))
	page, err := e.svc.Items(e.ctx, owner, ck, 0, 20)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Key != core.MovieKey(9001) {
		t.Fatalf("page %+v %v saved %+v", page, err, saved)
	}
	if _, err = e.cat.Get(e.ctx, core.MovieKey(9001)); err == nil {
		t.Fatal("future title globally visible")
	}
	if _, err = e.svc.Placeholder(e.ctx, owner, ck, core.MovieKey(9001), "web"); err != nil {
		t.Fatal(err)
	}
	if _, got, err := e.svc.FindPlaceholder(e.ctx, owner, core.MovieKey(9001), "web"); err != nil || got != ck {
		t.Fatalf("canonical lookup: %s %v", got, err)
	}
	stored, err := e.cat.StoredTitle(e.ctx, core.MovieKey(9001))
	if err != nil {
		t.Fatal(err)
	}
	originalDate := stored.Released
	stored.Released = now.AddDate(0, 0, 45)
	if err := e.cat.UpsertTitle(e.ctx, *stored, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.FindPlaceholder(e.ctx, owner, stored.Key, "web"); err == nil {
		t.Fatal("changed date outside the window admitted")
	}
	stored.Released = originalDate
	if err := e.cat.UpsertTitle(e.ctx, *stored, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.FindPlaceholder(e.ctx, &core.User{ID: 99}, core.MovieKey(9001), "web"); err == nil {
		t.Fatal("untargeted viewer admitted")
	}
	sp.Targets.Schedule = &core.CollectionSchedule{Devices: []string{core.DeviceTV}}
	if _, err := e.svc.UpdateSpec(e.ctx, saved.ID, sp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.FindPlaceholder(e.ctx, owner, core.MovieKey(9001), core.DeviceWeb); err == nil {
		t.Fatal("device schedule ignored")
	}
	if _, _, err := e.svc.FindPlaceholder(e.ctx, owner, core.MovieKey(9001), core.DeviceTV); err != nil {
		t.Fatalf("scheduled device denied: %v", err)
	}
	sp.Targets.Schedule = nil
	if _, err := e.svc.UpdateSpec(e.ctx, saved.ID, sp); err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.Placeholder(e.ctx, owner, ck, core.MovieKey(9002), "web"); err == nil {
		t.Fatal("nonmember admitted")
	}
	now = now.AddDate(0, 0, 15)
	if _, err = e.svc.Placeholder(e.ctx, owner, ck, core.MovieKey(9001), "web"); err == nil {
		t.Fatal("released placeholder admitted")
	}
	if _, _, err := e.svc.FindPlaceholder(e.ctx, owner, core.MovieKey(9001), "web"); err == nil {
		t.Fatal("released title authorized by canonical lookup")
	}
	page, err = e.svc.Items(e.ctx, owner, ck, 0, 20)
	if err != nil || page.Total != 0 {
		t.Fatalf("boundary page %+v %v", page, err)
	}
	now = now.AddDate(0, 0, -15)
	sp.ReleaseMode = "released"
	if _, err := e.svc.UpdateSpec(e.ctx, saved.ID, sp); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Placeholder(e.ctx, owner, ck, core.MovieKey(9001), "web"); err == nil {
		t.Fatal("edited collection retained placeholder access")
	}
	page, err = e.svc.Items(e.ctx, owner, ck, 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Key == core.MovieKey(9001) {
			t.Fatal("future item leaked into released-only collection")
		}
	}
	sp.ReleaseMode = "upcoming"
	sp.Enabled = false
	if _, err := e.svc.UpdateSpec(e.ctx, saved.ID, sp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.FindPlaceholder(e.ctx, owner, core.MovieKey(9001), "web"); err == nil {
		t.Fatal("disabled collection admitted")
	}
	if err := e.svc.DeleteSpec(e.ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.FindPlaceholder(e.ctx, owner, core.MovieKey(9001), "web"); err == nil {
		t.Fatal("deleted collection admitted")
	}
}

func TestUpcomingRejectsUnsupportedSources(t *testing.T) {
	sp := spec("Upcoming", "movie", []core.CollectionSource{{Kind: "titles"}})
	sp.ReleaseMode = "upcoming"
	sp.LookaheadDays = 30
	if collections.Validate(&sp) == nil {
		t.Fatal("unsupported source accepted")
	}
	sp.Sources = []core.CollectionSource{{Kind: "catalog"}}
	sp.LookaheadDays = 367
	if collections.Validate(&sp) == nil {
		t.Fatal("unbounded window accepted")
	}
}

func TestUpcomingSourceFailureWarns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "offline", http.StatusBadGateway) }))
	defer server.Close()
	client := tmdb.New("fixture", slog.Default(), tmdb.WithBaseURL(server.URL), tmdb.WithRateLimit(100000, 100000), tmdb.WithMaxRetries(0))
	defer client.Close()
	e := newEnv(t, withOpts(collections.WithTMDB(client)))
	sp := spec("Upcoming", "movie", []core.CollectionSource{{Kind: "catalog"}})
	sp.ReleaseMode, sp.LookaheadDays = "upcoming", 30
	p, err := e.svc.Preview(e.ctx, sp, 20)
	if err != nil || len(p.Warnings) == 0 {
		t.Fatalf("missing source failure warning: %+v %v", p, err)
	}
}
