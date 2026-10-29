package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/newznab"
)

// prowlarrTestApp builds an App backed by a throwaway config store and a
// fake Prowlarr whose indexer list body can be swapped between calls.
func prowlarrTestApp(t *testing.T, body *string, status *int, retryAfter string) (*App, *config.Store, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		if *status != http.StatusOK {
			w.WriteHeader(*status)
			return
		}
		fmt.Fprint(w, *body)
	}))
	t.Cleanup(srv.Close)
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{cfg: store, log: quietLog()}, store, srv.URL
}

func postProwlarr(t *testing.T, a *App, url string, doImport bool) *httptest.ResponseRecorder {
	t.Helper()
	payload := fmt.Sprintf(`{"config":{"url":%q,"apiKey":"k","dailyLimit":0,"dailyGrabs":0,"priority":0},"import":%v}`, url, doImport)
	req := httptest.NewRequest(http.MethodPost, "/prowlarr", strings.NewReader(payload))
	w := httptest.NewRecorder()
	a.playbackPolicyHandler().ServeHTTP(w, req)
	return w
}

func storedIDs(store *config.Store) []string {
	ids := make([]string, 0, len(store.Get().Indexers))
	for _, x := range store.Get().Indexers {
		ids = append(ids, x.ID)
	}
	return ids
}

// Preview reports skipped unknown-protocol indexers and warnings, saves
// nothing, and the response carries no removal field.
func TestProwlarrPreviewReportsSkippedAndWarnings(t *testing.T) {
	body := `[{"id":4,"name":"News","enable":true,"protocol":"usenet"},{"id":5,"name":"Odd","enable":true,"protocol":"rss"}]`
	status := http.StatusOK
	a, store, url := prowlarrTestApp(t, &body, &status, "")
	w := postProwlarr(t, a, url, false)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Indexers               []config.Indexer `json:"indexers"`
		Imported               bool             `json:"imported"`
		SkippedUnknownProtocol int              `json:"skippedUnknownProtocol"`
		Warnings               []string         `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Imported || len(out.Indexers) != 1 || out.SkippedUnknownProtocol != 1 {
		t.Fatalf("preview %+v", out)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "unsupported protocol") {
		t.Fatalf("warnings %+v", out.Warnings)
	}
	if strings.Contains(w.Body.String(), `"removed"`) {
		t.Fatalf("response must not report removals: %s", w.Body.String())
	}
	if got := storedIDs(store); len(got) != 0 {
		t.Fatalf("preview saved indexers %v", got)
	}
}

// Import is additive (upsert only): importing twice, with the second
// discovery no longer reporting an indexer, must NOT remove it — nor any
// other stored indexer (manual or previously imported). This locks the
// owner's decision against implicit deletion on import.
func TestProwlarrImportIsAdditiveNeverRemovesAbsent(t *testing.T) {
	body := `[{"id":4,"name":"News","enable":true,"protocol":"usenet"},{"id":5,"name":"Odd","enable":true,"protocol":"rss"}]`
	status := http.StatusOK
	a, store, url := prowlarrTestApp(t, &body, &status, "")
	// Pre-existing entries: a manual one and a same-scope import from an
	// earlier run that this discovery does not report at all.
	prefix := newznab.ProwlarrIDPrefix(config.Prowlarr{URL: url})
	if prefix == "" {
		t.Fatal("empty prefix")
	}
	if err := store.Update(func(c *config.Config) error {
		c.Indexers = append(c.Indexers,
			config.Indexer{ID: "manual-1", Name: "manual"},
			config.Indexer{ID: prefix + "99", Name: "previously imported, now absent"},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	w := postProwlarr(t, a, url, true)
	if w.Code != http.StatusOK {
		t.Fatalf("import1 code=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"removed"`) {
		t.Fatalf("import must not report removals: %s", w.Body.String())
	}
	want := []string{"manual-1", prefix + "99", prefix + "4"}
	if got := storedIDs(store); !slices.Equal(got, want) {
		t.Fatalf("after import1 ids=%v want %v", got, want)
	}

	// Second import: indexer 4 vanished from the source; only 7 remains.
	// Nothing stored may be deleted.
	body = `[{"id":7,"name":"Other","enable":true,"protocol":"torrent"}]`
	w = postProwlarr(t, a, url, true)
	if w.Code != http.StatusOK {
		t.Fatalf("import2 code=%d body=%s", w.Code, w.Body.String())
	}
	want = []string{"manual-1", prefix + "99", prefix + "4", prefix + "7"}
	if got := storedIDs(store); !slices.Equal(got, want) {
		t.Fatalf("after import2 ids=%v want %v (import must be additive)", got, want)
	}

	// Third import with an empty discovery also removes nothing.
	body = `[]`
	w = postProwlarr(t, a, url, true)
	if w.Code != http.StatusOK {
		t.Fatalf("import3 code=%d body=%s", w.Code, w.Body.String())
	}
	if got := storedIDs(store); !slices.Equal(got, want) {
		t.Fatalf("after empty import ids=%v want %v", got, want)
	}
}

// A 429 from Prowlarr propagates a validated Retry-After to the caller;
// malformed/huge values are never forwarded verbatim.
func TestProwlarrHandlerPropagatesValidatedRetryAfter(t *testing.T) {
	body := ``
	status := http.StatusTooManyRequests
	a, _, url := prowlarrTestApp(t, &body, &status, "120")
	w := postProwlarr(t, a, url, false)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code=%d want 502", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "120" {
		t.Fatalf("Retry-After=%q want 120", got)
	}

	a2, _, url2 := prowlarrTestApp(t, &body, &status, "soon")
	w = postProwlarr(t, a2, url2, false)
	if got := w.Header().Get("Retry-After"); got != "" {
		t.Fatalf("malformed Retry-After forwarded verbatim: %q", got)
	}

	a3, _, url3 := prowlarrTestApp(t, &body, &status, "999999999")
	w = postProwlarr(t, a3, url3, false)
	if got := w.Header().Get("Retry-After"); got != "86400" {
		t.Fatalf("huge Retry-After=%q want capped 86400", got)
	}
}

func TestProwlarrRetryAfterHeaderValidation(t *testing.T) {
	if got := prowlarrRetryAfterHeader(""); got != "" {
		t.Fatalf("empty=%q", got)
	}
	if got := prowlarrRetryAfterHeader("45"); got != "45" {
		t.Fatalf("45=%q", got)
	}
	if got := prowlarrRetryAfterHeader("999999999"); got != "86400" {
		t.Fatalf("huge=%q want 86400", got)
	}
}
