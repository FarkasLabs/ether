package watchlist

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// runPasses runs Run until stop and counts its passes (Users calls).
func runPasses(t *testing.T, s *Service, passes *atomic.Int32) func() {
	t.Helper()
	s.o.Users = func(context.Context) ([]core.User, error) {
		passes.Add(1)
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

func waitPasses(t *testing.T, passes *atomic.Int32, n int32, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for passes.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("passes %d, want %d", passes.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunResumesRecordedSchedule(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var clock atomic.Int64
	clock.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	opts := Options{SyncInterval: time.Hour, ResumeSchedule: true, Now: func() time.Time { return time.UnixMilli(clock.Load()) }}

	// Wait for the durable write, not just the Users callback that precedes it.
	var first atomic.Int32
	stop := runPasses(t, New(db, opts), &first)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var recorded string
		if err := db.QueryRowContext(context.Background(), `SELECT v FROM watchlist_meta WHERE k='last_pass'`).Scan(&recorded); err == nil && recorded != "" {
			break
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("pass was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if first.Load() != 1 {
		t.Fatalf("first leader passes = %d", first.Load())
	}

	next := New(db, opts)
	if got := next.untilDue(context.Background()); got != time.Hour {
		t.Fatalf("new leader due in %v, want 1h", got)
	}
	clock.Add(int64((time.Hour - time.Minute) / time.Millisecond))
	if got := next.untilDue(context.Background()); got != time.Minute {
		t.Fatalf("new leader due in %v, want 1m", got)
	}
	clock.Add(int64(time.Minute / time.Millisecond))
	if got := next.untilDue(context.Background()); got != 0 {
		t.Fatalf("new leader due in %v, want 0", got)
	}
	var passes atomic.Int32
	stop = runPasses(t, next, &passes)
	defer stop()
	waitPasses(t, &passes, 1, 5*time.Second)
}

func TestRunPassesAtStartByDefault(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(db, Options{SyncInterval: time.Hour})
	s.recordPass(context.Background()) // a recent pass is ignored without ResumeSchedule
	var n atomic.Int32
	stop := runPasses(t, s, &n)
	defer stop()
	waitPasses(t, &n, 1, 2*time.Second)
}

func TestFutureRecordedPassCannotDeferMoreThanOneInterval(t *testing.T) {
	db := store.OpenTest(t)
	now := time.Now().Round(0)
	s := New(db, Options{SyncInterval: time.Hour, ResumeSchedule: true, Now: func() time.Time { return now }})
	future := now.Add(72 * time.Hour)
	s.o.Now = func() time.Time { return future }
	s.recordPass(t.Context())
	s.o.Now = func() time.Time { return now }
	if got := s.untilDue(t.Context()); got != time.Hour {
		t.Fatalf("clock correction postpones work for %v", got)
	}
}
