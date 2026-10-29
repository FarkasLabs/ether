package config

import (
	"os"
	"strings"
	"testing"
)

func TestPostersValidationAndSealing(t *testing.T) {
	for _, p := range []Posters{
		{Enabled: true, Source: "xrdb", BaseURL: "https://user:pass@example.com", Profile: "home"},
		{Enabled: true, Source: "xrdb", BaseURL: "https://example.com?key=secret", Profile: "home"},
	} {
		if p.Validate() == nil {
			t.Fatal("accepted invalid XRDB configuration")
		}
	}
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

func TestXRDBSecretRoundtrip(t *testing.T) {
	p := Posters{Enabled: true, Source: "xrdb", BaseURL: "http://localhost:8000", Profile: "family", APIKey: "xrdb-secret"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(c *Config) error { c.Posters = p; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), p.APIKey) {
		t.Fatal("plaintext credential")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Get().Posters != p {
		t.Fatal("XRDB settings did not roundtrip")
	}
}
