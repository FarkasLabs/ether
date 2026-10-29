package simkl

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestOutboxDeduplicatesAndIgnoresImports(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenPath(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := TokenStore{DB: db, Sealer: testSealer{}}
	tok := Token{ClientID: "id", AccessToken: "a", RefreshToken: "r", Scope: "media:read media:write", ExpiresIn: 300}
	if err := s.Save(ctx, 1, tok, time.Now()); err != nil {
		t.Fatal(err)
	}
	ev := state.Event{UserID: 1, Kind: state.EventCompleted, Key: core.MovieKey(123), At: time.Now()}
	if err := Enqueue(ctx, db, ev); err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(ctx, db, ev); err != nil {
		t.Fatal(err)
	}
	ev.Imported = true
	ev.Key = core.MovieKey(124)
	if err := Enqueue(ctx, db, ev); err != nil {
		t.Fatal(err)
	}
	ev.Imported = false
	ev.UserID = 2
	if err := Enqueue(ctx, db, ev); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM simkl_outbox`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("count %d err %v", n, err)
	}
	if err := s.Delete(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM simkl_outbox`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("disconnect count %d err %v", n, err)
	}
}

func TestFlushMovieAndSkipAnime(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenPath(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tokens := TokenStore{DB: db, Sealer: testSealer{}}
	tok := Token{ClientID: "id", AccessToken: "a", RefreshToken: "r", Scope: "media:read media:write", ExpiresIn: 7 * 24 * 3600}
	if err := tokens.Save(ctx, 1, tok, time.Now()); err != nil {
		t.Fatal(err)
	}
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "https://simkl.com/anime/42/foo")
			w.WriteHeader(301)
			return
		}
		posts++
		_, _ = w.Write([]byte(`{"not_found":{"movies":[],"shows":[],"episodes":[]}}`))
	}))
	defer srv.Close()
	s := Service{Auth: AuthClient{BaseURL: srv.URL, ClientID: "id"}, Tokens: tokens}
	at := time.Now().Add(-time.Minute)
	if err := Enqueue(ctx, db, state.Event{UserID: 1, Kind: state.EventCompleted, Key: core.MovieKey(123), At: at}); err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(ctx, db, state.Event{UserID: 1, Kind: state.EventCompleted, Key: core.EpisodeKey(456, 1, 1), At: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushOne(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE simkl_tokens SET next_post=0 WHERE user_id=1`); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushOne(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var n int
	var warning string
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM simkl_outbox),last_warning FROM simkl_tokens WHERE user_id=1`).Scan(&n, &warning); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || n != 0 || warning == "" {
		t.Fatalf("posts=%d queue=%d warning=%q", posts, n, warning)
	}
}

func TestStateCompletionFeedsOutboxWithoutImportEcho(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenPath(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tokens := TokenStore{DB: db, Sealer: testSealer{}}
	tok := Token{ClientID: "id", AccessToken: "a", RefreshToken: "r", ExpiresIn: 300, Scope: "media:read media:write"}
	if err := tokens.Save(ctx, 1, tok, time.Now()); err != nil {
		t.Fatal(err)
	}
	st := state.New(db, state.Options{})
	var enqueueErr error
	unsub := st.Subscribe(func(ev state.Event) {
		if err := Enqueue(ctx, db, ev); err != nil {
			enqueueErr = err
		}
	})
	defer unsub()
	if err := st.Scrobble(ctx, 1, core.MovieKey(123)); err != nil {
		t.Fatal(err)
	}
	if enqueueErr != nil {
		t.Fatal(enqueueErr)
	}
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	err = st.ImportTransaction(ctx, func(*sql.Tx) (state.ImportBatch, error) {
		return state.ImportBatch{Records: []state.ImportRecord{{UserID: 1, Key: core.MovieKey(456), Views: 1, ViewedAt: at}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM simkl_outbox WHERE user_id=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("outbox %d: %v", count, err)
	}
}

func TestDeliveryPacingIsSharedAcrossViewers(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenPath(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tokens := TokenStore{DB: db, Sealer: testSealer{}}
	tok := Token{ClientID: "id", AccessToken: "a", RefreshToken: "r", Scope: "media:read media:write", ExpiresIn: 7 * 24 * 3600}
	for _, id := range []int64{1, 2} {
		if err := tokens.Save(ctx, id, tok, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := Enqueue(ctx, db, state.Event{UserID: id, Kind: state.EventCompleted, Key: core.MovieKey(123), At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		_, _ = w.Write([]byte(`{"not_found":{"movies":[],"shows":[],"episodes":[]}}`))
	}))
	defer srv.Close()
	svc := Service{Auth: AuthClient{BaseURL: srv.URL, ClientID: "id"}, Tokens: tokens}
	if err := svc.FlushOne(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE simkl_post_pacing SET next_post=? WHERE id=1`, time.Now().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := svc.FlushOne(ctx, 2); !errors.Is(err, ErrBusy) || posts != 1 {
		t.Fatalf("shared pacing failed: %v posts=%d", err, posts)
	}
}
