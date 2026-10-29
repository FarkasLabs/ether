package subtitles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func writeCacheEntry(t *testing.T, dir, id, meta string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The cache meta has no title: Key must come from the selection index and
// FetchedAt from the meta. An entry without an index file keeps Key empty
// (unknown title upstream) — no key is invented.
func TestCachedSubtitlesKeyAndFetchedAt(t *testing.T) {
	s := New(Options{CacheDir: t.TempDir()})
	fetched := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	writeCacheEntry(t, s.dir, "abc123",
		`{"fileId":7,"language":"pt-br","release":"Some.Movie.2024.1080p.WEB-DL","fetchedAt":"`+fetched.Format(time.RFC3339)+`"}`)
	writeCacheEntry(t, s.dir, "orphan99",
		`{"fileId":9,"language":"en","release":"Other.Movie.2023.720p","fetchedAt":"`+fetched.Add(-time.Hour).Format(time.RFC3339)+`"}`)

	// Index entry for the episode this subtitle was remembered for.
	title := &core.Title{Key: core.EpisodeKey(1399, 1, 2)}
	s.saveSelection(title, "pt-br", "Some.Movie.2024.1080p.WEB-DL",
		selection{ID: "abc123", FileID: 7, Language: "pt-br", Release: "Some.Movie.2024.1080p.WEB-DL", At: fetched})

	items, err := s.CachedSubtitles()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	byID := map[string]CachedSubtitle{}
	for _, it := range items {
		byID[it.ID] = it
	}
	got := byID["abc123"]
	if got.Key != title.Key.String() {
		t.Fatalf("key = %q, want rating key %q from index", got.Key, title.Key.String())
	}
	if !got.FetchedAt.Equal(fetched) || got.Language != "pt-br" || got.Release != "Some.Movie.2024.1080p.WEB-DL" {
		t.Fatalf("meta fields lost: %+v", got)
	}
	if orphan := byID["orphan99"]; orphan.Key != "" {
		t.Fatalf("orphan key = %q, want empty (unknown)", orphan.Key)
	}
	// Newest first.
	if items[0].ID != "abc123" {
		t.Fatalf("order = %+v", items)
	}
	// The raw rating key is server-internal: it must not marshal to JSON.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), title.Key.String()) || strings.Contains(string(raw), `"key"`) {
		t.Fatalf("cached subtitle JSON leaks raw key: %s", raw)
	}
}

// Without fetchedAt in the meta, FetchedAt falls back to the .srt mtime.
func TestCachedSubtitlesFetchedAtFallsBackToMtime(t *testing.T) {
	s := New(Options{CacheDir: t.TempDir()})
	writeCacheEntry(t, s.dir, "nomtime1", `{"fileId":1,"language":"en","release":"X.2024"}`)
	mtime := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(s.dir, "nomtime1.srt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	items, err := s.CachedSubtitles()
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v err = %v", items, err)
	}
	if !items[0].FetchedAt.Equal(mtime) {
		t.Fatalf("fetchedAt = %v, want mtime %v", items[0].FetchedAt, mtime)
	}
	if items[0].Key != "" {
		t.Fatalf("key = %q, want empty with no index at all", items[0].Key)
	}
}
