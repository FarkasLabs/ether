package collections_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
)

// animeMapping mirrors the community mapping document's shapes: TV ids are
// numbers, movie and IMDb ids lists, and most entries have no AniList id.
const animeMapping = `[
{"type":"TV","anidb_id":1,"anilist_id":901,"themoviedb_id":{"tv":509},"tvdb_id":7509,"imdb_id":["tt0000509"]},
{"type":"TV","anidb_id":2,"anilist_id":902,"themoviedb_id":{"tv":509},"tvdb_id":7509},
{"type":"MOVIE","anidb_id":3,"anilist_id":905,"themoviedb_id":{"movie":[101]},"imdb_id":["tt0000101"]},
{"type":"TV","anidb_id":4,"anilist_id":906,"tvdb_id":502},
{"type":"OVA","anidb_id":5,"anilist_id":null,"themoviedb_id":null},
{"type":"TV","anidb_id":6,"anilist_id":907,"themoviedb_id":{"tv":"bad"}}
]`

func TestAniListAndStevenLuSources(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Ether") {
			http.Error(w, "identify yourself", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/graphql":
			var q struct {
				Query     string `json:"query"`
				Variables struct {
					User  string `json:"user"`
					Chunk int    `json:"chunk"`
				} `json:"variables"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&q) != nil || !strings.Contains(q.Query, "MediaListCollection") {
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if q.Variables.User != "kai" {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"data":{"MediaListCollection":null},"errors":[{"message":"Private User","status":404}]}`)
				return
			}
			entry := func(id int, format, english, romaji string, year int) string {
				return fmt.Sprintf(`{"media":{"id":%d,"format":%q,"seasonYear":%d,"startDate":{"year":%d},"title":{"english":%s,"romaji":%q}}}`,
					id, format, year, year, map[bool]string{true: "null", false: fmt.Sprintf("%q", english)}[english == ""], romaji)
			}
			if q.Variables.Chunk == 1 {
				fmt.Fprintf(w, `{"data":{"MediaListCollection":{"hasNextChunk":true,"lists":[{"name":"Watching","entries":[%s,%s,%s,%s,%s]}]}}}`,
					entry(901, "TV", "Space Show", "Uchuu Show", 2013),
					entry(902, "TV", "Space Show Season 2", "Uchuu Show 2", 2015),
					entry(903, "MOVIE", "", "Spirit Valley", 2001),
					entry(904, "OVA", "Lost Tape", "Lost Tape", 1990),
					entry(906, "TV", "Office Life Anime", "Office Life Anime", 2005))
				return
			}
			fmt.Fprintf(w, `{"data":{"MediaListCollection":{"hasNextChunk":false,"lists":[{"name":"Watching","entries":[%s]},{"name":"Planning","entries":[%s]}]}}}`,
				entry(901, "TV", "Space Show", "Uchuu Show", 2013),
				entry(905, "MOVIE", "Space Saga Anime", "Uchuu Saga", 1977))
		case "/anime-list-mini.json":
			fmt.Fprint(w, animeMapping)
		case "/movies.json":
			fmt.Fprint(w, `[{"title":"Space Saga","tmdb_id":101,"imdb_id":"tt0000101","genres":["action"]},
				{"title":"Prison Hope","imdb_id":"tt0000105"}]`)
		case "/movies-metacritic-min70.json":
			fmt.Fprint(w, `[{"title":"Dream Heist","tmdb_id":107,"imdb_id":"tt0000107"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	bases := collections.ExternalBases{AniListAPI: ext.URL + "/graphql", AnimeIDs: ext.URL + "/anime-list-mini.json", StevenLu: ext.URL}
	e := newEnv(t, withOpts(collections.WithExternalBases(bases), collections.WithUserAgent("Ether/test")))
	preview := func(media string, src core.CollectionSource) *core.CollectionPreview {
		t.Helper()
		sp := spec("list", media, []core.CollectionSource{src})
		sp.Sort, sp.SortDesc = "source", false
		p, err := e.svc.Preview(e.ctx, sp, 60)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Seasons fold into one show; unmapped entries match by title; the
	// TVDB id resolves an entry with no TMDB id; across chunks a title is
	// listed once.
	all := preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "https://anilist.co/user/kai/animelist"})
	if got := strings.Join(titlesOf(all), ","); got != "Space Show,Spirit Valley,Office Life,Space Saga" {
		t.Fatalf("anilist: %s %v", got, all.Warnings)
	}
	if !strings.Contains(strings.Join(all.Warnings, " "), "1 of 6") {
		t.Fatalf("anilist should report the unmatched entries: %v", all.Warnings)
	}
	planning := preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "anilist.co/user/kai/animelist/planning"})
	if got := strings.Join(titlesOf(planning), ","); got != "Space Saga" {
		t.Fatalf("anilist named list: %s %v", got, planning.Warnings)
	}
	if hits["/anime-list-mini.json"] != 1 {
		t.Fatalf("mapping fetched %d times, want once", hits["/anime-list-mini.json"])
	}
	nope := preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "https://anilist.co/user/kai/animelist/Dropped"})
	if nope.Total != 0 || !strings.Contains(strings.Join(nope.Warnings, " "), "lists: Planning, Watching") {
		t.Fatalf("missing named list: %+v", nope)
	}
	private := preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "https://anilist.co/user/shy/animelist"})
	if private.Total != 0 || !strings.Contains(strings.Join(private.Warnings, " "), "private") {
		t.Fatalf("private user: %+v", private)
	}

	popular := preview("movie", core.CollectionSource{Kind: "stevenlu"})
	if got := strings.Join(titlesOf(popular), ","); got != "Space Saga,Prison Hope" {
		t.Fatalf("stevenlu: %s %v", got, popular.Warnings)
	}
	critics := preview("movie", core.CollectionSource{Kind: "stevenlu", List: "movies-metacritic-min70"})
	if got := strings.Join(titlesOf(critics), ","); got != "Dream Heist" {
		t.Fatalf("stevenlu variant: %s %v", got, critics.Warnings)
	}
}
