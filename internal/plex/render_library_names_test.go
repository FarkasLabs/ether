package plex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

// ETHE-26: retained PMS rows exercise bounded naming inputs without capturing
// access policy, viewer state or mutable configuration. Numeric PMS keys here
// are opaque fixture identities, not verified TMDB mappings.
func retainedLibraryRows(t *testing.T) []core.Hub {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../testdata/pms/web", "web_json_hubs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recording struct {
		Body []byte `json:"body"`
	}
	if err := json.Unmarshal(b, &recording); err != nil {
		t.Fatal(err)
	}
	var response struct {
		MediaContainer struct {
			Hub []struct {
				ID       string         `json:"hubIdentifier"`
				Title    string         `json:"title"`
				Promoted bool           `json:"promoted"`
				Type     core.MediaType `json:"type"`
				Metadata []struct {
					Key           string         `json:"ratingKey"`
					Title         string         `json:"title"`
					Type          core.MediaType `json:"type"`
					Year          int            `json:"year"`
					Summary       string         `json:"summary"`
					ContentRating string         `json:"contentRating"`
				} `json:"Metadata"`
			} `json:"Hub"`
		}
	}
	if err := json.Unmarshal(recording.Body, &response); err != nil {
		t.Fatal(err)
	}
	var rows []core.Hub
	for _, h := range response.MediaContainer.Hub {
		row := core.Hub{ID: h.ID, Title: h.Title, Promoted: h.Promoted, Type: h.Type}
		for _, m := range h.Metadata {
			if m.Type != core.TypeMovie && m.Type != core.TypeShow {
				continue
			}
			// Preserve recorded opaque identities, not invented provider mappings.
			id, err := strconv.ParseInt(m.Key, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			key := core.MovieKey(id)
			if m.Type == core.TypeShow {
				key = core.ShowKey(id)
			}
			row.Items = append(row.Items, core.Title{Key: key, Type: m.Type, Title: m.Title, Year: m.Year, Summary: m.Summary, ContentRating: m.ContentRating})
		}
		if len(row.Items) > 0 {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		t.Fatal("recording has no movie/show rows")
	}
	return rows
}

// ETHE-26 requires a bounded copy count, whitespace/default naming, stable
// identifiers, and next-render updates, while Store.Get remains isolated.
func TestRenderLibraryNamesBoundedAndIsolated(t *testing.T) {
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	update := func(movie, show string) {
		t.Helper()
		if err := store.Update(func(c *config.Config) error {
			c.Server.MoviesLibraryName = movie
			c.Server.TVShowsLibraryName = show
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	update("  Ether Films  ", " \t ")
	copies := 0
	f := newFixture(func(d *Deps) { d.Config = func() config.Config { copies++; return store.Get() } })
	defer f.srv.Close()
	copies = 0
	ctx := f.srv.withRenderLibraryNames(context.Background())
	names := f.srv.renderLibraryNames(ctx)
	original := f.srv.withLibraryTitles(f.srv.sections, names)
	for _, row := range retainedLibraryRows(t) {
		for _, user := range []*core.User{&ownerUser, {ID: 3, AllowedSections: []int{1}}} {
			visible := f.srv.visibleTitles(user, row.Items, names)
			for _, title := range visible {
				sec, ok := f.srv.sectionFor(title.Type, names)
				if !ok || !f.srv.canSeeSection(user, sec.ID, names) {
					t.Fatal("invalid visible section")
				}
				if sec.Type == core.TypeShow && user.ID == 3 {
					t.Fatal("restricted show leaked")
				}
			}
		}
	}
	if copies != 1 {
		t.Fatalf("library-name copies=%d, want one including setup", copies)
	}
	if original[0].Title != "Ether Films" || original[1].Title != "TV Shows" {
		t.Fatalf("trim/default: %+v", original)
	}
	update("Other Films", "Other Series")
	detached := store.Get()
	detached.Server.MoviesLibraryName = "not saved"
	again := f.srv.withLibraryTitles(f.srv.sections, f.srv.renderLibraryNames(ctx))
	if again[0] != original[0] || again[1] != original[1] {
		t.Fatal("old render changed after rename")
	}
	again[0].Title = "mutated output"
	next := f.srv.withLibraryTitles(f.srv.sections, f.srv.renderLibraryNames(f.srv.withRenderLibraryNames(context.Background())))
	if next[0].Title != "Other Films" || next[1].Title != "Other Series" {
		t.Fatalf("next render: %+v", next)
	}
	for i := range next {
		if next[i].ID != original[i].ID || next[i].UUID != original[i].UUID {
			t.Fatal("rename changed section identity")
		}
	}
	if store.Get().Server.MoviesLibraryName != "Other Films" {
		t.Fatal("Get mutation escaped")
	}
	// Permissions are evaluated now, not captured with names.
	user := core.User{ID: 3, AllowedSections: []int{1}}
	if f.srv.canSeeSection(&user, 2, names) {
		t.Fatal("show initially allowed")
	}
	user.AllowedSections = []int{2}
	if !f.srv.canSeeSection(&user, 2, names) || f.srv.canSeeSection(&user, 1, names) {
		t.Fatal("permission update hidden by snapshot")
	}
}

// ETHE-26: concurrent renames/viewers must not mix settings generations or
// share restrictions. Only Store's real copy/update boundary is synchronized.
func TestRenderLibraryNamesConcurrentViewersAndRenames(t *testing.T) {
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set := func(movie, show string) error {
		return store.Update(func(c *config.Config) error {
			c.Server.MoviesLibraryName = movie
			c.Server.TVShowsLibraryName = show
			return nil
		})
	}
	if err := set("A movies", "A shows"); err != nil {
		t.Fatal(err)
	}
	f := newFixture(func(d *Deps) { d.Config = store.Get })
	defer f.srv.Close()
	rows := retainedLibraryRows(t)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := set("B movies", "B shows"); err != nil {
				t.Error(err)
				return
			}
			if err := set("A movies", "A shows"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for _, id := range []int{1, 2} {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				ctx := f.srv.withRenderLibraryNames(context.Background())
				n := f.srv.renderLibraryNames(ctx)
				if strings.Fields(n.movies)[0] != strings.Fields(n.shows)[0] {
					t.Error("mixed rename generations")
					return
				}
				user := &core.User{ID: int64(id), AllowedSections: []int{id}}
				for _, row := range rows {
					for _, title := range f.srv.visibleTitles(user, row.Items, n) {
						sec, ok := f.srv.sectionFor(title.Type, n)
						if !ok || sec.ID != id {
							t.Error("viewer restriction leaked")
							return
						}
					}
				}
			}
		}(id)
	}
	wg.Wait()
}

// ETHE-26: actual row rendering must propagate the supplied immutable names
// through filtering and metadata, retain cancellation, and load watch state for
// each serve instead of retaining a rendered response.
func TestRenderLibraryNamesRowRendering(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	rows := retainedLibraryRows(t)
	for _, row := range rows {
		for _, it := range row.Items {
			f.catalog.titles[it.Key] = it
		}
	}
	ctx := context.WithValue(context.Background(), libraryNamesKey{}, libraryNames{"Captured films", "Captured series"})
	pr := &Request{User: &ownerUser, Language: "en", Format: model.JSON}
	rendered := f.srv.renderHubs(ctx, pr, rows, hubCtx{})
	for i, row := range rendered {
		separate := f.srv.renderHub(ctx, pr, hubSpec{Hub: rows[i]}, hubCtx{})
		if !reflect.DeepEqual(row, separate) {
			t.Fatal("batch rendering changed row output")
		}
		for _, md := range row.Metadata {
			want := "Captured films"
			if md.Type == "show" {
				want = "Captured series"
			}
			if md.LibrarySectionTitle != want {
				t.Fatalf("render lost immutable names: %+v", md)
			}
		}
	}
	// Re-serving the same names must still read current watch state.
	if len(rendered) == 0 || len(rendered[0].Metadata) == 0 {
		t.Fatal("empty retained render")
	}
	key := rows[0].Items[0].Key
	f.state.set(ownerUser.ID, ws(key, 60000, 1))
	updated := f.srv.renderHubs(ctx, pr, rows, hubCtx{})
	if reflect.DeepEqual(rendered, updated) {
		t.Fatal("watch update hidden between renders")
	}
	pr.User = &core.User{ID: 3, AllowedSections: []int{1}}
	restricted := f.srv.renderHubs(ctx, pr, rows, hubCtx{})
	for _, row := range restricted {
		for _, md := range row.Metadata {
			if md.Type == "show" || md.ViewCount != 0 || md.ViewOffset != 0 {
				t.Fatal("viewer state or section leaked")
			}
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	seen := false
	f.srv.d.FilterAvailable = func(c context.Context, items []core.Title) []core.Title {
		if c.Err() != context.Canceled {
			t.Error("render dropped cancellation")
		}
		seen = true
		return nil
	}
	f.srv.renderHubs(canceled, pr, rows, hubCtx{})
	if !seen {
		t.Fatal("availability cancellation path not exercised")
	}
}

// ETHE-26: a rename must appear on the next Home/promoted/section serve,
// without dropping per-serve impressions or caching the first viewer's rows.
func TestRenderLibraryNamesHTTPRenames(t *testing.T) {
	rows := retainedLibraryRows(t)
	rec := &contextualRecommender{fakeRecommender: fakeRecommender{home: rows, section: map[core.MediaType][]core.Hub{core.TypeMovie: rows}}}
	names := libraryNames{"First films", "First series"}
	f := newFixture(func(d *Deps) {
		d.Recommender = rec
		base := d.Config
		d.Config = func() config.Config {
			c := base()
			c.Server.MoviesLibraryName = names.movies
			c.Server.TVShowsLibraryName = names.shows
			return c
		}
	})
	defer f.srv.Close()
	for _, row := range rows {
		for _, it := range row.Items {
			f.catalog.titles[it.Key] = it
		}
	}
	for _, route := range []string{"/hubs?excludeContinueWatching=1", "/hubs/promoted?count=2", "/hubs/sections/1?count=12"} {
		for _, format := range []string{"application/json", "text/xml"} {
			names = libraryNames{"First films", "First series"}
			first := f.get(t, route, "Accept", format, "X-Plex-Language", "fr", "X-Plex-Product", "Plex Web")
			names = libraryNames{"Next films", "Next series"}
			next := f.get(t, route, "Accept", format, "X-Plex-Language", "fr", "X-Plex-Product", "Plex Web")
			if first.Code != 200 || next.Code != 200 {
				t.Fatalf("%s: statuses %d/%d", route, first.Code, next.Code)
			}
			if !strings.Contains(first.Body.String(), "First films") || !strings.Contains(next.Body.String(), "Next films") {
				t.Fatalf("%s rename stale", route)
			}
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.impressions) == 0 {
		t.Fatal("no per-serve impressions")
	}
	// Equal requests must produce a separate impression, not a cached response.
	before := len(rec.impressions)
	rec.mu.Unlock()
	f.get(t, "/hubs?excludeContinueWatching=1", "Accept", "application/json")
	f.get(t, "/hubs?excludeContinueWatching=1", "Accept", "application/json")
	rec.mu.Lock()
	if len(rec.impressions) != before+2 {
		t.Fatal("per-serve impressions suppressed")
	}
}
