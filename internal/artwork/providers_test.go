package artwork

import (
	"bytes"
	"context"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProviderIdentity(t *testing.T) {
	for _, kind := range []core.MediaType{core.TypeMovie, core.TypeShow} {
		title := &core.Title{Type: kind, IDs: core.IDs{TMDB: 123, IMDB: "tt1234567"}}
		for _, source := range []string{"postersplus", "xrdb", "rpdb", "betterposters", "custom"} {
			p := config.Posters{Enabled: true, Source: source, BaseURL: "http://lan:8000", APIKey: "secret&?#/", Profile: "profile & test", URLTemplate: "https://example.com/{type}/{tmdb_id}?key={api_key}"}
			if source == "betterposters" {
				p.URLTemplate = ""
			}
			raw, ok := providerURL(p, title)
			if !ok {
				t.Fatal(source)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			switch source {
			case "xrdb":
				want := "/poster/tmdb:movie:123"
				if kind == core.TypeShow {
					want = "/poster/tmdb:series:123"
				}
				if u.Path != want || u.Query().Get("config") != p.Profile || u.Query().Get("key") != p.APIKey {
					t.Fatal(raw)
				}
			case "postersplus":
				want := "movie"
				if kind == core.TypeShow {
					want = "tv"
				}
				if u.Query().Get("type") != want || u.Query().Get("access_key") != p.APIKey {
					t.Fatal(raw)
				}
			case "rpdb":
				want := "movie-123"
				if kind == core.TypeShow {
					want = "series-123"
				}
				if !strings.Contains(raw, want) {
					t.Fatal(raw)
				}
			case "betterposters":
				if !strings.HasSuffix(raw, "/tt1234567.jpg") {
					t.Fatal(raw)
				}
			case "custom":
				if u.Query().Get("key") != p.APIKey {
					t.Fatal(raw)
				}
			}
		}
	}
	for _, kind := range []core.MediaType{core.TypeEpisode, core.TypeSeason} {
		if _, ok := providerURL(config.Posters{Source: "rpdb"}, &core.Title{Type: kind, IDs: core.IDs{TMDB: 123}}); ok {
			t.Fatal("wrong type accepted")
		}
	}
	if _, ok := providerURL(config.Posters{Source: "betterposters"}, &core.Title{Type: core.TypeMovie, IDs: core.IDs{TMDB: 123}}); ok {
		t.Fatal("substituted TMDB for IMDb")
	}
	raw, ok := providerURL(config.Posters{Source: "xrdb", BaseURL: "https://example.com"}, &core.Title{Type: core.TypeMovie, IDs: core.IDs{TMDB: 123}})
	if !ok || strings.Contains(raw, "?") {
		t.Fatal("default profile should omit query", raw)
	}
}

func TestPosterFetchFallbackAndCacheSettings(t *testing.T) {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 400, 600)), nil)
	count := 0
	fail := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.URL.Path != "/poster/tmdb:movie:123" || r.URL.Query().Get("config") != "family" {
			t.Errorf("unexpected XRDB request: %s", r.URL.Path)
		}
		if fail {
			http.Error(w, "secret upstream message", 503)
			return
		}
		w.Write(buf.Bytes())
	}))
	defer upstream.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(buf.Bytes()) }))
	defer fallback.Close()
	p := config.Posters{Enabled: true, Source: "xrdb", BaseURL: upstream.URL, Profile: "family"}
	im := NewImages(ImagesOptions{Dir: t.TempDir(), Posters: func() config.Posters { return p }, ImageURL: func(string, string) string { return fallback.URL }})
	title := &core.Title{Type: core.TypeMovie, IDs: core.IDs{TMDB: 123}}
	get := func() []byte {
		t.Helper()
		data, _, err := im.GetPoster(context.Background(), title, "/fallback.jpg", 200)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	data := get()
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
	if cfg.Width != 200 {
		t.Fatal(cfg)
	}
	get()
	if count != 1 {
		t.Fatal("cache miss", count)
	}
	version := im.PosterVersion()
	p.APIKey = "new-key"
	get()
	if count != 2 || version == im.PosterVersion() {
		t.Fatal("settings reused old cache")
	}
	fail = true
	p.APIKey = "failed-key"
	if !bytes.Equal(get(), buf.Bytes()) {
		t.Fatal("fallback missing")
	}
	get()
	if count != 4 {
		t.Fatal("fallback poisoned provider cache", count)
	}
	p.Enabled = false
	get()
	if count != 4 {
		t.Fatal("disabled provider fetched")
	}
}

func TestProviderRejectsRedirectAndNonRaster(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer target.Close()
	for _, body := range []string{"redirect", "<svg xmlns='http://www.w3.org/2000/svg'></svg>", strings.Repeat("x", 8<<20+1)} {
		src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body == "redirect" {
				http.Redirect(w, r, target.URL, 302)
			} else {
				w.Write([]byte(body))
			}
		}))
		im := NewImages(ImagesOptions{})
		_, _, err := im.fetchPoster(context.Background(), src.URL+"?key=private", 200)
		src.Close()
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe response/error", err)
		}
	}
	if hits != 0 {
		t.Fatal("followed redirect")
	}
}
