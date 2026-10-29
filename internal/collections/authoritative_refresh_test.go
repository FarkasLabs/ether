package collections_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

func TestUpcomingAuthoritativeRefresh(t *testing.T) {
	now := time.Date(2035, 4, 1, 12, 0, 0, 0, time.UTC)
	var date atomic.Value
	date.Store("2035-04-10")
	var mode atomic.Int32
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover/movie":
			results := `[]`
			if date.Load().(string) <= "2035-04-30" && date.Load().(string) != "" {
				results = fmt.Sprintf(`[{"id":90001,"title":"Film","release_date":%q}]`, date.Load().(string))
			}
			fmt.Fprintf(w, `{"page":1,"total_pages":1,"total_results":1,"results":%s}`, results)
		case "/movie/90001":
			if mode.Load() == 1 {
				http.Error(w, "offline", 502)
				return
			}
			if mode.Load() == 2 {
				entered <- struct{}{}
				<-r.Context().Done()
				return
			}
			fmt.Fprintf(w, `{"id":90001,"title":"Film","release_date":%q,"overview":"Detailed synopsis","runtime":120,"poster_path":"/detail.jpg","genres":[{"id":18,"name":"Drama"}]}`, date.Load().(string))
		default:
			fmt.Fprint(w, `{"genres":[]}`)
		}
	}))
	defer srv.Close()
	client := tmdb.New("fixture", slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithRateLimit(100000, 100000), tmdb.WithMaxRetries(0))
	defer client.Close()
	e := newEnv(t, withCatalogClient(client), withOpts(collections.WithTMDB(client), collections.WithNow(func() time.Time { return now })))
	key := core.MovieKey(90001)
	if _, err := e.cat.RefreshTitle(e.ctx, key, true); err != nil {
		t.Fatal(err)
	}
	sp := spec("Authoritative", "movie", catalogSource())
	sp.ReleaseMode = "upcoming"
	sp.LookaheadDays = 30
	if _, err := client.MovieDetails(e.ctx, key.TMDB()); err != nil {
		t.Fatal(err)
	}
	date.Store("2035-04-20")
	preview, err := e.svc.Preview(e.ctx, sp, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 1 || preview.Items[0].Key != key || preview.Items[0].Released.Format("2006-01-02") != "2035-04-20" {
		t.Fatalf("preview: %+v", preview)
	}
	stored, err := e.cat.StoredTitle(e.ctx, key)
	if err != nil || stored.Released.Format("2006-01-02") != "2035-04-10" {
		t.Fatalf("preview wrote catalog: %+v %v", stored, err)
	}
	normal, err := client.MovieDetails(e.ctx, key.TMDB())
	if err != nil || normal.ReleaseDate != "2035-04-20" {
		t.Fatalf("stale ordinary TMDB cache: %+v %v", normal, err)
	}
	saved, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	ck := core.SmartKey(mustInt(t, saved.ID))
	stored, err = e.cat.StoredTitle(e.ctx, key)
	if err != nil || stored.Released.Format("2006-01-02") != "2035-04-20" || stored.Summary != "Detailed synopsis" || stored.Runtime != 120*time.Minute || stored.PosterPath != "/detail.jpg" {
		t.Fatalf("persisted details: %+v %v", stored, err)
	}
	date.Store("2035-07-01")
	if _, err := e.svc.Rebuild(e.ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	stored, err = e.cat.StoredTitle(e.ctx, key)
	if err != nil || stored.Released.Format("2006-01-02") != "2035-07-01" {
		t.Fatalf("postponement: %+v %v", stored, err)
	}
	if _, err := e.svc.Placeholder(e.ctx, owner, ck, key, "web"); err == nil {
		t.Fatal("postponed title authorized")
	}
	if _, err := e.cat.Get(e.ctx, key); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("future title passed catalog guard: %v", err)
	}
	date.Store("2035-04-25")
	if _, err := e.svc.Rebuild(e.ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Placeholder(e.ctx, owner, ck, key, "web"); err != nil {
		t.Fatalf("restored member: %v", err)
	}
	date.Store("")
	unknown, err := e.svc.Rebuild(e.ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown.Warnings) == 0 {
		t.Fatal("unknown date not reported")
	}
	stored, err = e.cat.StoredTitle(e.ctx, key)
	if err != nil || !stored.Released.IsZero() {
		t.Fatalf("unknown date not persisted: %+v %v", stored, err)
	}
	if _, err := e.svc.Placeholder(e.ctx, owner, ck, key, "web"); err == nil {
		t.Fatal("unknown date admitted")
	}
	date.Store("2035-04-25")
	if _, err := e.svc.Rebuild(e.ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	mode.Store(1)
	rebuilt, err := e.svc.Rebuild(e.ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt.Warnings) == 0 {
		t.Fatal("refresh failure not reported")
	}
	if _, err := e.svc.Placeholder(e.ctx, owner, ck, key, "web"); err == nil {
		t.Fatal("failed refresh admitted stale title")
	}
	mode.Store(2)
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { _, err := e.svc.Preview(ctx, sp, 50); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("refresh did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not cancel")
	}
}
