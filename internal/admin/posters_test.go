package admin

import (
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/config"
	"strings"
	"testing"
)

func TestPosterSecrets(t *testing.T) {
	c := config.Defaults(t.TempDir())
	c.Posters = config.Posters{Enabled: true, Source: "custom", APIKey: "private-key", URLTemplate: "http://lan/{type}/{tmdb_id}?secret=private-template"}
	redacted := RedactConfig(c)
	raw, _ := json.Marshal(redacted)
	if strings.Contains(string(raw), "private-") {
		t.Fatal("secret exposed")
	}
	restored, errs := UnredactConfig(redacted, c)
	if len(errs) > 0 || restored.Posters != c.Posters {
		t.Fatal(errs)
	}
	redacted.Posters.URLTemplate = "http://other/{type}/{tmdb_id}?key={api_key}"
	if _, errs = UnredactConfig(redacted, c); len(errs) == 0 {
		t.Fatal("forwarded retained key to new template")
	}
}
