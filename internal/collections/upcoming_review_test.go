package collections_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

func TestReviewUpcomingModesAndDateCorrection(t *testing.T) {
	now := time.Date(2035, 4, 1, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover/movie" {
			fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":888991,"title":"Tomorrow Review","release_date":"2035-04-10"}]}`)
			return
		}
		fmt.Fprint(w, `{"genres":[]}`)
	}))
	defer srv.Close()
	client := tmdb.New("fixture", slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithRateLimit(100000, 100000), tmdb.WithMaxRetries(0))
	defer client.Close()
	e := newEnv(t, withOpts(collections.WithTMDB(client), collections.WithNow(func() time.Time { return now })))
	future := core.MovieKey(888991)
	for _, mode := range []string{"released", "upcoming", "mixed"} {
		sp := spec("Review "+mode, "movie", catalogSource())
		sp.ReleaseMode = mode
		if mode != "released" {
			sp.LookaheadDays = 30
		}
		p, err := e.svc.Preview(e.ctx, sp, 200)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := e.svc.CreateSpec(e.ctx, sp)
		if err != nil {
			t.Fatal(err)
		}
		ck := core.SmartKey(mustInt(t, saved.ID))
		page, err := e.svc.Items(e.ctx, owner, ck, 0, 5000)
		if err != nil {
			t.Fatal(err)
		}
		hasPreview, hasPage := false, false
		for _, x := range p.Items {
			if x.Key == future {
				hasPreview = true
			}
		}
		for _, x := range page.Items {
			if x.Key == future {
				hasPage = true
			}
		}
		want := mode != "released"
		if hasPreview != want || hasPage != want || p.Total != page.Total {
			t.Fatalf("%s preview=%v/%d page=%v/%d", mode, hasPreview, p.Total, hasPage, page.Total)
		}
		if mode == "upcoming" {
			title, err := e.cat.StoredTitle(e.ctx, future)
			if err != nil {
				t.Fatal(err)
			}
			title.Released = now.AddDate(0, 0, 60)
			if err := e.cat.UpsertTitle(e.ctx, *title, false); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.Placeholder(e.ctx, owner, ck, future, "web"); err == nil {
				t.Fatal("corrected date still authorized")
			}
			title.Released = time.Date(2035, 4, 10, 0, 0, 0, 0, time.UTC)
			if err := e.cat.UpsertTitle(e.ctx, *title, false); err != nil {
				t.Fatal(err)
			}
			sp.Enabled = false
			if _, err := e.svc.UpdateSpec(e.ctx, saved.ID, sp); err != nil {
				t.Fatal(err)
			}
			if _, _, err := e.svc.FindPlaceholder(e.ctx, owner, future, "web"); err == nil {
				t.Fatal("disabled collection still authorized")
			}
			if err := e.svc.DeleteSpec(e.ctx, saved.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestReviewUpcomingSaveSurfacesDiscoveryFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "offline", http.StatusBadGateway) }))
	defer srv.Close()
	client := tmdb.New("fixture", slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithRateLimit(100000, 100000), tmdb.WithMaxRetries(0))
	defer client.Close()
	e := newEnv(t, withOpts(collections.WithTMDB(client)))
	sp := spec("Offline review", "movie", catalogSource())
	sp.ReleaseMode = "upcoming"
	sp.LookaheadDays = 30
	saved, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Warnings) == 0 {
		t.Fatal("save hid discovery failure")
	}
	page, err := e.svc.Items(e.ctx, owner, core.SmartKey(mustInt(t, saved.ID)), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("offline produced %d future titles", page.Total)
	}
}

func TestReviewCanceledDiscoveryDoesNotPublish(t *testing.T) {
	b := blockedUpcoming{entered: make(chan struct{})}
	e := newEnv(t, withOpts(collections.WithTMDB(b)))
	sp := spec("Canceled review", "movie", catalogSource())
	sp.ReleaseMode = "upcoming"
	sp.LookaheadDays = 10
	ctx, cancel := context.WithCancel(e.ctx)
	result := make(chan error, 1)
	go func() { _, err := e.svc.Preview(ctx, sp, 10); result <- err }()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("discovery did not begin")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not stop")
	}
}
