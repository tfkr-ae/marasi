//go:build unix

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// IsUnavailable reports whether err indicates contention for a file lock.
func IsUnavailable(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK)
}

// TryLock acquires an exclusive lock on the whole file without waiting.
// IsUnavailable identifies lock contention in the returned error.
// The caller owns file and releases the lock with Unlock or by closing file.
func TryLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// Unlock releases the lock on file without closing file.
func Unlock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
