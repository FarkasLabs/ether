//go:build windows

package app

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockInstanceFile takes a non-blocking exclusive LockFileEx on the first
// byte of the file. The lock is released by the OS if the process dies, so a
// stale ether.lock file never blocks the next boot.
func tryLockInstanceFile(f *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errInstanceLocked
	}
	return err
}

func unlockInstanceFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
