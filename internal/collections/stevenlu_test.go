package collections_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestStevenLuSource(t *testing.T) {
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Ether") {
			http.Error(w, "identify yourself", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/movies.json":
			fmt.Fprint(w, `[{"title":"Space Saga","tmdb_id":101,"imdb_id":"tt0000101","genres":["action"]},
				{"title":"Prison Hope","imdb_id":"tt0000105"}]`)
		case "/movies-metacritic-min70.json":
			fmt.Fprint(w, `[{"title":"Dream Heist","tmdb_id":107,"imdb_id":"tt0000107"}]`)
		case "/movies-imdb-min8.json":
			http.Error(w, "down", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	e := newEnv(t, withOpts(collections.WithExternalBases(collections.ExternalBases{StevenLu: ext.URL}), collections.WithUserAgent("Ether/test")))
	preview := func(src core.CollectionSource) *core.CollectionPreview {
		t.Helper()
		sp := spec("list", "movie", []core.CollectionSource{src})
		sp.Sort, sp.SortDesc = "source", false
		p, err := e.svc.Preview(e.ctx, sp, 60)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// The plain list is the default; an item without a TMDB id matches by IMDb.
	popular := preview(core.CollectionSource{Kind: "stevenlu"})
	if got := strings.Join(titlesOf(popular), ","); got != "Space Saga,Prison Hope" {
		t.Fatalf("stevenlu: %s %v", got, popular.Warnings)
	}
	critics := preview(core.CollectionSource{Kind: "stevenlu", List: "movies-metacritic-min70"})
	if got := strings.Join(titlesOf(critics), ","); got != "Dream Heist" {
		t.Fatalf("stevenlu variant: %s %v", got, critics.Warnings)
	}
	down := preview(core.CollectionSource{Kind: "stevenlu", List: "movies-imdb-min8"})
	if down.Total != 0 || !strings.Contains(strings.Join(down.Warnings, " "), "stevenlu") {
		t.Fatalf("outage: %+v", down)
	}
}
