package config

import (
	"os"
	"strings"
	"testing"
)

func TestPostersValidationAndSealing(t *testing.T) {
	for _, template := range []string{"file:///tmp/{imdb_id}", "https://{imdb_id}.example.com/poster", "https://user:pass@example.com/{imdb_id}", "http://lan/{tmdb_id}", "http://lan/{unknown}"} {
		if (Posters{Enabled: true, Source: "custom", URLTemplate: template}).Validate() == nil {
			t.Fatal("accepted", template)
		}
	}
	cfg := Posters{Enabled: true, Source: "custom", URLTemplate: "http://lan/{type}/{tmdb_id}?token=template-secret", APIKey: "poster-secret"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Update(func(c *Config) error { c.Posters = cfg; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "template-secret") || strings.Contains(string(raw), "poster-secret") {
		t.Fatal("plaintext poster credential")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Get().Posters != cfg {
		t.Fatal("poster settings did not roundtrip")
	}
}
