package collections_test

import (
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

func TestMixedRefreshFailurePreservesReleasedMembers(t *testing.T) {
	now := time.Date(2035, 4, 1, 12, 0, 0, 0, time.UTC)
	var failed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover/movie":
			fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":888991,"title":"Future","release_date":"2035-04-10"}]}`)
		case "/movie/888991":
			if failed.Load() {
				http.Error(w, "offline", 502)
				return
			}
			fmt.Fprint(w, `{"id":888991,"title":"Future","release_date":"2035-04-10"}`)
		default:
			http.Error(w, "no authoritative detail for released fixture", 502)
		}
	}))
	defer srv.Close()
	client := tmdb.New("fixture", slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithRateLimit(100000, 100000), tmdb.WithMaxRetries(0))
	defer client.Close()
	e := newEnv(t, withCatalogClient(client), withOpts(collections.WithTMDB(client), collections.WithNow(func() time.Time { return now })))
	sp := spec("Mixed refresh", "movie", catalogSource())
	sp.ReleaseMode = "mixed"
	sp.LookaheadDays = 30
	sp.Limit = 5000
	saved, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	key := core.SmartKey(mustInt(t, saved.ID))
	before, err := e.svc.Items(e.ctx, owner, key, 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if before.Total < 2 {
		t.Fatalf("need released and future members: %+v", before)
	}
	failed.Store(true)
	rebuilt, err := e.svc.Rebuild(e.ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt.Warnings) == 0 {
		t.Fatal("missing refresh warning")
	}
	after, err := e.svc.Items(e.ctx, owner, key, 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if after.Total != before.Total-1 {
		t.Fatalf("refresh removed released members: before=%d after=%d", before.Total, after.Total)
	}
	for _, item := range after.Items {
		if item.Key == core.MovieKey(888991) {
			t.Fatal("unverified future member retained")
		}
	}
}
