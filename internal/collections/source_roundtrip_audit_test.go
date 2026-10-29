package collections_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestSourcePreviewToPublishedRoundTrip(t *testing.T) {
	cases := []struct {
		name, media string
		source      core.CollectionSource
		expected    core.RatingKey
	}{
		{"catalog", "movie", core.CollectionSource{Kind: "catalog"}, core.MovieKey(107)},
		{"titles", "movie", core.CollectionSource{Kind: "titles", Keys: []string{core.MovieKey(101).String()}}, core.MovieKey(101)},
		{"list", "movie", core.CollectionSource{Kind: "tmdbList", List: "popular"}, core.MovieKey(107)},
		{"franchise", "movie", core.CollectionSource{Kind: "tmdbCollection", ID: 10}, core.MovieKey(101)},
		{"discover", "movie", core.CollectionSource{Kind: "tmdbDiscover", Params: map[string]string{"with_keywords": "1001"}}, core.MovieKey(107)},
		{"keyword", "movie", core.CollectionSource{Kind: "tmdbKeyword", ID: 1001}, core.MovieKey(107)},
		{"company", "movie", core.CollectionSource{Kind: "tmdbCompany", ID: 1}, core.MovieKey(101)},
		{"network", "show", core.CollectionSource{Kind: "tmdbNetwork", ID: 174}, core.ShowKey(501)},
		{"person", "movie", core.CollectionSource{Kind: "tmdbPerson", ID: 31, Role: "director"}, core.MovieKey(107)},
		{"provider", "mixed", core.CollectionSource{Kind: "watchProvider", ID: 8}, core.ShowKey(501)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			sp := spec("Source "+tc.name, tc.media, []core.CollectionSource{tc.source})
			sp.Sort = "source"
			sp.SortDesc = false
			preview, err := e.svc.Preview(e.ctx, sp, 200)
			if err != nil {
				t.Fatal(err)
			}
			var want []core.RatingKey
			for _, item := range preview.Items {
				want = append(want, item.Key)
			}
			if !slices.Contains(want, tc.expected) {
				t.Fatalf("expected %v in preview %v warnings=%v", tc.expected, want, preview.Warnings)
			}
			created, err := e.svc.CreateSpec(e.ctx, sp)
			if err != nil {
				t.Fatal(err)
			}
			page, err := e.svc.Items(e.ctx, owner, core.SmartKey(mustInt(t, created.ID)), 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			var got []core.RatingKey
			for _, item := range page.Items {
				got = append(got, item.Key)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("preview %v published %v warnings=%v", want, got, created.Warnings)
			}
		})
	}
}

func TestExternalSourcePreviewToPublishedRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/users/"):
			fmt.Fprint(w, `[{"type":"movie","movie":{"title":"Space Saga","ids":{"tmdb":101}}}]`)
		case strings.Contains(r.URL.Path, "/someone/list/"):
			fmt.Fprint(w, `<div class="poster" data-film-slug="space-saga" data-target-link="/film/space-saga/"><img alt="Space Saga" /></div>`)
		case strings.Contains(r.URL.Path, "/lists/"):
			fmt.Fprint(w, `[{"id":101,"title":"Space Saga","mediatype":"movie"}]`)
		case r.URL.Path == "/graphql":
			fmt.Fprint(w, `{"data":{"MediaListCollection":{"hasNextChunk":false,"lists":[{"name":"Watching","entries":[{"media":{"id":905,"format":"MOVIE","title":{"english":"Space Saga"}}}]}]}}}`)
		case r.URL.Path == "/mapping":
			fmt.Fprint(w, animeMapping)
		case r.URL.Path == "/movies.json":
			fmt.Fprint(w, `[{"title":"Space Saga","tmdb_id":101}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	bases := collections.ExternalBases{TraktAPI: server.URL, Letterboxd: server.URL, MDBList: server.URL, AniListAPI: server.URL + "/graphql", AnimeIDs: server.URL + "/mapping", StevenLu: server.URL}
	cases := []core.CollectionSource{{Kind: "traktList", URL: "https://trakt.tv/users/alice/lists/greats"}, {Kind: "letterboxdList", URL: "https://letterboxd.com/someone/list/favourites/"}, {Kind: "mdblist", URL: "https://mdblist.com/lists/bob/top-picks"}, {Kind: "anilistList", URL: "https://anilist.co/user/kai/animelist"}, {Kind: "stevenlu"}}
	for _, src := range cases {
		t.Run(src.Kind, func(t *testing.T) {
			e := newEnv(t, withOpts(collections.WithExternalBases(bases), collections.WithTraktConfig(func() config.Trakt { return config.Trakt{ClientID: "fixture"} })))
			sp := spec("External "+src.Kind, "movie", []core.CollectionSource{src})
			sp.Sort = "source"
			p, err := e.svc.Preview(e.ctx, sp, 20)
			if err != nil || len(p.Items) != 1 || p.Items[0].Key != core.MovieKey(101) {
				t.Fatalf("preview: %+v %v", p, err)
			}
			created, err := e.svc.CreateSpec(e.ctx, sp)
			if err != nil {
				t.Fatal(err)
			}
			page, err := e.svc.Items(e.ctx, owner, core.SmartKey(mustInt(t, created.ID)), 0, 20)
			if err != nil || len(page.Items) != 1 || page.Items[0].Key != p.Items[0].Key {
				t.Fatalf("published: %+v %v", page, err)
			}
		})
	}
}
