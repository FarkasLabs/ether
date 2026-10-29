package ratings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

const omdbOK = `{"Response":"True","Ratings":[{"Source":"Internet Movie Database","Value":"8/10"}]}`

func refreshTestService(t *testing.T, h http.HandlerFunc) *Service {
	t.Helper()
	db, e := store.OpenMemory(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s := New(db, Options{OMDbBaseURL: srv.URL})
	if e := s.SaveSettings(context.Background(), Settings{Enabled: true, Provider: "omdb", APIKey: "secret", CacheHours: 24}); e != nil {
		t.Fatal(e)
	}
	return s
}

type refreshResult struct {
	badges []Badge
	err    error
}

func callRefresh(s *Service, ctx context.Context, title *core.Title) chan refreshResult {
	ch := make(chan refreshResult, 1)
	go func() {
		b, e := s.Refresh(ctx, title)
		ch <- refreshResult{b, e}
	}()
	return ch
}

func awaitResult(t *testing.T, ch chan refreshResult, what string) refreshResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout esperando %s (lock preso?)", what)
		return refreshResult{}
	}
}

// Mesma key concorrente: 1 só request HTTP, ambos recebem o resultado.
func TestRefreshSameKeySingleflight(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	s := refreshTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		_, _ = w.Write([]byte(omdbOK))
	})
	title := &core.Title{Key: core.MovieKey(1), IDs: core.IDs{IMDB: "tt0000001"}}
	ctx := context.Background()
	leader := callRefresh(s, ctx, title)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("líder não entrou no servidor")
	}
	waiter := callRefresh(s, ctx, title)
	// Dá tempo de o waiter se registrar no flight antes de liberar o líder.
	time.Sleep(100 * time.Millisecond)
	close(release)
	for i, ch := range []chan refreshResult{leader, waiter} {
		r := awaitResult(t, ch, "Refresh mesma key")
		if r.err != nil || len(r.badges) != 1 || r.badges[0].Value != 8 {
			t.Fatalf("resultado %d: %+v err=%v", i, r.badges, r.err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests HTTP=%d, want 1 (singleflight)", got)
	}
}

// Keys diferentes: barreira exige 2 requests em voo ao mesmo tempo.
// Se Refresh voltar a serializar (lock global durante HTTP), o segundo
// request nunca entra e o teste estoura o timeout da barreira.
func TestRefreshDifferentKeysParallel(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	s := refreshTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		_, _ = w.Write([]byte(omdbOK))
	})
	ctx := context.Background()
	a := callRefresh(s, ctx, &core.Title{Key: core.MovieKey(1), IDs: core.IDs{IMDB: "tt0000001"}})
	b := callRefresh(s, ctx, &core.Title{Key: core.MovieKey(2), IDs: core.IDs{IMDB: "tt0000002"}})
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release) // destrava o handler p/ o FAIL não virar hang
			t.Fatalf("só %d request(s) em voo: refreshes de keys diferentes serializaram", i)
		}
	}
	close(release)
	for _, ch := range []chan refreshResult{a, b} {
		if r := awaitResult(t, ch, "Refresh keys diferentes"); r.err != nil || len(r.badges) != 1 {
			t.Fatalf("badges=%+v err=%v", r.badges, r.err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests HTTP=%d, want 2", got)
	}
}

// Líder com ctx cancelado no meio do fetch: quem espera (ctx vivo) refaz
// e recebe sucesso; o Refresh seguinte completa (nada fica preso).
func TestRefreshCancelledLeaderWaiterRetries(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 4)
	s := refreshTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-r.Context().Done() // só destrava com o cancelamento do líder
			return
		}
		_, _ = w.Write([]byte(omdbOK))
	})
	title := &core.Title{Key: core.MovieKey(1), IDs: core.IDs{IMDB: "tt0000001"}}
	leaderCtx, cancel := context.WithCancel(context.Background())
	leader := callRefresh(s, leaderCtx, title)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("líder não entrou no servidor")
	}
	waiter := callRefresh(s, context.Background(), title)
	time.Sleep(100 * time.Millisecond) // waiter se registra no flight
	cancel()
	if r := awaitResult(t, leader, "líder cancelado"); r.err == nil {
		t.Fatalf("líder cancelado deveria errar, badges=%+v", r.badges)
	}
	r := awaitResult(t, waiter, "waiter após cancelamento do líder")
	if r.err != nil || len(r.badges) != 1 {
		t.Fatalf("waiter deveria refazer e ter sucesso: badges=%+v err=%v", r.badges, r.err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests HTTP=%d, want 2 (fetch cancelado + refetch do waiter)", got)
	}
	// Seguinte completa na hora (cache do refetch), sem lock/flight preso.
	r = awaitResult(t, callRefresh(s, context.Background(), title), "Refresh seguinte")
	if r.err != nil || len(r.badges) != 1 || calls.Load() != 2 {
		t.Fatalf("Refresh seguinte: badges=%+v err=%v calls=%d", r.badges, r.err, calls.Load())
	}
}
