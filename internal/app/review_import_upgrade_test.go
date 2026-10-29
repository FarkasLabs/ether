package app

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/braedonsaunders/ether/internal/config"
	"net/http"
	"strings"
	"testing"
)

func TestReviewLegacyImportDoesNotDuplicateOnUpgrade(t *testing.T) {
	body := `[{"id":4,"name":"News","enable":true,"protocol":"usenet"}]`
	status := http.StatusOK
	a, store, raw := prowlarrTestApp(t, &body, &status, "")
	raw = strings.Replace(raw, "127.0.0.1", "LOCALHOST", 1)
	hash := sha256.Sum256([]byte(raw))
	id := "prowlarr-" + hex.EncodeToString(hash[:6]) + "-4"
	if e := store.Update(func(c *config.Config) error {
		c.Indexers = []config.Indexer{{ID: id, Name: "previous import", URL: raw + "/4", Enabled: true}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	w := postProwlarr(t, a, raw, true)
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	if got := storedIDs(store); len(got) != 1 {
		t.Fatalf("same configured URL after upgrade duplicates enabled indexer: %v", got)
	}
}
