//go:build !windows

package app

import (
	"errors"
	"os"
	"syscall"
)

// tryLockInstanceFile takes a non-blocking exclusive flock. The lock is tied
// to the open file description, so it is released by the kernel if the
// process dies — a stale ether.lock file never blocks the next boot.
func tryLockInstanceFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EACCES) {
		return errInstanceLocked
	}
	return err
}

func unlockInstanceFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
