package collections

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReviewAniListLaterChunkFailure(t *testing.T) {
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			fmt.Fprint(w, `{"data":{"MediaListCollection":{"hasNextChunk":true,"lists":[{"name":"Watching","entries":[{"media":{"id":1,"format":"TV","title":{"romaji":"A"}}}]}]}}}`)
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	now := time.Now
	s := &Service{web: newWebClient(server.Client(), "Ether/test", now), bases: ExternalBases{AniListAPI: server.URL}, now: now, log: slog.Default()}
	s.anime.ids = map[int64]animeIDs{1: {show: 1}}
	s.anime.loaded = now()
	refs, err := s.fetchAniList(context.Background(), "https://anilist.co/user/kai/animelist")
	if err == nil {
		t.Fatalf("later chunk failure silently returned %d partial items", len(refs))
	}
}

func TestReviewAniListGraphQLErrorNotCached(t *testing.T) {
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			fmt.Fprint(w, `{"data":{"MediaListCollection":{"hasNextChunk":false,"lists":[]}},"errors":[{"message":"temporary"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"MediaListCollection":{"hasNextChunk":false,"lists":[]}}}`)
	}))
	defer server.Close()
	now := time.Now
	s := &Service{web: newWebClient(server.Client(), "Ether/test", now), bases: ExternalBases{AniListAPI: server.URL}, now: now, log: slog.Default()}
	s.anime.ids = map[int64]animeIDs{1: {show: 1}}
	s.anime.loaded = now()
	if _, err := s.fetchAniList(context.Background(), "https://anilist.co/user/kai/animelist"); err == nil {
		t.Fatal("partial GraphQL response must fail")
	}
	if _, err := s.fetchAniList(context.Background(), "https://anilist.co/user/kai/animelist"); err != nil {
		t.Fatalf("temporary GraphQL error cached: %v (requests %d)", err, n)
	}
}

func TestAniListChunkLimitIsExplicit(t *testing.T) {
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{"data":{"MediaListCollection":{"hasNextChunk":true,"lists":[]}}}`)
	}))
	defer server.Close()
	now := time.Now
	s := &Service{web: newWebClient(server.Client(), "Ether/test", now), bases: ExternalBases{AniListAPI: server.URL}, now: now, log: slog.Default()}
	_, err := s.fetchAniList(context.Background(), "https://anilist.co/user/kai/animelist")
	if err == nil || n != maxExternalPages {
		t.Fatalf("expected truncation error after %d requests, got %d: %v", maxExternalPages, n, err)
	}
}

func TestAniListRateLimitRespectsCancellation(t *testing.T) {
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Retry-After", "60")
		http.Error(w, "limited", http.StatusTooManyRequests)
	}))
	defer server.Close()
	now := time.Now
	s := &Service{web: newWebClient(server.Client(), "Ether/test", now), bases: ExternalBases{AniListAPI: server.URL}, now: now, log: slog.Default()}
	_, err := s.fetchAniList(context.Background(), "https://anilist.co/user/kai/animelist")
	if err == nil {
		t.Fatal("expected rate limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = s.fetchAniList(ctx, "https://anilist.co/user/kai/animelist")
	if !errors.Is(err, context.DeadlineExceeded) || n != 1 {
		t.Fatalf("expected cancelled backoff and one request, got %v, %d", err, n)
	}
}

func TestExternalRedirectOrigin(t *testing.T) {
	first, _ := http.NewRequest(http.MethodGet, "https://graphql.anilist.co/", nil)
	for _, tc := range []struct {
		url     string
		allowed bool
	}{
		{"https://graphql.anilist.co/next", true},
		{"http://graphql.anilist.co/next", false},
		{"https://127.0.0.1/", false},
	} {
		next, _ := http.NewRequest(http.MethodGet, tc.url, nil)
		if (sameOriginRedirect(next, []*http.Request{first}) == nil) != tc.allowed {
			t.Fatalf("redirect %s allowed=%v", tc.url, tc.allowed)
		}
	}
}

func TestAnimeMappingRejectsTruncatedArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"anilist_id":1,"themoviedb_id":{"tv":2}}`)
	}))
	defer server.Close()
	_, err := fetchAnimeIDs(context.Background(), newWebClient(server.Client(), "Ether/test", time.Now), server.URL)
	if err == nil {
		t.Fatal("truncated mapping must fail")
	}
}
