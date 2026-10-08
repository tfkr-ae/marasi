//go:build windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// IsUnavailable reports whether err indicates contention for a file lock.
func IsUnavailable(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// TryLock acquires an exclusive lock on the first byte of file without waiting.
// IsUnavailable identifies lock contention in the returned error.
// The caller owns file and releases the lock with Unlock or by closing file.
func TryLock(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
}

// Unlock releases the lock on file without closing file.
func Unlock(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
