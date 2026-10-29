package debrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// Espelho de TestTorBoxBackgroundDiscoversExistingAccountTorrent: o
// Premiumize não tem Find, mas tem CheckCached em lote. Em background
// ele deve validar pelo CheckCached e servir do cache sem nunca
// escrever na conta (transfer/create); não-cached é skip limpo.
func TestPremiumizeBackgroundValidatesViaCheckCachedWithoutWrites(t *testing.T) {
	for _, cached := range []bool{true, false} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			var checks, directdls, creates atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cache/check":
					checks.Add(1)
					resp := []bool{cached}
					b, _ := json.Marshal(resp)
					fmt.Fprintf(w, `{"status":"success","response":%s}`, b)
				case "/transfer/directdl":
					directdls.Add(1)
					fmt.Fprint(w, `{"status":"success","content":[{"path":"Movie/Movie.mkv","size":2500000000,"link":"https://pm.example/dl/Movie.mkv"}]}`)
				case "/transfer/create":
					creates.Add(1) // a única escrita possível na conta
					fmt.Fprint(w, `{"status":"success","id":"x"}`)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))
			defer srv.Close()
			p := newPremiumize(testKey, fastOpts(srv.URL))
			cfg := config.Debrid{CachedOnly: false, Providers: []config.DebridProvider{{ID: "pm", Kind: "premiumize", APIKey: testKey, Enabled: true}}}
			svc := NewService(cfg, Options{NewProvider: func(config.DebridProvider, ProviderOptions) (Provider, error) { return p, nil }})
			ctx := core.WithBackgroundBudget(t.Context(), func(context.Context, string, string, int64) error { return nil })
			payload, err := svc.Acquire(ctx, core.Release{ID: "pm-bg", Name: "Movie.2024.1080p", Protocol: core.ProtocolTorrent, InfoHash: testHash})
			if cached {
				if err != nil || !IsPayload(payload) {
					t.Fatalf("cached em background não serviu: %v", err)
				}
				got, err := DecodePayload(payload)
				if err != nil || got.Path != "Movie/Movie.mkv" || got.URL != "https://pm.example/dl/Movie.mkv" {
					t.Fatalf("payload errado: %+v %v", got, err)
				}
				if directdls.Load() == 0 {
					t.Fatal("cached sem directdl: payload não veio do cache")
				}
			} else {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("não-cached em background: %v, want skip ErrUnsupported", err)
				}
				if directdls.Load() != 0 {
					t.Fatalf("não-cached chamou directdl %d vezes", directdls.Load())
				}
			}
			if creates.Load() != 0 {
				t.Fatalf("background escreveu na conta: creates=%d", creates.Load())
			}
			if checks.Load() == 0 {
				t.Fatal("background não validou via CheckCached")
			}
		})
	}
}
