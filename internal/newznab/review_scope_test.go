package newznab

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/braedonsaunders/ether/internal/config"
	"testing"
)

func TestReviewExistingProwlarrScopeSurvivesUpgrade(t *testing.T) {
	for _, raw := range []string{"http://PROWLARR.lan:9696", "http://prowlarr.lan:80"} {
		sum := sha256.Sum256([]byte(raw))
		old := "prowlarr-" + hex.EncodeToString(sum[:6]) + "-"
		got := ProwlarrIDPrefix(config.Prowlarr{URL: raw})
		if got != old {
			t.Errorf("unchanged saved URL %s: existing prefix %s becomes %s; additive import appends duplicate indexer", raw, old, got)
		}
	}
}
