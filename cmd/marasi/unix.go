//go:build unix

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func isLockUnavailable(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK)
}

func lockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

func secureInstancesDir(path string) error {
	return os.Chmod(path, 0700)
}

func secureInstanceFile(path string) error {
	return os.Chmod(path, 0600)
}
