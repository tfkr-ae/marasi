package service

import (
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"unicode"
)

// ControlPassword is the control password that guards a service instance's
// network address. It lives only in the instance's memory: a restart hands
// it to the successor through the successor's environment, and stopping the
// instance forgets it
// (docs/adr/2026-11-03-the-control-listener-network-address-is-guarded-by-an-optional-password.md).
// An empty password means none is set.
type ControlPassword struct {
	mu    sync.Mutex
	value []byte
	// generation counts changes, so a connection can tell whether the
	// password it authenticated with is still the current one.
	generation uint64
}

// NewControlPassword returns a control password holding value, or none when
// value is empty.
func NewControlPassword(value string) *ControlPassword {
	return &ControlPassword{value: []byte(value)}
}

// Value returns the password, "" when none is set.
func (p *ControlPassword) Value() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.value)
}

// IsSet reports whether a password is set.
func (p *ControlPassword) IsSet() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.value) > 0
}

// set replaces the password, or removes it when value is empty, and returns
// the current generation and whether the password changed. Setting the
// password it already holds is no change, so it keeps its connections.
func (p *ControlPassword) set(value string) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if string(p.value) == value {
		return p.generation, false
	}
	p.value = []byte(value)
	p.generation++
	return p.generation, true
}

// admits reports whether a request presenting presented may pass, and the
// generation of the password it was checked against. With no password set,
// every request passes. The comparison takes the same time wherever the
// first difference is.
func (p *ControlPassword) admits(presented string, ok bool) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.value) == 0 {
		return p.generation, true
	}
	return p.generation, ok && subtle.ConstantTimeCompare([]byte(presented), p.value) == 1
}

// currentGeneration returns how many times the password has changed.
func (p *ControlPassword) currentGeneration() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.generation
}

// ErrInvalidControlPassword refuses a password that cannot be sent intact
// as Authorization: Bearer: the header drops surrounding whitespace and
// cannot carry control characters.
var ErrInvalidControlPassword = errors.New("a control password cannot start or end with whitespace or contain control characters")

// CheckControlPassword returns ErrInvalidControlPassword when password
// cannot be presented in an Authorization header.
func CheckControlPassword(password string) error {
	if strings.TrimSpace(password) != password || strings.ContainsFunc(password, unicode.IsControl) {
		return ErrInvalidControlPassword
	}
	return nil
}
