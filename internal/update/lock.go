package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tfkr-ae/marasi/internal/filelock"
)

// maxHolderSize bounds the holder name read from a lock file.
const maxHolderSize = 1 << 10

// Lock is the install lock for one installed build (docs/adr/2026-10-18-service-instances-install-updates.md). It is held
// across processes until Release.
type Lock struct {
	file *os.File
}

// LockedError reports an install of the same installed build running in
// another instance.
type LockedError struct {
	// Holder is the name of the instance holding the lock; "" when it could
	// not be read.
	Holder string
}

func (e *LockedError) Error() string {
	if e.Holder == "" {
		return "another instance is already installing an update for this build"
	}
	return fmt.Sprintf("instance %q is already installing an update for this build", e.Holder)
}

// AcquireLock takes the install lock for the installed build at target,
// recording holder (the instance name) for anyone who finds it held. The
// lock file lives in dir, named after target. When another holder has it,
// the error is a *LockedError. The lock is not reentrant: a second
// AcquireLock in the same process also fails.
func AcquireLock(dir, target, holder string) (*Lock, error) {
	if resolved, err := resolveTarget(target); err == nil {
		target = resolved
	}
	sum := sha256.Sum256([]byte(target))
	path := filepath.Join(dir, "update-install-"+hex.EncodeToString(sum[:8])+".lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the install lock: %w", err)
	}
	if err := filelock.TryLock(file); err != nil {
		defer file.Close()
		if filelock.IsUnavailable(err) {
			return nil, &LockedError{Holder: readHolder(file)}
		}
		return nil, fmt.Errorf("taking the install lock: %w", err)
	}
	// Windows locks only the first byte, and other processes cannot read a
	// locked range, so the holder is written after it.
	if err := writeHolder(file, holder); err != nil {
		return nil, errors.Join(fmt.Errorf("recording the install lock holder: %w", err), filelock.Unlock(file), file.Close())
	}
	return &Lock{file: file}, nil
}

// Release gives up the lock.
func (l *Lock) Release() error {
	return errors.Join(filelock.Unlock(l.file), l.file.Close())
}

func writeHolder(file *os.File, holder string) error {
	if err := file.Truncate(1); err != nil {
		return err
	}
	_, err := file.WriteAt([]byte(holder), 1)
	return err
}

func readHolder(file *os.File) string {
	data, err := io.ReadAll(io.NewSectionReader(file, 1, maxHolderSize))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
