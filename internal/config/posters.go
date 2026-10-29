package config

import (
	"errors"
	"net/url"
	"strings"
)

// Posters configures optional owner-supplied artwork. Templates are secrets:
// exported provider URLs can embed account configuration and credentials.
type Posters struct {
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
	BaseURL     string `json:"baseUrl"`
	APIKey      string `json:"apiKey"`
	URLTemplate string `json:"urlTemplate"`
}

func (p Posters) Validate() error {
	if len(p.URLTemplate) > 8192 || len(p.APIKey) > 2048 || len(p.BaseURL) > 2048 {
		return errors.New("poster settings exceed length limit")
	}
	if !p.Enabled {
		return nil
	}
	switch p.Source {
	case "rpdb":
		if p.APIKey == "" {
			return errors.New("RPDB requires an API key")
		}
	case "postersplus":
		u, e := url.Parse(p.BaseURL)
		if e != nil || !posterURL(u) || u.RawQuery != "" || strings.ContainsAny(p.BaseURL, "{}") {
			return errors.New("PostersPlus requires an HTTP(S) base URL without credentials or query")
		}
	case "betterposters", "custom":
		template := p.URLTemplate
		if template == "" && p.Source == "betterposters" {
			return nil
		}
		u, e := url.Parse(template)
		if e != nil || !posterURL(u) || strings.ContainsAny(u.Host, "{}") {
			return errors.New("poster template requires a fixed HTTP(S) host without user information")
		}
		if p.Source == "betterposters" && (u.Scheme != "https" || u.Host != "btttr.cc") {
			return errors.New("BetterPosters templates must use https://btttr.cc")
		}
		hasID := false
		for _, key := range []string{"imdb_id", "tmdb_id", "type", "api_key"} {
			token := "{" + key + "}"
			if strings.Contains(template, token) && strings.HasSuffix(key, "_id") {
				hasID = true
			}
			template = strings.ReplaceAll(template, token, "")
		}
		if !hasID || strings.ContainsAny(template, "{}") {
			return errors.New("use {imdb_id}, or {tmdb_id} with {type}; optional {api_key}")
		}
		if strings.Contains(p.URLTemplate, "{tmdb_id}") && !strings.Contains(p.URLTemplate, "{type}") {
			return errors.New("TMDB templates require {type} to distinguish movie and TV IDs")
		}
	default:
		return errors.New("choose a supported poster source")
	}
	return nil
}
func posterURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
