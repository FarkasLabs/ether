package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/braedonsaunders/ether/internal/config"
)

// instanceLockName is the per-DataDir lock file. Two Ether processes on the
// same DataDir would fight over SQLite and let the transcode cleanStale of
// the second boot delete the first process's live s-*/download files, so
// only one instance may own a DataDir at a time.
const instanceLockName = "ether.lock"

// errAnotherInstanceRunning marks the contention case so callers/tests can
// match it with errors.Is; the wrapped message names the DataDir and, when
// the holder recorded it, its pid.
var errAnotherInstanceRunning = errors.New("another Ether instance is already running")

// errInstanceLocked is returned by the platform try-lock when the lock is
// already held. acquireInstanceLock translates it into
// errAnotherInstanceRunning with context.
var errInstanceLocked = errors.New("instance lock already held")

// instanceLock is a held lock. Release is idempotent; the lock file is left
// in place on purpose — unlinking it would let a contender open a fresh
// inode and lock that while the old holder still owns the unlinked one.
type instanceLock struct {
	file        *os.File
	path        string
	releaseOnce sync.Once
}

// acquireInstanceLock takes the exclusive, non-blocking lock for dataDir,
// creating dataDir (owner-only, via config.EnsureDataDir) and the lock file
// when missing. The holder's pid is recorded in the file for diagnostics.
func acquireInstanceLock(dataDir string) (*instanceLock, error) {
	if err := config.EnsureDataDir(dataDir); err != nil {
		return nil, fmt.Errorf("instance lock: %w", err)
	}
	path := filepath.Join(dataDir, instanceLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("instance lock %s: %w", path, err)
	}
	if err := tryLockInstanceFile(f); err != nil {
		pid := readLockPID(f)
		_ = f.Close()
		if errors.Is(err, errInstanceLocked) {
			if pid > 0 {
				return nil, fmt.Errorf("%w with data dir %s (pid %d)", errAnotherInstanceRunning, dataDir, pid)
			}
			return nil, fmt.Errorf("%w with data dir %s", errAnotherInstanceRunning, dataDir)
		}
		return nil, fmt.Errorf("instance lock %s: %w", path, err)
	}
	// Best effort only where a failure would strand a held lock: surface it,
	// releasing the lock, rather than running without the recorded pid.
	if err := writeLockPID(f); err != nil {
		_ = unlockInstanceFile(f)
		_ = f.Close()
		return nil, fmt.Errorf("instance lock %s: %w", path, err)
	}
	return &instanceLock{file: f, path: path}, nil
}

// Release unlocks and closes the lock file. Safe to call more than once.
func (l *instanceLock) Release() {
	if l == nil {
		return
	}
	l.releaseOnce.Do(func() {
		_ = unlockInstanceFile(l.file)
		_ = l.file.Close()
	})
}

// writeLockPID replaces the lock file contents with the current pid.
func writeLockPID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return err
}

// readLockPID reads the pid recorded by the current holder, or 0 when the
// file has no parseable pid.
func readLockPID(f *os.File) int {
	buf := make([]byte, 64)
	n, err := f.ReadAt(buf, 0)
	if err != nil && n == 0 {
		return 0
	}
	fields := strings.Fields(string(buf[:n]))
	if len(fields) == 0 {
		return 0
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
