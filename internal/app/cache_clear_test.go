package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/plextv"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOwnerCacheClearBlock(t *testing.T) {
	a := setupTestApp(t, true)
	if err := a.users.SetOwner(context.Background(), plextv.Account{ID: 42, Username: "owner"}, "test-owner"); err != nil {
		t.Fatal(err)
	}
	if err := a.cache.Put("release", 0, []byte("cached")); err != nil {
		t.Fatal(err)
	}
	call := func(token, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/ether/api/cache/clear-block", strings.NewReader(`{"releaseId":"release"}`))
		r.RemoteAddr = "127.0.0.1:1000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Plex-Token", token)
		r.Header.Set("X-Ether-Request", csrf)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("", "1"); w.Code != 401 && w.Code != 403 {
		t.Fatalf("anonymous %d", w.Code)
	}
	if w := call("test-owner", ""); w.Code != 403 {
		t.Fatalf("csrf %d", w.Code)
	}
	if err := a.cache.Pin("release", "playback:live"); err != nil {
		t.Fatal(err)
	}
	if w := call("test-owner", "1"); w.Code != 409 {
		t.Fatalf("live %d %s", w.Code, w.Body)
	}
	if blocked, _ := a.resolver.ReleaseBlocked(context.Background(), "release"); blocked {
		t.Fatal("active release blocked")
	}
	if err := a.cache.Unpin("release", "playback:live"); err != nil {
		t.Fatal(err)
	}
	if w := call("test-owner", "1"); w.Code != 200 || !strings.Contains(w.Body.String(), `"cleared":true`) {
		t.Fatalf("clear %d %s", w.Code, w.Body)
	}
	if blocked, err := a.resolver.ReleaseBlocked(context.Background(), "release"); err != nil || !blocked {
		t.Fatalf("block %v %v", blocked, err)
	}
	if a.cache.Has("release", 0) {
		t.Fatal("bytes remain")
	}
}
