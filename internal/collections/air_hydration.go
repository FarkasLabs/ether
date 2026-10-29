package collections

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// Each evaluation performs at most this many show-detail requests. Cached
// results are shared by previews and builds; oldest entries are refreshed
// first, so repeated builds make progress through larger catalogues.
const airFetchBudget = 100
const airFreshness = 24 * time.Hour

type airRecord struct {
	episode, premiere, fetched, attempted int64
	failed                                bool
}

func (s *Service) hydrateAirDates(ctx context.Context, cs []*cand, mode string, now time.Time, online bool) (int, int, error) {
	s.airMu.Lock()
	defer s.airMu.Unlock()
	if err := s.loadAirDates(ctx, cs, mode, now); err != nil {
		return 0, 0, err
	}
	records := map[core.RatingKey]airRecord{}
	keys := make([]any, 0, len(cs))
	for _, c := range cs {
		if c.e.typ == core.TypeShow {
			keys = append(keys, c.e.key)
		}
	}
	for start := 0; start < len(keys); start += 300 {
		chunk := keys[start:min(start+300, len(keys))]
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		rows, err := s.db.QueryContext(ctx, `SELECT show_key,episode_ts,premiere_ts,fetched_at,attempted_at,failed FROM collections_air_dates WHERE show_key IN (`+placeholders+`)`, chunk...)
		if err != nil {
			return 0, 0, err
		}
		for rows.Next() {
			var k core.RatingKey
			var r airRecord
			if err = rows.Scan(&k, &r.episode, &r.premiere, &r.fetched, &r.attempted, &r.failed); err != nil {
				rows.Close()
				return 0, 0, err
			}
			records[k] = r
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return 0, 0, err
		}
		rows.Close()
	}

	var pending []*cand
	if online && s.api != nil && s.api.c != nil {
		for _, c := range cs {
			r := records[c.e.key]
			retry := int64(airFreshness.Seconds())
			if r.failed {
				retry = int64(time.Hour.Seconds())
			}
			if c.e.typ == core.TypeShow && (r.attempted == 0 || now.Unix()-r.attempted >= retry) {
				pending = append(pending, c)
			}
		}
	}
	sort.SliceStable(pending, func(i, j int) bool { return records[pending[i].e.key].attempted < records[pending[j].e.key].attempted })
	if len(pending) > airFetchBudget {
		pending = pending[:airFetchBudget]
	}
	type result struct {
		key core.RatingKey
		r   airRecord
		err error
	}
	results := make(chan result, len(pending))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, c := range pending {
		wg.Add(1)
		go func(k core.RatingKey) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			r, err := s.fetchAirRecord(ctx, k, now)
			results <- result{k, r, err}
		}(c.e.key)
	}
	wg.Wait()
	close(results)
	for x := range results {
		if x.err != nil {
			x.r = records[x.key]
			x.r.attempted = now.Unix()
			x.r.failed = true
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO collections_air_dates(show_key,episode_ts,premiere_ts,fetched_at,attempted_at,failed) VALUES(?,?,?,?,?,?) ON CONFLICT(show_key) DO UPDATE SET episode_ts=excluded.episode_ts,premiere_ts=excluded.premiere_ts,fetched_at=excluded.fetched_at,attempted_at=excluded.attempted_at,failed=excluded.failed`, x.key, x.r.episode, x.r.premiere, x.r.fetched, x.r.attempted, x.r.failed); err != nil {
			return 0, 0, err
		}
		records[x.key] = x.r
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	unknown, stale := 0, 0
	for _, c := range cs {
		if c.e.typ != core.TypeShow {
			continue
		}
		r := records[c.e.key]
		d := r.episode
		if mode == "latestSeasonPremiere" {
			d = r.premiere
		}
		// A successful show-details response describes the current show;
		// a partial mirror cannot establish a more recent season or episode.
		if r.fetched != 0 {
			c.airDate = d
		}
		if c.airDate == 0 {
			unknown++
		} else if r.fetched != 0 && (r.failed || now.Unix()-r.fetched >= int64(airFreshness.Seconds())) {
			stale++
		}
	}
	return unknown, stale, nil
}

func (s *Service) fetchAirRecord(ctx context.Context, k core.RatingKey, now time.Time) (airRecord, error) {
	var d tmdb.TVDetails
	if err := s.api.get(ctx, fmt.Sprintf("/tv/%d", k.TMDB()), nil, &d); err != nil {
		return airRecord{}, err
	}
	cutoff := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day()+1, 0, 0, 0, 0, time.UTC)
	aired := func(date string) int64 {
		t := tmdb.ParseDate(date)
		if t.IsZero() || !t.Before(cutoff) {
			return 0
		}
		return t.Unix()
	}
	r := airRecord{fetched: now.Unix(), attempted: now.Unix()}
	if ep := d.LastEpisodeToAir; ep != nil && ep.SeasonNumber > 0 && ep.EpisodeNumber > 0 {
		r.episode = aired(ep.AirDate)
	}
	latest := 0
	for _, season := range d.Seasons {
		if season.SeasonNumber > latest && season.EpisodeCount > 0 {
			if date := aired(season.AirDate); date != 0 {
				latest = season.SeasonNumber
				r.premiere = date
			}
		}
	}
	// TMDB's last episode can be a special. Read the most recent aired
	// regular season in that case, rather than using the special's date.
	// This costs at most one additional request per selected show.
	if d.LastEpisodeToAir != nil && d.LastEpisodeToAir.SeasonNumber == 0 && latest > 0 {
		var season tmdb.SeasonDetails
		if err := s.api.get(ctx, fmt.Sprintf("/tv/%d/season/%d", k.TMDB(), latest), nil, &season); err != nil {
			return airRecord{}, err
		}
		for _, ep := range season.Episodes {
			if ep.EpisodeNumber > 0 {
				r.episode = max(r.episode, aired(ep.AirDate))
			}
		}
	}
	// A later known aired season without a dated summary must remain unknown,
	// rather than borrowing the premiere of an older season.
	if ep := d.LastEpisodeToAir; ep != nil && ep.SeasonNumber > latest && ep.EpisodeNumber > 0 && aired(ep.AirDate) > 0 {
		r.premiere = 0
	}
	return r, nil
}
