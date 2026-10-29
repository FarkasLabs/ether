package collections_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
)

type airProviderTMDB struct{}

func (airProviderTMDB) GetJSON(_ context.Context, path string, q url.Values, out any) error {
	var data any
	switch {
	case path == "/genre/tv/list":
		data = map[string]any{"genres": []any{map[string]any{"id": 18, "name": "Drama"}, map[string]any{"id": 10764, "name": "Reality"}}}
	case path == "/watch/providers/tv" || path == "/watch/providers/movie":
		data = map[string]any{"results": []any{map[string]any{"provider_id": 8, "provider_name": "Test Stream"}}}
	case path == "/discover/tv":
		data = map[string]any{"page": 1, "total_pages": 1, "total_results": 4, "results": []any{
			map[string]any{"id": 901, "name": "Alpha", "first_air_date": "2020-01-01", "popularity": 90, "vote_count": 500, "genre_ids": []int{18}},
			map[string]any{"id": 902, "name": "Beta", "first_air_date": "2020-01-01", "popularity": 80, "vote_count": 500, "genre_ids": []int{18}},
			map[string]any{"id": 903, "name": "Gamma", "first_air_date": "2020-01-01", "popularity": 70, "vote_count": 500, "genre_ids": []int{18}},
			map[string]any{"id": 904, "name": "Reality", "first_air_date": "2020-01-01", "popularity": 100, "vote_count": 500, "genre_ids": []int{10764}},
		}}
	case strings.HasPrefix(path, "/tv/"):
		details := map[string][2]string{"/tv/901": {"2026-10-27", "2026-01-01"}, "/tv/902": {"2026-10-01", "2026-10-01"}, "/tv/903": {"2026-10-15", "2026-09-01"}, "/tv/904": {"2026-10-28", "2026-10-28"}}
		d, ok := details[path]
		if !ok {
			return fmt.Errorf("unexpected %s", path)
		}
		data = map[string]any{"last_episode_to_air": map[string]any{"season_number": 2, "episode_number": 2, "air_date": d[0]}, "seasons": []any{map[string]any{"season_number": 2, "air_date": d[1], "episode_count": 2}}}
	default:
		return fmt.Errorf("unexpected %s", path)
	}
	b, _ := json.Marshal(data)
	return json.Unmarshal(b, out)
}

func TestProviderAirSortColdMirror(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, withOpts(collections.WithTMDB(airProviderTMDB{}), collections.WithNow(func() time.Time { return now })))
	sp := spec("air", "show", []core.CollectionSource{{Kind: "watchProvider", ID: 8, Region: "US"}}, rl("votes", "gte", 100))
	sp.Exclude = []core.CollectionRule{rl("genre", "in", []string{"Talk", "News", "Reality", "Soap"})}
	sp.Limit = 100
	for _, tc := range []struct {
		sort string
		desc bool
		want []string
	}{
		{"latestEpisode", true, []string{"Alpha", "Gamma", "Beta"}}, {"latestEpisode", false, []string{"Beta", "Gamma", "Alpha"}},
		{"latestSeasonPremiere", true, []string{"Beta", "Gamma", "Alpha"}}, {"latestSeasonPremiere", false, []string{"Alpha", "Gamma", "Beta"}},
	} {
		sp.Sort, sp.SortDesc = tc.sort, tc.desc
		p, err := e.svc.Preview(e.ctx, sp, 60)
		if err != nil {
			t.Fatal(err)
		}
		if got := titlesOf(p); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s desc=%v got %v want %v warnings=%v", tc.sort, tc.desc, got, tc.want, p.Warnings)
		}
	}
}

func TestProviderAirSortPublishedColdMirror(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		sort string
		desc bool
		want []core.RatingKey
	}{
		{"latestEpisode", true, []core.RatingKey{core.ShowKey(901), core.ShowKey(903), core.ShowKey(902)}},
		{"latestEpisode", false, []core.RatingKey{core.ShowKey(902), core.ShowKey(903), core.ShowKey(901)}},
		{"latestSeasonPremiere", true, []core.RatingKey{core.ShowKey(902), core.ShowKey(903), core.ShowKey(901)}},
		{"latestSeasonPremiere", false, []core.RatingKey{core.ShowKey(901), core.ShowKey(903), core.ShowKey(902)}},
	} {
		t.Run(fmt.Sprintf("%s-%v", tc.sort, tc.desc), func(t *testing.T) {
			e := newEnv(t, withOpts(collections.WithTMDB(airProviderTMDB{}), collections.WithNow(func() time.Time { return now })))
			sp := spec("air", "show", []core.CollectionSource{{Kind: "watchProvider", ID: 8, Region: "US"}}, rl("votes", "gte", 100))
			sp.Exclude = []core.CollectionRule{rl("genre", "in", []string{"Reality"})}
			sp.Limit = 100
			sp.Sort = tc.sort
			sp.SortDesc = tc.desc
			saved, err := e.svc.CreateSpec(e.ctx, sp)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := e.db.(*sql.DB).QueryContext(e.ctx, `SELECT key FROM collections_members WHERE spec_id=? ORDER BY pos`, saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []core.RatingKey
			for rows.Next() {
				var k core.RatingKey
				if err := rows.Scan(&k); err != nil {
					t.Fatal(err)
				}
				got = append(got, k)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("published got %v want %v", got, tc.want)
			}
		})
	}
}

type missingAirProvider struct{ airProviderTMDB }

func (m missingAirProvider) GetJSON(ctx context.Context, path string, q url.Values, out any) error {
	if path == "/tv/903" {
		return json.Unmarshal([]byte(`{}`), out)
	}
	return m.airProviderTMDB.GetJSON(ctx, path, q, out)
}
func TestProviderAirSortReportsMissingDates(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, withOpts(collections.WithTMDB(missingAirProvider{}), collections.WithNow(func() time.Time { return now })))
	sp := spec("air", "show", []core.CollectionSource{{Kind: "watchProvider", ID: 8, Region: "US"}}, rl("votes", "gte", 100))
	sp.Exclude = []core.CollectionRule{rl("genre", "in", []string{"Reality"})}
	sp.Sort = "latestEpisode"
	sp.SortDesc = false
	sp.Limit = 100
	p, err := e.svc.Preview(e.ctx, sp, 60)
	if err != nil {
		t.Fatal(err)
	}
	if got := titlesOf(p); !reflect.DeepEqual(got, []string{"Beta", "Alpha", "Gamma"}) {
		t.Fatalf("unknown dates should be last: %v", got)
	}
	if len(p.Warnings) == 0 || !strings.Contains(strings.Join(p.Warnings, " "), "unavailable for 1") {
		t.Fatalf("missing dates warning: %v", p.Warnings)
	}
}
