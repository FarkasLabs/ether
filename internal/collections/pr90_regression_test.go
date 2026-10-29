package collections_test

import (
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

func TestUpcomingProviderJoinParity(t *testing.T) {
	now := time.Date(2035, 4, 1, 12, 0, 0, 0, time.UTC)
	for _, exclude := range []bool{false, true} {
		t.Run(fmt.Sprint(exclude), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/discover/movie" {
					if r.URL.Query().Get("primary_release_date.gte") == "" {
						if exclude {
							fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":888992,"title":"Released","release_date":"2030-01-01"}]}`)
						} else {
							fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":2,"results":[{"id":888991,"title":"Future","release_date":"2035-04-10"},{"id":888992,"title":"Released","release_date":"2030-01-01"}]}`)
						}
						return
					}
					fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":888991,"title":"Future","release_date":"2035-04-10"}]}`)
					return
				}
				if r.URL.Path == "/movie/888991" {
					fmt.Fprint(w, `{"id":888991,"title":"Future","release_date":"2035-04-10"}`)
					return
				}
				fmt.Fprint(w, `{"genres":[]}`)
			}))
			defer srv.Close()
			client := tmdb.New("fixture", slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithRateLimit(100000, 100000), tmdb.WithMaxRetries(0))
			defer client.Close()
			e := newEnv(t, withCatalogClient(client), withOpts(collections.WithTMDB(client), collections.WithNow(func() time.Time { return now })))
			sp := spec("Provider", "movie", catalogSource())
			sp.ReleaseMode = "upcoming"
			sp.LookaheadDays = 30
			rule := rl("watchProvider", "is", map[string]any{"id": 8, "name": "Provider"})
			if exclude {
				sp.Exclude = []core.CollectionRule{rule}
			} else {
				sp.Rules = []core.CollectionRule{rule}
			}
			p, err := e.svc.Preview(e.ctx, sp, 50)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := e.svc.CreateSpec(e.ctx, sp)
			if err != nil {
				t.Fatal(err)
			}
			page, err := e.svc.Items(e.ctx, owner, core.SmartKey(mustInt(t, saved.ID)), 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			if p.Total != 1 || page.Total != 1 || page.Items[0].Key != core.MovieKey(888991) {
				t.Fatalf("preview=%+v published=%+v", p, page)
			}
		})
	}
}
