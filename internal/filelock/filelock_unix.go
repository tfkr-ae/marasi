//go:build unix

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func IsUnavailable(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK)
}

func TryLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func Unlock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
