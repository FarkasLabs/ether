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

func TestAniListSource(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	calls := map[string]int{} // GraphQL requests per user
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
					Per   int    `json:"per"`
				} `json:"variables"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&q) != nil || !strings.Contains(q.Query, "MediaListCollection") {
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			mu.Lock()
			calls[q.Variables.User]++
			n := calls[q.Variables.User]
			mu.Unlock()
			one := `{"media":{"id":901,"format":"TV","seasonYear":2013,"title":{"english":"Space Show"}}}`
			switch q.Variables.User {
			case "flaky": // a GraphQL failure inside HTTP 200, then recovery
				if n == 1 {
					fmt.Fprint(w, `{"data":null,"errors":[{"message":"Internal Server Error","status":500}]}`)
					return
				}
				fmt.Fprintf(w, `{"data":{"MediaListCollection":{"hasNextChunk":false,"lists":[{"name":"Watching","entries":[%s]}]}}}`, one)
				return
			case "halfway": // the second part fails once
				if q.Variables.Chunk == 2 && n == 2 {
					http.Error(w, "busy", http.StatusBadGateway)
					return
				}
				if q.Variables.Chunk == 1 {
					fmt.Fprintf(w, `{"data":{"MediaListCollection":{"hasNextChunk":true,"lists":[{"name":"Watching","entries":[%s]}]}}}`, one)
					return
				}
				fmt.Fprint(w, `{"data":{"MediaListCollection":{"hasNextChunk":false,"lists":[{"name":"Planning","entries":[{"media":{"id":905,"format":"MOVIE","title":{"english":"Space Saga Anime"}}}]}]}}}`)
				return
			case "endless": // more parts than Ether reads
				if q.Variables.Per != 500 {
					http.Error(w, "bad page size", http.StatusBadRequest)
					return
				}
				fmt.Fprintf(w, `{"data":{"MediaListCollection":{"hasNextChunk":true,"lists":[{"name":"Watching","entries":[%s]}]}}}`, one)
				return
			}
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
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	bases := collections.ExternalBases{AniListAPI: ext.URL + "/graphql", AnimeIDs: ext.URL + "/anime-list-mini.json"}
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

	// A GraphQL error inside HTTP 200 is not cached: the next refresh
	// reaches the recovered server.
	flaky := preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "https://anilist.co/user/flaky/animelist"})
	if flaky.Total != 0 || !strings.Contains(strings.Join(flaky.Warnings, " "), "Internal Server Error") {
		t.Fatalf("graphql error: %+v", flaky)
	}
	flaky = preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "https://anilist.co/user/flaky/animelist"})
	if got := strings.Join(titlesOf(flaky), ","); got != "Space Show" || calls["flaky"] != 2 {
		t.Fatalf("after recovery: %s (%d calls) %v", got, calls["flaky"], flaky.Warnings)
	}

	// A failed later part fails the source instead of passing off half a
	// list as the whole one; the next refresh reads it all.
	half := preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "https://anilist.co/user/halfway/animelist"})
	if half.Total != 0 || !strings.Contains(strings.Join(half.Warnings, " "), "could not read all") {
		t.Fatalf("partial list: %+v", half)
	}
	half = preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "https://anilist.co/user/halfway/animelist"})
	if got := strings.Join(titlesOf(half), ","); got != "Space Show,Space Saga" {
		t.Fatalf("after recovery: %s %v", got, half.Warnings)
	}

	// A list longer than the page cap is used, with a warning saying so.
	long := preview("mixed", core.CollectionSource{Kind: "anilistList", URL: "https://anilist.co/user/endless/animelist"})
	if got := strings.Join(titlesOf(long), ","); got != "Space Show" || !strings.Contains(strings.Join(long.Warnings, " "), "only the first 5000") {
		t.Fatalf("capped list: %s %v", got, long.Warnings)
	}
	if calls["endless"] != 10 {
		t.Fatalf("capped list read %d parts, want 10", calls["endless"])
	}
}
