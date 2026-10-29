package simkl

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaseExclusionAndFencedRelease(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenPath(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := TokenStore{DB: db, Sealer: testSealer{}}
	token := Token{ClientID: "id", AccessToken: "a", RefreshToken: "r", ExpiresIn: 300, Scope: "media:read media:write"}
	now := time.Now()
	if err := s.Save(ctx, 1, token, now); err != nil {
		t.Fatal(err)
	}
	first, err := s.AcquireLease(ctx, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireLease(ctx, 1, now); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent claim: %v", err)
	}
	second, err := s.AcquireLease(ctx, 1, now.Add(91*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, 1, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireLease(ctx, 1, now.Add(92*time.Second)); !errors.Is(err, ErrBusy) {
		t.Fatalf("old owner released new lease: %v", err)
	}
	if err := s.ReleaseLease(ctx, 1, second); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshRequiresLeaseAndKeepsAccount(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenPath(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := TokenStore{DB: db, Sealer: testSealer{}}
	tok := Token{ClientID: "id", AccessToken: "a", RefreshToken: "r", ExpiresIn: 300, Scope: "media:read media:write"}
	if err := s.Save(ctx, 1, tok, time.Now()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"r","expires_in":300,"scope":"media:read media:write"}`))
	}))
	defer srv.Close()
	auth := AuthClient{BaseURL: srv.URL, ClientID: "id"}
	if _, err := s.Refresh(ctx, 1, "wrong", auth); !errors.Is(err, ErrBusy) || calls != 0 {
		t.Fatalf("unleased refresh: %v", err)
	}
	id, err := s.AcquireLease(ctx, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.Refresh(ctx, 1, id, auth)
	if err != nil || grant.Access != "new" {
		t.Fatalf("refresh: %+v %v", grant, err)
	}
	loaded, err := s.Load(ctx, 1)
	if err != nil || loaded.Access != "new" {
		t.Fatalf("stored: %+v %v", loaded, err)
	}
	if err := s.Delete(ctx, 1); !errors.Is(err, ErrBusy) {
		t.Fatalf("disconnect bypassed lease: %v", err)
	}
}
