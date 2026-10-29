package debrid

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// dlAccountLists conta só as listagens de conta inteira (sem ids=);
// GET /seedbox/list?ids=... é a leitura por-id do polling normal.
func dlService(t *testing.T, f *fakeDL) (*debridLink, *Service, *atomic.Int64, *httptest.Server) {
	t.Helper()
	base := f.handler(t)
	var accountLists atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/seedbox/list" && r.URL.Query().Get("ids") == "" {
			accountLists.Add(1)
		}
		base.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	p := newDebridLink(testKey, fastOpts(srv.URL))
	cfg := config.Debrid{CachedOnly: true, RemoveUncached: true, Providers: []config.DebridProvider{{ID: "dl", Kind: "debridlink", APIKey: testKey, Enabled: true}}}
	svc := NewService(cfg, Options{NewProvider: func(config.DebridProvider, ProviderOptions) (Provider, error) { return p, nil }})
	return p, svc, &accountLists, srv
}

func dlRelease() core.Release {
	return core.Release{ID: "dl-episode", Name: "Show.S01", Protocol: core.ProtocolTorrent, InfoHash: testHash, Target: &core.ReleaseTarget{Season: 1, Episode: 2}}
}

func TestDebridLinkColdAcquireNeverListsTheAccountBeforeAddingCachedItem(t *testing.T) {
	f := &fakeDL{cached: true}
	p, svc, accountLists, _ := dlService(t, f)
	payload, err := svc.Acquire(t.Context(), dlRelease())
	if err != nil || !IsPayload(payload) {
		t.Fatalf("cold acquire: %v", err)
	}
	if accountLists.Load() != 0 {
		t.Fatalf("cold Play listou a conta: lists=%d", accountLists.Load())
	}
	f.mu.Lock()
	adds := len(f.adds)
	f.mu.Unlock()
	if adds != 1 {
		t.Fatalf("cold acquire adds=%d, want 1", adds)
	}
	if p.findKnown(strings.ToUpper(testHash)) != "T9" {
		t.Fatal("aquisição não reteve id reutilizável por hash")
	}

	// Duplicata (novo Service, mesmo provider/conta): reusa o id
	// conhecido por hash, sem listar a conta nem adicionar de novo.
	cfg := config.Debrid{CachedOnly: true, RemoveUncached: true, Providers: []config.DebridProvider{{ID: "dl", Kind: "debridlink", APIKey: testKey, Enabled: true}}}
	svc2 := NewService(cfg, Options{NewProvider: func(config.DebridProvider, ProviderOptions) (Provider, error) { return p, nil }})
	payload, err = svc2.Acquire(t.Context(), dlRelease())
	if err != nil || !IsPayload(payload) {
		t.Fatalf("duplicate acquire: %v", err)
	}
	got, err := DecodePayload(payload)
	if err != nil || got.TorrentID != "T9" {
		t.Fatalf("duplicata não reusou T9: %+v %v", got, err)
	}
	f.mu.Lock()
	adds = len(f.adds)
	f.mu.Unlock()
	if adds != 1 || accountLists.Load() != 0 {
		t.Fatalf("duplicata: adds=%d lists=%d", adds, accountLists.Load())
	}
}

func TestDebridLinkDuplicateAddRecoversExistingTorrentByHash(t *testing.T) {
	f := &fakeDL{cached: true, torrents: map[string]bool{"T9": true}}
	base := f.handler(t)
	var accountLists, adds atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/seedbox/add" {
			adds.Add(1)
			fmt.Fprint(w, `{"success":false,"error":"alreadyExists","error_description":"torrent already exists"}`)
			return
		}
		if r.URL.Path == "/seedbox/list" && r.URL.Query().Get("ids") == "" {
			accountLists.Add(1)
		}
		base.ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := newDebridLink(testKey, fastOpts(srv.URL))
	acq, err := p.Acquire(t.Context(), episodeReq(true))
	if err != nil || acq == nil || acq.TorrentID != "T9" || acq.Added {
		t.Fatalf("duplicate recovery: %+v err=%v", acq, err)
	}
	if accountLists.Load() != 1 || adds.Load() != 1 {
		t.Fatalf("duplicate recovery: lists=%d adds=%d", accountLists.Load(), adds.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) != 0 {
		t.Fatal("apagou item que já era da conta")
	}
}

func TestDebridLinkUnlistedDuplicateRemainsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"success":false,"error":"alreadyExists","error_description":"torrent already exists"}`)
		} else {
			fmt.Fprint(w, `{"success":true,"value":[]}`)
		}
	}))
	defer srv.Close()
	p := newDebridLink(testKey, fastOpts(srv.URL))
	_, err := p.Acquire(t.Context(), acquireReq(true))
	if !errors.Is(err, core.ErrSourceUnavailable) || errors.Is(err, core.ErrArticleMissing) || errors.Is(err, core.ErrReleaseRejected) {
		t.Fatalf("duplicata sem listagem virou permanente: %v", err)
	}
}
