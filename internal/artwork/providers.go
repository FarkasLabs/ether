package artwork

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"golang.org/x/image/draw"
)

var imdbID = regexp.MustCompile(`^tt[0-9]{5,12}$`)

var errProvider = errors.New("artwork: provider unavailable")

func (im *Images) posterSettings() config.Posters {
	if im.o.Posters != nil {
		return im.o.Posters()
	}
	return config.Posters{}
}
func posterVersion(p config.Posters) string {
	if !p.Enabled {
		return ""
	}
	b, _ := json.Marshal(p)
	// Periodic refresh permits provider-side style and rating changes.
	b = append(b, []byte(strconv.FormatInt(time.Now().Unix()/21600, 10))...)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (im *Images) PosterVersion() string { return posterVersion(im.posterSettings()) }

// GetPoster never sends provider URLs to a client. Missing IDs, unsupported
// title kinds and every provider failure use the original TMDB artwork.
func (im *Images) GetPoster(ctx context.Context, t *core.Title, fallback string, w int) ([]byte, string, error) {
	p := im.posterSettings()
	if p.Enabled && p.Validate() == nil {
		if raw, ok := providerURL(p, t); ok {
			h := sha256.Sum256([]byte(raw))
			key := "provider-v1/" + posterVersion(p) + "/" + hex.EncodeToString(h[:]) + "/" + strconv.Itoa(clampWidth(w))
			data, ct, err := im.get(ctx, key, func(ctx context.Context) ([]byte, string, error) { return im.fetchPoster(ctx, raw, w) })
			if err == nil {
				return data, ct, nil
			}
		}
	}
	return im.Get(ctx, fallback, w)
}

func providerURL(p config.Posters, t *core.Title) (string, bool) {
	if t == nil || (t.Type != core.TypeMovie && t.Type != core.TypeShow) {
		return "", false
	}
	kind := "movie"
	if t.Type == core.TypeShow {
		kind = "tv"
	}
	id := t.IDs.TMDB
	imdb := t.IDs.IMDB
	if !imdbID.MatchString(imdb) {
		imdb = ""
	}
	switch p.Source {
	case "postersplus":
		if id <= 0 {
			return "", false
		}
		u, _ := url.Parse(strings.TrimRight(p.BaseURL, "/") + "/poster")
		q := u.Query()
		q.Set("tmdb_id", strconv.FormatInt(id, 10))
		q.Set("type", kind)
		if p.APIKey != "" {
			q.Set("access_key", p.APIKey)
		}
		u.RawQuery = q.Encode()
		return u.String(), true
	case "rpdb":
		if id <= 0 {
			return "", false
		}
		if kind == "tv" {
			kind = "series"
		}
		return "https://api.ratingposterdb.com/" + url.PathEscape(p.APIKey) + "/tmdb/poster-default/" + kind + "-" + strconv.FormatInt(id, 10) + ".jpg", true
	case "betterposters", "custom":
		template := p.URLTemplate
		if template == "" && p.Source == "betterposters" {
			template = "https://btttr.cc/poster/imdb/poster-default/{imdb_id}.jpg"
		}
		if strings.Contains(template, "{imdb_id}") && imdb == "" || strings.Contains(template, "{tmdb_id}") && id <= 0 {
			return "", false
		}
		// Substitute query and path independently so credentials cannot inject fields.
		u, err := url.Parse(template)
		if err != nil {
			return "", false
		}
		replace := func(s string, escape func(string) string) string {
			return strings.NewReplacer("{imdb_id}", escape(imdb), "{tmdb_id}", strconv.FormatInt(id, 10), "{type}", kind, "{api_key}", escape(p.APIKey)).Replace(s)
		}
		path := replace(u.Path, url.PathEscape)
		u.Path, _ = url.PathUnescape(path)
		u.RawPath = path
		u.RawQuery = replace(u.RawQuery, url.QueryEscape)
		return u.String(), true
	}
	return "", false
}

func (im *Images) fetchPoster(ctx context.Context, raw string, w int) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	select {
	case im.posterSlots <- struct{}{}:
		defer func() { <-im.posterSlots }()
	case <-ctx.Done():
		return nil, "", errProvider
	}
	client := *im.o.HTTPClient
	// No redirects: prevents credential forwarding, HTTPS downgrade and LAN pivots.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", errProvider
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", errProvider
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", errProvider
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil || len(data) > 8<<20 {
		return nil, "", errProvider
	}
	data, err = ResizePoster(data, w)
	if err != nil {
		return nil, "", errProvider
	}
	return data, "image/jpeg", nil
}

// ResizePoster validates the decode budget before allocating pixels. Raster
// re-encoding also excludes SVG scripts, animation and upstream metadata.
func ResizePoster(data []byte, w int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 16000000 {
		return nil, errProvider
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errProvider
	}
	w = min(clampWidth(w), cfg.Width)
	h := max(1, cfg.Height*w/cfg.Width)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	err = jpeg.Encode(&out, dst, &jpeg.Options{Quality: 88})
	return out.Bytes(), err
}
