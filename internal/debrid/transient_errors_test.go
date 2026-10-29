package debrid

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// Erros transitórios de provider (link "delayed" do AllDebrid,
// srvMaint do Debrid-Link) devem ser classificáveis por errors.Is e
// NÃO podem envenenar o breaker: nem outage (failures/backoff) nem
// blocked contam.
func TestTransientProviderErrorsDoNotTripBreaker(t *testing.T) {
	// AllDebrid: unlock sem link, com delayed.
	adSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"delayed":12345}}`)
	}))
	defer adSrv.Close()
	ad := newAllDebrid(testKey, fastOpts(adSrv.URL))
	_, adErr := ad.Link(t.Context(), Handle{TorrentID: "555", Link: "https://alldebrid.com/f/abc"})

	// Debrid-Link: torrent pronto, mas srvMaint (Acquire e Link).
	f := &fakeDL{cached: true, srvMaint: true, torrents: map[string]bool{"T9": true}}
	dlSrv := httptest.NewServer(f.handler(t))
	defer dlSrv.Close()
	dl := newDebridLink(testKey, fastOpts(dlSrv.URL))
	_, dlAcqErr := dl.Acquire(t.Context(), episodeReq(true))
	_, dlLinkErr := dl.Link(t.Context(), Handle{TorrentID: "T9", FileID: "T9-2"})

	for name, err := range map[string]error{"alldebrid delayed": adErr, "debrid-link srvMaint acquire": dlAcqErr, "debrid-link srvMaint link": dlLinkErr} {
		if err == nil {
			t.Fatalf("%s: sem erro", name)
		}
		if !errors.Is(err, core.ErrSourceUnavailable) {
			t.Errorf("%s: não é transitório classificável: %v", name, err)
		}
		if errors.Is(err, ErrBlocked) || errors.Is(err, core.ErrNotCached) || errors.Is(err, ErrGone) || errors.Is(err, core.ErrArticleMissing) || errors.Is(err, core.ErrReleaseRejected) {
			t.Errorf("%s: classificado como permanente/blocked: %v", name, err)
		}
		// Repetido, nunca vira outage nem blocked no breaker.
		e := &entry{cfg: config.DebridProvider{ID: "x", Enabled: true}, sem: newLimiter(1)}
		now := time.Unix(1_800_000_000, 0)
		for i := range 5 {
			e.observe(now.Add(time.Duration(i)*time.Second), err)
		}
		if e.stats.Failures != 0 || e.stats.Blocked != 0 || e.state != "" || e.blocked(now.Add(time.Minute)) != nil {
			t.Errorf("%s envenenou o breaker: failures=%d blocked=%d state=%q", name, e.stats.Failures, e.stats.Blocked, e.state)
		}
	}
}
