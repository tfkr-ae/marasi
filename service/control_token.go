package service

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// controlTokenBytes is the control token's length in random bytes, before it
// is encoded as text.
const controlTokenBytes = 32

var errEmptyControlToken = errors.New("control token file is empty")

// ControlToken is a service instance's control token, kept in an owner-only
// file beside the instance's socket, lock, and log on the instance machine.
// It is read or generated on first use and then held in memory, so one
// instance compares every request against the same value.
type ControlToken struct {
	path string
	// secure applies the instance files' owner-only access to a file, the
	// same way it is applied to the socket, lock, and log on each platform.
	secure func(path string) error
	mu     sync.Mutex
	value  []byte
}

// NewControlToken returns the control token kept at path. secure is applied
// to the file before the token is written to it. Nothing is read or written
// until the token is first needed.
func NewControlToken(path string, secure func(path string) error) *ControlToken {
	return &ControlToken{path: path, secure: secure}
}

// Value returns the control token, reading its file, or generating and
// writing one when the file is absent. An existing file is reused, so the
// token survives restarts.
func (t *ControlToken) Value() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.value == nil {
		value, err := t.load()
		if err != nil {
			return "", err
		}
		t.value = value
	}
	return string(t.value), nil
}

// matches reports whether presented is the control token. The comparison
// takes the same time wherever the first difference is.
func (t *ControlToken) matches(presented string) bool {
	value, err := t.Value()
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(value)) == 1
}

func (t *ControlToken) load() ([]byte, error) {
	content, err := os.ReadFile(t.path)
	if err == nil {
		value := strings.TrimSpace(string(content))
		if value == "" {
			return nil, fmt.Errorf("reading control token %s: %w", t.path, errEmptyControlToken)
		}
		return []byte(value), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading control token %s: %w", t.path, err)
	}
	random := make([]byte, controlTokenBytes)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("generating control token: %w", err)
	}
	value := hex.EncodeToString(random)
	if err := t.write(value); err != nil {
		return nil, err
	}
	return []byte(value), nil
}

// write replaces the token file with value in one rename, so a reader never
// sees a partly written token.
func (t *ControlToken) write(value string) (resultErr error) {
	temp, err := os.CreateTemp(filepath.Dir(t.path), filepath.Base(t.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating control token file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if resultErr != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()
	if err := t.secure(tempPath); err != nil {
		return fmt.Errorf("securing control token file: %w", err)
	}
	if _, err := temp.WriteString(value + "\n"); err != nil {
		return fmt.Errorf("writing control token file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("syncing control token file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("closing control token file: %w", err)
	}
	if err := os.Rename(tempPath, t.path); err != nil {
		return fmt.Errorf("replacing control token file: %w", err)
	}
	return nil
}
