package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/subtitles"
)

// countingCatalog records how many keys the handler asked it to resolve.
type countingCatalog struct {
	calls    int
	keysSeen int
	titles   map[core.RatingKey]core.Title
}

func (c *countingCatalog) resolve(_ context.Context, keys []core.RatingKey) ([]core.Title, error) {
	c.calls++
	c.keysSeen += len(keys)
	out := make([]core.Title, 0, len(keys))
	for _, k := range keys {
		if t, ok := c.titles[k]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func TestSubtitleSyncHandlerResolvesTitlesBounded(t *testing.T) {
	movie := core.MovieKey(550)
	episode := core.EpisodeKey(1399, 1, 2)
	fetched := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	entries := []subtitles.CachedSubtitle{
		{ID: "aaa", Language: "pt-br", Release: "Some.Movie.2024.1080p", FetchedAt: fetched, Key: movie.String()},
		{ID: "bbb", Language: "en", Release: "Some.Show.S01E02.720p", FetchedAt: fetched, Key: episode.String()},
		// Same title again: the catalog must not be asked twice for it.
		{ID: "ccc", Language: "es", Release: "Some.Movie.2024.720p", FetchedAt: fetched, Key: movie.String()},
		// No key at all, and a key the catalog does not know: both unknown.
		{ID: "ddd", Language: "en", Release: "Orphan.2023.720p", FetchedAt: fetched},
		{ID: "eee", Language: "en", Release: "Gone.2022.720p", FetchedAt: fetched, Key: core.MovieKey(999999).String()},
	}
	cat := &countingCatalog{titles: map[core.RatingKey]core.Title{
		movie:   {Key: movie, Type: core.TypeMovie, Title: "Some Movie"},
		episode: {Key: episode, Type: core.TypeEpisode, Title: "The Episode", ShowTitle: "Some Show", Season: 1, Episode: 2},
	}}
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := subtitleSyncHandler(func() ([]subtitles.CachedSubtitle, error) { return entries, nil }, cat.resolve, fallback)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	// Bounded: one batch call, at most one key per returned entry, and
	// duplicates collapsed (movie + episode + unknown-key = 3).
	if cat.calls != 1 || cat.keysSeen != 3 || cat.keysSeen > len(entries) {
		t.Fatalf("catalog calls = %d keys = %d, want 1 call with 3 distinct keys (<= %d entries)", cat.calls, cat.keysSeen, len(entries))
	}
	// The raw rating keys must not leak into the payload.
	body := rec.Body.String()
	for _, raw := range []string{movie.String(), episode.String()} {
		if strings.Contains(body, raw) {
			t.Fatalf("payload leaks raw rating key %s: %s", raw, body)
		}
	}

	var out []subtitleSyncItem
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != len(entries) {
		t.Fatalf("items = %+v", out)
	}
	if out[0].Title != "Some Movie" || out[0].FetchedAt != fetched {
		t.Fatalf("movie row = %+v", out[0])
	}
	if out[1].Title != "The Episode" || out[1].ShowTitle != "Some Show" || out[1].Season != 1 || out[1].Episode != 2 {
		t.Fatalf("episode row = %+v", out[1])
	}
	if out[2].Title != "Some Movie" {
		t.Fatalf("duplicate-key row = %+v", out[2])
	}
	// Unknown is explicit: the title field is present and empty.
	var rawRows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rawRows); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{3, 4} {
		v, present := rawRows[i]["title"]
		if !present || v != "" {
			t.Fatalf("row %d title = %v (present %v), want explicit empty title", i, v, present)
		}
	}

	// Non-list requests still reach the subtitles service handler.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/aaa", strings.NewReader(`{}`)))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("POST status = %d, want fallback", rec.Code)
	}
}
