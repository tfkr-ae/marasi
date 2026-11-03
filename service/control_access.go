package service

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// ControlAccess is what a start or update of the network address changes
// about who may use it. The zero value changes nothing.
type ControlAccess struct {
	Password PasswordChange
	// AllowedOrigins replaces the origins that may call the network address
	// from a browser while it has no password. nil keeps them.
	AllowedOrigins *[]string
}

// passwordMode is what a PasswordChange does to the control password.
type passwordMode int

const (
	keepPassword passwordMode = iota
	setPassword
	removePassword
)

// PasswordChange is what a start or update does to the control password:
// keep it, which is the zero value, set it, or remove it.
type PasswordChange struct {
	mode  passwordMode
	value string
}

// SetPassword returns the change that sets the control password to value,
// which must not be empty.
func SetPassword(value string) PasswordChange {
	return PasswordChange{mode: setPassword, value: value}
}

// RemovePassword returns the change that removes the control password. It
// is also the consent that --no-password gives to serve beyond loopback
// without a password.
func RemovePassword() PasswordChange {
	return PasswordChange{mode: removePassword}
}

// keeps reports whether p leaves the control password as it is.
func (p PasswordChange) keeps() bool {
	return p.mode == keepPassword
}

// removes reports whether p removes the control password, which is the
// consent to run without one.
func (p PasswordChange) removes() bool {
	return p.mode == removePassword
}

// leavesSet reports whether a password is set after p, when current says
// whether one is set before it.
func (p PasswordChange) leavesSet(current bool) bool {
	switch p.mode {
	case setPassword:
		return true
	case removePassword:
		return false
	default:
		return current
	}
}

// nullOrigin is the Origin a browser sends from a sandboxed iframe or a local
// file, whatever site it is on.
const nullOrigin = "null"

// ErrInvalidAllowedOrigin refuses an allowed origin that a browser never
// sends as Origin.
var ErrInvalidAllowedOrigin = errors.New("an allowed origin is null or SCHEME://HOST[:PORT], such as http://localhost:5173")

// ErrPasswordRequired refuses a network address beyond loopback with no
// control password and no consent to run without one
// (docs/adr/2026-11-03-the-control-listener-network-address-is-guarded-by-an-optional-password.md).
var ErrPasswordRequired = errors.New("is beyond loopback: set a control password with --password, or pass --no-password to run without one")

// passwordRequiredError names the address that ErrPasswordRequired refused.
type passwordRequiredError struct{ endpoint string }

func (e passwordRequiredError) Error() string {
	return fmt.Sprintf("control listener network address %s %s", e.endpoint, ErrPasswordRequired)
}

func (e passwordRequiredError) Unwrap() error { return ErrPasswordRequired }

// CheckAllowedOrigins returns origins in the form a browser sends them,
// lowercase and without duplicates, in the order given. It returns
// ErrInvalidAllowedOrigin for an origin with a path, a query, credentials or
// a wildcard.
func CheckAllowedOrigins(origins []string) ([]string, error) {
	checked := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.ToLower(origin)
		if origin != nullOrigin {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
				parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(origin, "*") {
				return nil, fmt.Errorf("%w: %q", ErrInvalidAllowedOrigin, origin)
			}
		}
		if !slices.Contains(checked, origin) {
			checked = append(checked, origin)
		}
	}
	return checked, nil
}

// NullOriginWarning returns the warning line for allowing origins, or ""
// when they do not include null.
func NullOriginWarning(origins []string) string {
	if !slices.Contains(origins, nullOrigin) {
		return ""
	}
	return "warning: allowing the origin null lets a sandboxed iframe or a local file on any site call the control listener network address without a password"
}

// NoPasswordWarning returns the warning line for a network address bound at
// address with no control password.
func NoPasswordWarning(address string) string {
	if isLoopbackEndpoint(address) {
		return fmt.Sprintf("warning: control listener network address %s has no control password; any local process can control the instance", address)
	}
	return fmt.Sprintf("warning: control listener network address %s has no control password; any host that can reach it can control the instance", address)
}

// RequirePasswordBeyondLoopback returns an error wrapping
// ErrPasswordRequired when endpoint, as HOST:PORT, is beyond loopback,
// passwordSet is false, and noPassword, the --no-password consent, is false.
func RequirePasswordBeyondLoopback(endpoint string, passwordSet, noPassword bool) error {
	if passwordSet || noPassword || isLoopbackEndpoint(endpoint) {
		return nil
	}
	return passwordRequiredError{endpoint: endpoint}
}

// isLoopbackEndpoint reports whether endpoint, as HOST:PORT or a bare host,
// names a loopback IP or localhost. Any other hostname may resolve beyond
// loopback.
func isLoopbackEndpoint(endpoint string) bool {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		host = endpoint
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
