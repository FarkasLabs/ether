package collections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// AniList public anime lists. AniList ids are not TMDB ids, so entries are
// mapped through a community anime id mapping (AniList -> TMDB/TVDB/IMDb);
// entries the mapping does not know fall back to an exact TMDB title search.
// AniList splits a show into one entry per season; the mapping folds them
// into the one TMDB show.

var anilistPath = regexp.MustCompile(`^/user/([^/]+)/(animelist|mangalist)(?:/([^/]+))?/?$`)

// parseAniListURL returns the user and (optional) list name of an AniList
// anime list URL.
func parseAniListURL(raw string) (user, list string, err error) {
	u, err := parseListURL(raw, "anilist.co")
	if err != nil {
		return "", "", err
	}
	m := anilistPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", fmt.Errorf("use a list URL like https://anilist.co/user/<user>/animelist")
	}
	if m[2] == "mangalist" {
		return "", "", fmt.Errorf("use an anime list; manga lists have nothing to play")
	}
	return m[1], strings.TrimSpace(m[3]), nil
}

const anilistQuery = `query ($user: String, $chunk: Int) {
  MediaListCollection(userName: $user, type: ANIME, chunk: $chunk, perChunk: 500) {
    hasNextChunk
    lists { name entries { media { id format seasonYear startDate { year } title { english romaji } } } }
  }
}`

func (s *Service) fetchAniList(ctx context.Context, rawURL string) ([]extRef, error) {
	user, want, err := parseAniListURL(rawURL)
	if err != nil {
		return nil, err
	}
	type media struct {
		ID         int64  `json:"id"`
		Format     string `json:"format"`
		SeasonYear int    `json:"seasonYear"`
		StartDate  struct {
			Year int `json:"year"`
		} `json:"startDate"`
		Title struct {
			English string `json:"english"`
			Romaji  string `json:"romaji"`
		} `json:"title"`
	}
	var entries []media
	seen := map[int64]bool{}
	names := map[string]bool{}
	matched := false
	hdr := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	for chunk := 1; chunk <= maxExternalPages; chunk++ {
		q, _ := json.Marshal(map[string]any{"query": anilistQuery, "variables": map[string]any{"user": user, "chunk": chunk}})
		body, _, err := s.web.post(ctx, s.bases.AniListAPI, q, hdr)
		if err != nil {
			if chunk > 1 {
				break
			}
			if errors.Is(err, errHTTPNotFound) {
				return nil, fmt.Errorf("anilist: user %q was not found or their lists are private", user)
			}
			return nil, fmt.Errorf("anilist: %w", err)
		}
		var res struct {
			Data struct {
				Collection *struct {
					HasNextChunk bool `json:"hasNextChunk"`
					Lists        []struct {
						Name    string `json:"name"`
						Entries []struct {
							Media *media `json:"media"`
						} `json:"entries"`
					} `json:"lists"`
				} `json:"MediaListCollection"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			return nil, fmt.Errorf("anilist: unexpected response: %w", err)
		}
		c := res.Data.Collection
		if c == nil {
			if len(res.Errors) > 0 {
				return nil, fmt.Errorf("anilist: %s", res.Errors[0].Message)
			}
			return nil, fmt.Errorf("anilist: unexpected response")
		}
		for _, l := range c.Lists {
			names[l.Name] = true
			if want != "" && !strings.EqualFold(l.Name, want) {
				continue
			}
			matched = true
			for _, e := range l.Entries {
				if e.Media != nil && e.Media.ID > 0 && !seen[e.Media.ID] {
					seen[e.Media.ID] = true
					entries = append(entries, *e.Media)
				}
			}
		}
		if !c.HasNextChunk {
			break
		}
	}
	if want != "" && !matched {
		have := make([]string, 0, len(names))
		for n := range names {
			have = append(have, n)
		}
		sort.Strings(have)
		return nil, fmt.Errorf("anilist: %s has no list named %q (lists: %s)", user, want, strings.Join(have, ", "))
	}
	ids, err := s.anime.load(ctx, s)
	if err != nil {
		s.log.Warn("collections: anime id mapping unavailable; matching AniList titles by name", "err", err)
	}
	out := make([]extRef, 0, len(entries))
	for _, m := range entries {
		r := extRef{Title: firstNonEmpty(m.Title.English, m.Title.Romaji), Year: m.SeasonYear}
		if r.Year == 0 {
			r.Year = m.StartDate.Year
		}
		switch m.Format {
		case "MOVIE":
			r.Type = "movie"
		case "TV", "TV_SHORT":
			r.Type = "show"
		}
		if id, ok := ids[m.ID]; ok {
			switch {
			case id.movie > 0 && (r.Type == "movie" || id.show == 0):
				r.TMDB, r.Type = id.movie, "movie"
			case id.show > 0:
				r.TMDB, r.Type = id.show, "show"
			}
			r.IMDB, r.TVDB = id.imdb, id.tvdb
		}
		out = append(out, r)
	}
	return out, nil
}

// animeIDs are the other databases' ids for one AniList entry.
type animeIDs struct {
	show, movie, tvdb int64
	imdb              string
}

// animeIDCache holds the AniList id mapping, refreshed daily and fetched
// only once some collection uses an AniList list.
type animeIDCache struct {
	mu     sync.Mutex
	ids    map[int64]animeIDs
	loaded time.Time
	tried  time.Time
}

const (
	animeIDsTTL   = 24 * time.Hour
	animeIDsRetry = 10 * time.Minute
)

// load returns the mapping, refreshing it when stale. A failed refresh
// keeps serving the previous copy and is not retried for a while.
func (c *animeIDCache) load(ctx context.Context, s *Service) (map[int64]animeIDs, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if c.ids != nil && now.Sub(c.loaded) < animeIDsTTL {
		return c.ids, nil
	}
	if !c.tried.IsZero() && now.Sub(c.tried) < animeIDsRetry {
		return c.ids, nil
	}
	c.tried = now
	ids, err := fetchAnimeIDs(ctx, s.web, s.bases.AnimeIDs)
	if err != nil {
		return c.ids, err
	}
	c.ids, c.loaded = ids, now
	return ids, nil
}

// flexIDs reads an id field that is null, a number or a list of numbers.
type flexIDs []int64

func (f *flexIDs) UnmarshalJSON(b []byte) error {
	var one int64
	if json.Unmarshal(b, &one) == nil {
		if one > 0 {
			*f = flexIDs{one}
		}
		return nil
	}
	var many []int64
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*f = many
	return nil
}

// flexStrings reads a field that is null, a string or a list of strings.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		if one != "" {
			*f = flexStrings{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*f = many
	return nil
}

func (f flexIDs) first() int64 {
	if len(f) > 0 {
		return f[0]
	}
	return 0
}

// fetchAnimeIDs downloads the mapping document (a JSON array of entries
// carrying anilist_id, themoviedb_id {tv|movie}, tvdb_id and imdb_id) and
// keeps only the AniList entries. The document is several megabytes, so
// it bypasses the page cache and is decoded entry by entry.
func fetchAnimeIDs(ctx context.Context, w *webClient, src string) (map[int64]animeIDs, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", w.ua)
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anime id mapping: %s", resp.Status)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<20))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil, fmt.Errorf("anime id mapping: expected a JSON array")
	}
	ids := map[int64]animeIDs{}
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("anime id mapping: %w", err)
		}
		var e struct {
			AniList flexIDs `json:"anilist_id"`
			TMDB    *struct {
				TV    flexIDs `json:"tv"`
				Movie flexIDs `json:"movie"`
			} `json:"themoviedb_id"`
			TVDB flexIDs     `json:"tvdb_id"`
			IMDB flexStrings `json:"imdb_id"`
		}
		if json.Unmarshal(raw, &e) != nil || e.AniList.first() <= 0 {
			continue
		}
		id := animeIDs{tvdb: e.TVDB.first()}
		if e.TMDB != nil {
			id.show, id.movie = e.TMDB.TV.first(), e.TMDB.Movie.first()
		}
		if len(e.IMDB) > 0 {
			id.imdb = e.IMDB[0]
		}
		if id != (animeIDs{}) {
			ids[e.AniList.first()] = id
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("anime id mapping: no AniList entries")
	}
	return ids, nil
}
