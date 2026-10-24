// Package stagedfile publishes a file from a stream so that readers of its
// directory never see it partly written and a racing writer never replaces it.
package stagedfile

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrExists means the destination already exists and was left alone.
var ErrExists = errors.New("destination already exists")

// Publish writes contents to a file staged in stageDir, named by pattern as
// os.CreateTemp names it, and then links it to destination. stageDir must be
// on the same file system as destination and outside any directory that
// readers list. Publish fails with ErrExists rather than replace
// destination, and a failure leaves no file behind.
func Publish(stageDir, pattern, destination string, contents io.Reader) (err error) {
	// Refuse a taken name before reading what may be a large body.
	if _, err := os.Lstat(destination); err == nil {
		return ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", destination, err)
	}
	staged, err := os.CreateTemp(stageDir, pattern)
	if err != nil {
		return fmt.Errorf("staging: %w", err)
	}
	defer func() {
		err = errors.Join(err, os.Remove(staged.Name()))
	}()
	if _, err := io.Copy(staged, contents); err != nil {
		staged.Close()
		return fmt.Errorf("writing: %w", err)
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return fmt.Errorf("syncing: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("closing: %w", err)
	}
	// Link creates the name exclusively, so a racing writer cannot be replaced.
	if err := os.Link(staged.Name(), destination); errors.Is(err, os.ErrExist) {
		return ErrExists
	} else if err != nil {
		return fmt.Errorf("publishing %s: %w", destination, err)
	}
	return nil
}
