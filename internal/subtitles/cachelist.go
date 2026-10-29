package subtitles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CachedSubtitle is one downloaded sidecar as listed for the owner UI.
//
// The per-file meta (<id>.json, written by fetchAndCache) stores only
// fileId, language, release and fetchedAt, so the title's name cannot be
// recovered from the cache files themselves. Key is the title's rating key,
// recovered from the selection index (see indexKeysByID), and is
// server-internal only: the app handler resolves it to a real title via the
// catalog and never sends the raw key to the UI. Empty when no index entry
// points at this file — the caller then reports the title as unknown
// instead of inventing one. FetchedAt falls back to the sidecar's mtime
// when the meta carries no timestamp.
type CachedSubtitle struct {
	ID        string    `json:"id"`
	Language  string    `json:"language"`
	Release   string    `json:"release"`
	FetchedAt time.Time `json:"fetchedAt"`
	Key       string    `json:"-"`
}

func (s *Service) CachedSubtitles() ([]CachedSubtitle, error) {
	out := []CachedSubtitle{}
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if len(out) >= 1000 {
			break
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".sync.json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !s.cached(id, "srt") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil || len(raw) > 64<<10 {
			continue
		}
		var c CachedSubtitle
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		c.ID = id
		if c.FetchedAt.IsZero() {
			if info, err := os.Stat(filepath.Join(s.dir, id+".srt")); err == nil {
				c.FetchedAt = info.ModTime().UTC()
			}
		}
		out = append(out, c)
	}
	// Resolve keys only for the entries actually returned (bounded by the
	// 1000-entry cap above), never for the whole cache.
	if len(out) > 0 {
		want := make(map[string]bool, len(out))
		for _, c := range out {
			want[c.ID] = true
		}
		keys := s.indexKeysByID(want)
		for i := range out {
			out[i].Key = keys[out[i].ID]
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FetchedAt.After(out[j].FetchedAt) })
	return out, nil
}

// indexKeysByID maps cache id -> title rating key for the wanted ids only,
// using the selection index (index/<ratingKey>.<lang>.<releaseSig>.json,
// written by saveSelection, whose stored selection id is the cache id).
//
// Bounded by design: index directory names are read in batches and each
// file's contents are read only until every wanted id has been resolved,
// at which point the scan stops. Entries with no index file (orphans) are
// the only ones that force the scan to run to the end — proving their
// title is unknown requires it. When several index files point at the
// same cache id the first in filename order wins; a cache id already
// embeds its title key (see cacheID), so any of them names the same title.
func (s *Service) indexKeysByID(want map[string]bool) map[string]string {
	out := map[string]string{}
	if s.dir == "" || len(want) == 0 {
		return out
	}
	dir, err := os.Open(filepath.Join(s.dir, "index"))
	if err != nil {
		return out
	}
	defer dir.Close()
	for len(out) < len(want) {
		names, err := dir.Readdirnames(256)
		for _, name := range names {
			if len(out) >= len(want) {
				break
			}
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			key, _, ok := strings.Cut(name, ".")
			if !ok {
				continue
			}
			if _, err := strconv.ParseInt(key, 10, 64); err != nil {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(s.dir, "index", name))
			if err != nil || len(raw) > 64<<10 {
				continue
			}
			var sel selection
			if json.Unmarshal(raw, &sel) != nil || sel.ID == "" {
				continue
			}
			if !want[sel.ID] {
				continue
			}
			if _, dup := out[sel.ID]; !dup {
				out[sel.ID] = key
			}
		}
		if err != nil || len(names) == 0 {
			break
		}
	}
	return out
}
