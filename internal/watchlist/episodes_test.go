package watchlist

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

type showCatalog struct {
	fakeCatalog
	episodes []core.Title
}

func (c *showCatalog) Grandchildren(context.Context, core.RatingKey) ([]core.Title, error) {
	return append([]core.Title(nil), c.episodes...), nil
}

type episodeResolver struct {
	core.Resolver
	mu      sync.Mutex
	missing map[core.RatingKey]bool
	asked   []core.RatingKey
}

func (r *episodeResolver) Resolve(_ context.Context, q core.ResolveRequest) (*core.Resolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, q.Title.Key)
	if r.missing[q.Title.Key] {
		return nil, core.ErrNotAvailable
	}
	return &core.Resolution{Title: q.Title.Key, Release: core.Release{ID: "rel-" + q.Title.Key.String()}, NZB: []byte("nzb")}, nil
}

func (r *episodeResolver) take() []core.RatingKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.asked
	r.asked = nil
	return out
}

type recordingNotifier struct {
	mu     sync.Mutex
	titles []string
}

func (n *recordingNotifier) TitleAvailable(_ context.Context, title string) error {
	n.mu.Lock()
	n.titles = append(n.titles, title)
	n.mu.Unlock()
	return nil
}

func TestWatchlistedShowAnnouncesEachNewEpisodeOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ether.db")
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	const show = 1399
	ep := func(season, episode int, aired time.Duration, title string) core.Title {
		return core.Title{Key: core.EpisodeKey(show, season, episode), Type: core.TypeEpisode, Title: title, ShowTitle: "The Show", Season: season, Episode: episode, Released: now.Add(aired)}
	}
	showTitle := core.Title{Key: core.ShowKey(show), Type: core.TypeShow, Title: "The Show", ContentRating: "TV-14", Released: now.Add(-60 * 24 * time.Hour)}
	cat := &showCatalog{fakeCatalog: fakeCatalog{titles: map[core.RatingKey]core.Title{showTitle.Key: showTitle}}, episodes: []core.Title{
		ep(1, 1, -30*24*time.Hour, "Pilot"),
		ep(1, 2, -7*24*time.Hour, "Second"),
		ep(0, 1, -24*time.Hour, "Behind the scenes"), // special: never "new"
		ep(1, 3, 3*24*time.Hour, "Third"),            // not aired yet
	}}
	res := &episodeResolver{missing: map[core.RatingKey]bool{}}
	note := &recordingNotifier{}
	eng := &fakeEngine{}
	open := func() *Service {
		db, err := store.OpenPath(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return New(db, Options{Catalog: cat, Resolver: res, Engine: eng, Notifier: note, Now: clock})
	}
	s := open()
	if err := s.SaveSettings(ctx, Settings{Enabled: true, Notify: true, Prewarm: true, CheckHours: 6}); err != nil {
		t.Fatal(err)
	}
	alice, bob := &core.User{ID: 1, MaxContentRating: "TV-MA"}, &core.User{ID: 2, MaxContentRating: "TV-MA"}
	for _, u := range []*core.User{alice, bob} {
		if err := s.Remind(ctx, u, showTitle.Key); err != nil {
			t.Fatal(err)
		}
	}
	check := func(svc *Service, users ...*core.User) {
		t.Helper()
		for _, u := range users {
			if err := svc.Check(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The show becomes available through its pilot; episodes so far are
	// not announced individually.
	check(s, alice, bob)
	if got := res.take(); len(got) != 2 || got[0] != core.EpisodeKey(show, 1, 1) {
		t.Fatalf("resolved %v, want the pilot for each viewer", got)
	}
	if len(note.titles) != 2 || note.titles[0] != "The Show" {
		t.Fatalf("alerts %q", note.titles)
	}

	// Later checks re-validate as before and announce nothing new.
	now = now.Add(7 * time.Hour)
	check(s, alice, bob)
	if len(note.titles) != 2 {
		t.Fatalf("alerts %q", note.titles)
	}
	res.take()

	// Episode 3 airs; no release is out yet.
	third := core.EpisodeKey(show, 1, 3)
	res.missing[third] = true
	now = now.Add(3 * 24 * time.Hour)
	check(s, alice)
	if got := res.take(); len(got) != 1 || got[0] != third {
		t.Fatalf("resolved %v, want only the new episode", got)
	}
	// Recently aired: checked again within the hour, not every 6 hours.
	now = now.Add(30 * time.Minute)
	check(s, alice)
	if got := res.take(); len(got) != 0 {
		t.Fatalf("re-checked too soon: %v", got)
	}
	now = now.Add(31 * time.Minute)
	delete(res.missing, third)
	check(s, alice)
	if got := res.take(); len(got) != 1 || got[0] != third {
		t.Fatalf("resolved %v", got)
	}
	if len(note.titles) != 3 || note.titles[2] != "The Show S01E03 · Third" {
		t.Fatalf("alerts %q", note.titles)
	}
	if eng.calls != 3 {
		t.Fatalf("prewarmed %d times, want pilot x2 + new episode", eng.calls)
	}

	// The other viewer finds it too, and after a restart nothing repeats.
	check(s, bob)
	s2 := open()
	now = now.Add(7 * time.Hour)
	check(s2, alice, bob)
	if len(note.titles) != 3 {
		t.Fatalf("duplicate alerts %q", note.titles)
	}
	for _, title := range note.titles {
		if strings.Contains(title, "S00") {
			t.Fatalf("special announced: %q", title)
		}
	}

	// A viewer who may not see the show is never asked about it.
	res.take()
	kid := &core.User{ID: 3, MaxContentRating: "TV-Y"}
	if err := s2.Remind(ctx, kid, showTitle.Key); err == nil {
		t.Fatal("restricted viewer could add the show")
	}
	check(s2, kid)
	if got := res.take(); len(got) != 0 {
		t.Fatalf("resolved %v for a restricted viewer", got)
	}
}
