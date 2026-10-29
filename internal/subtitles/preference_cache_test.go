package subtitles

import (
	"context"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"strings"
	"testing"
)

func TestReviewerNormalForcedNormal(t *testing.T) {
	f := newForcedFake(t, []forcedCand{{id: 21, downloads: 9000}, {id: 23, downloads: 100, foreign: true}})
	s := New(Options{Config: config.OpenSubtitles{Enabled: true, APIKey: "k"}, CacheDir: t.TempDir(), BaseURL: f.srv.URL})
	u := &core.User{SubtitleLanguages: []string{"en"}}
	title := forcedTitle(500)
	for i, p := range []Preferences{{Mode: "always", Style: "normal"}, {Mode: "forced-only"}, {Mode: "always", Style: "normal"}} {
		out, e := s.SidecarsForRelease(context.Background(), title, u, nil, "release", p)
		if e != nil || len(out) != 1 {
			t.Fatalf("step %d: %+v %v", i, out, e)
		}
		want := "track-21"
		if i == 1 {
			want = "track-23"
		}
		if body := openText(t, s, out[0]); !strings.Contains(body, want) {
			t.Errorf("step %d got %q forced=%v want %s", i, body, out[0].Forced, want)
		}
	}
}
func TestReviewerEmbeddedNormalForcedRequest(t *testing.T) {
	f := newForcedFake(t, []forcedCand{{id: 23, downloads: 100, foreign: true}})
	s := New(Options{Config: config.OpenSubtitles{Enabled: true, APIKey: "k"}, CacheDir: t.TempDir(), BaseURL: f.srv.URL})
	out, e := s.SidecarsForRelease(context.Background(), forcedTitle(501), &core.User{SubtitleLanguages: []string{"en"}}, []core.StreamInfo{{Type: "subtitle", Index: 1, Language: "en"}}, "release", Preferences{Mode: "forced-only"})
	if e != nil || len(out) != 1 {
		t.Fatalf("forced candidate available but normal embedded suppresses fetch: %+v %v", out, e)
	}
}

// Selections persist independently across service restarts and cache-only detail
// reads. Viewers sharing a preference may reuse the same bytes safely.
func TestPreferenceCacheRoundTripAndRestart(t *testing.T) {
	for _, special := range []Preferences{{Mode: "forced-only"}, {Mode: "always", Style: "sdh"}} {
		t.Run(prefSig(special), func(t *testing.T) {
			f := newForcedFake(t, []forcedCand{{id: 21, downloads: 9000}, {id: 23, downloads: 100, foreign: true}, {id: 24, downloads: 50, hi: true}})
			opts := Options{Config: config.OpenSubtitles{Enabled: true, APIKey: "k"}, CacheDir: t.TempDir(), BaseURL: f.srv.URL}
			u := &core.User{SubtitleLanguages: []string{"en"}}
			title := forcedTitle(502)
			normal := Preferences{Mode: "always", Style: "normal"}
			for _, p := range []Preferences{normal, special, normal, special} {
				s := New(opts)
				out, e := s.SidecarsForRelease(context.Background(), title, u, nil, "release", p)
				if e != nil || len(out) != 1 {
					t.Fatalf("%+v: %+v %v", p, out, e)
				}
				want := "track-21"
				if p == special {
					want = "track-23"
					if p.Style == "sdh" {
						want = "track-24"
					}
				}
				if got := openText(t, s, out[0]); !strings.Contains(got, want) {
					t.Fatalf("%+v got %q want %s", p, got, want)
				}
				cached := s.CachedSidecars(title, u, nil, p)
				if len(cached) != 1 || cached[0].ExternalURL != out[0].ExternalURL {
					t.Fatalf("cache-only %+v versus %+v", cached, out)
				}
			}
			if got := f.downloads(); len(got) != 2 {
				t.Fatalf("repeated preference downloaded again: %v", got)
			}
		})
	}
}
func TestPreferenceEmbeddedCoverage(t *testing.T) {
	u := &core.User{SubtitleLanguages: []string{"en"}}
	for _, tt := range []struct {
		p       Preferences
		st      core.StreamInfo
		missing bool
	}{
		{Preferences{Mode: "forced-only"}, core.StreamInfo{Language: "en"}, true},
		{Preferences{Mode: "forced-only"}, core.StreamInfo{Language: "en", Forced: true}, false},
		{Preferences{Mode: "forced-only", Style: "sdh"}, core.StreamInfo{Language: "en", Forced: true}, false},
		{Preferences{Style: "sdh"}, core.StreamInfo{Language: "en"}, true},
		{Preferences{Style: "sdh"}, core.StreamInfo{Language: "en", Title: "English (SDH)"}, false},
		{Preferences{Style: "normal"}, core.StreamInfo{Language: "en", Forced: true}, true},
		{Preferences{Style: "normal"}, core.StreamInfo{Language: "en"}, false},
	} {
		tt.st.Type = "subtitle"
		if got := len(missingForPreferences(u, []core.StreamInfo{tt.st}, tt.p)) > 0; got != tt.missing {
			t.Errorf("%+v %+v missing=%v", tt.p, tt.st, got)
		}
	}
}

func TestConcurrentViewerPreferences(t *testing.T) {
	f := newForcedFake(t, []forcedCand{{id: 21, downloads: 9000}, {id: 23, downloads: 100, foreign: true}})
	s := New(Options{Config: config.OpenSubtitles{Enabled: true, APIKey: "k"}, CacheDir: t.TempDir(), BaseURL: f.srv.URL})
	type result struct {
		forced bool
		out    []core.StreamInfo
		err    error
	}
	results := make(chan result, 2)
	for i, p := range []Preferences{{Mode: "always", Style: "normal"}, {Mode: "forced-only"}} {
		go func(i int, p Preferences) {
			out, err := s.SidecarsForRelease(context.Background(), forcedTitle(503), &core.User{ID: int64(i + 1), SubtitleLanguages: []string{"en"}}, nil, "release", p)
			results <- result{p.Mode == "forced-only", out, err}
		}(i, p)
	}
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil || len(r.out) != 1 {
			t.Fatalf("%+v", r)
		}
		if r.out[0].Forced != r.forced {
			t.Fatalf("viewer received wrong forced selection: %+v", r)
		}
	}
}
