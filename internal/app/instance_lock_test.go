package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestInstanceLockSecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	second, err := acquireInstanceLock(dir)
	if err == nil {
		second.Release()
		t.Fatal("second acquire on the same data dir should fail")
	}
	if !errors.Is(err, errAnotherInstanceRunning) {
		t.Fatalf("err %v should wrap errAnotherInstanceRunning", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("err %q should name the data dir %q", err, dir)
	}
	if want := "(" + "pid " + strconv.Itoa(os.Getpid()) + ")"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err %q should name the holder %s", err, want)
	}
}

func TestInstanceLockDifferentDirsOK(t *testing.T) {
	a, err := acquireInstanceLock(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	b, err := acquireInstanceLock(t.TempDir())
	if err != nil {
		t.Fatalf("different data dir should lock independently: %v", err)
	}
	defer b.Release()
}

func TestInstanceLockReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	first.Release() // idempotente

	second, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("acquire after release should succeed: %v", err)
	}
	defer second.Release()
}

func TestInstanceLockCreatesMissingSubdir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	lock, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("lock in a missing subdir should create it: %v", err)
	}
	defer lock.Release()
	if _, err := os.Stat(filepath.Join(dir, instanceLockName)); err != nil {
		t.Fatalf("lock file should exist: %v", err)
	}
}

// TestInstanceLockBlocksSecondApp é a integração com New: com o lock detido,
// um segundo App no mesmo DataDir falha antes de abrir o banco.
func TestInstanceLockBlocksSecondApp(t *testing.T) {
	dir := t.TempDir()
	held, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(context.Background(), Options{DataDir: dir, Logger: quiet})
	if err == nil {
		a.Close()
		t.Fatal("New on a locked data dir should fail")
	}
	if !errors.Is(err, errAnotherInstanceRunning) {
		t.Fatalf("err %v should wrap errAnotherInstanceRunning", err)
	}
}

func TestLockedStartupPreservesConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := cfg.Get().Server.Port
	held, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	t.Setenv("ETHER_PORT", "12345")
	_, err = New(context.Background(), Options{DataDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if !errors.Is(err, errAnotherInstanceRunning) {
		t.Fatalf("got %v", err)
	}
	reopened, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get().Server.Port; got != before {
		t.Fatalf("port changed from %d to %d", before, got)
	}
}

func TestFailedStartupReleasesInstanceLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ETHER_PORT", "invalid")
	_, err := New(context.Background(), Options{DataDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil {
		t.Fatal("expected invalid port error")
	}
	lock, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("lock retained after failed New: %v", err)
	}
	lock.Release()
}
