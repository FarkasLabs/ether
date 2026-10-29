package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/subtitles"
)

// subtitleSyncItem is one row of GET /subtitle-sync/ as the owner UI sees
// it. Title is resolved from the catalog; it is always present in the JSON
// and empty when the title is unknown (no cached key, or the catalog has
// no such title), so the UI can say "Unknown title" explicitly instead of
// showing a raw rating key or cache hash. The raw key itself is never
// sent: it is only the join key between the subtitle cache and the
// catalog, resolved here on the server.
type subtitleSyncItem struct {
	ID        string    `json:"id"`
	Language  string    `json:"language"`
	Release   string    `json:"release"`
	FetchedAt time.Time `json:"fetchedAt"`
	Title     string    `json:"title"`
	ShowTitle string    `json:"showTitle,omitempty"`
	Season    int       `json:"season,omitempty"`
	Episode   int       `json:"episode,omitempty"`
}

// subtitleSyncHandler serves the subtitle-timing list with real titles,
// delegating everything else (the POST sync) to the subtitles service.
//
// Title lookup is bounded to the returned page: CachedSubtitles caps the
// list at 1000 entries, and the catalog is consulted exactly once with the
// distinct rating keys of those entries (the catalog's batch CachedTitles,
// which reads only the local mirror — no TMDB fetch per title). Entries
// without a key, or whose key the catalog does not know, keep Title == "".
func (a *App) subtitleSyncHandler() http.Handler {
	var resolve func(context.Context, []core.RatingKey) ([]core.Title, error)
	if a.catalog != nil {
		resolve = a.catalog.CachedTitles
	}
	return subtitleSyncHandler(a.subtitles.CachedSubtitles, resolve, a.subtitles.SyncHandler())
}

func subtitleSyncHandler(
	list func() ([]subtitles.CachedSubtitle, error),
	resolve func(context.Context, []core.RatingKey) ([]core.Title, error),
	fallback http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || strings.Trim(r.URL.Path, "/") != "" {
			fallback.ServeHTTP(w, r)
			return
		}
		items, err := list()
		if err != nil {
			http.Error(w, "Subtitles unavailable", http.StatusInternalServerError)
			return
		}
		out := make([]subtitleSyncItem, 0, len(items))
		keys := make([]core.RatingKey, 0, len(items))
		seen := map[core.RatingKey]bool{}
		for _, it := range items {
			out = append(out, subtitleSyncItem{
				ID: it.ID, Language: it.Language, Release: it.Release, FetchedAt: it.FetchedAt,
			})
			if it.Key == "" {
				continue
			}
			if n, err := strconv.ParseInt(it.Key, 10, 64); err == nil && !seen[core.RatingKey(n)] {
				seen[core.RatingKey(n)] = true
				keys = append(keys, core.RatingKey(n))
			}
		}
		if resolve != nil && len(keys) > 0 {
			// A catalog failure degrades to "unknown title" for every row;
			// the list itself (releases, languages, download times) is
			// still useful and must not fail with it.
			if titles, err := resolve(r.Context(), keys); err == nil {
				byKey := make(map[core.RatingKey]core.Title, len(titles))
				for _, t := range titles {
					byKey[t.Key] = t
				}
				for i, it := range items {
					n, err := strconv.ParseInt(it.Key, 10, 64)
					if err != nil {
						continue
					}
					if t, ok := byKey[core.RatingKey(n)]; ok {
						out[i].Title = t.Title
						out[i].ShowTitle = t.ShowTitle
						out[i].Season = t.Season
						out[i].Episode = t.Episode
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}
