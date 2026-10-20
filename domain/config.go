package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrClientSettingNotFound is returned when a client has no setting under a key.
var ErrClientSettingNotFound = errors.New("client setting not found")

// ErrInvalidClientSetting is returned when a client name, key or value breaks
// the shape rules of the client setting store.
var ErrInvalidClientSetting = errors.New("invalid client setting")

// ErrReservedClient is returned when a write targets ReservedClient.
var ErrReservedClient = errors.New("client name is reserved")

// ReservedClient is the client name Marasi keeps for its own settings.
// Clients can read and delete its keys but cannot write them.
const ReservedClient = "marasi"

// Limits of the client setting store.
const (
	MaxClientSettingKeyBytes   = 256
	MaxClientSettingValueBytes = 1 << 20
)

var clientNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateClientSettingAddress checks a client name and key.
// The client name matches ^[a-z0-9][a-z0-9._-]{0,63}$ and the key is
// non-empty, at most 256 bytes and printable UTF-8.
func ValidateClientSettingAddress(client, key string) error {
	if err := ValidateClientName(client); err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("%w: key is empty", ErrInvalidClientSetting)
	}
	if len(key) > MaxClientSettingKeyBytes {
		return fmt.Errorf("%w: key is longer than %d bytes", ErrInvalidClientSetting, MaxClientSettingKeyBytes)
	}
	if !utf8.ValidString(key) {
		return fmt.Errorf("%w: key is not valid UTF-8", ErrInvalidClientSetting)
	}
	for _, r := range key {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("%w: key has a non-printable character", ErrInvalidClientSetting)
		}
	}
	return nil
}

// ValidateClientName checks a client name against ^[a-z0-9][a-z0-9._-]{0,63}$.
func ValidateClientName(client string) error {
	if !clientNamePattern.MatchString(client) {
		return fmt.Errorf("%w: client name %q does not match %s", ErrInvalidClientSetting, client, clientNamePattern)
	}
	return nil
}

// ValidateClientSettingValue checks that value is JSON of at most 1 MiB.
func ValidateClientSettingValue(value json.RawMessage) error {
	if len(value) > MaxClientSettingValueBytes {
		return fmt.Errorf("%w: value is larger than %d bytes", ErrInvalidClientSetting, MaxClientSettingValueBytes)
	}
	if !json.Valid(value) {
		return fmt.Errorf("%w: value is not JSON", ErrInvalidClientSetting)
	}
	return nil
}

// ClientSetting is a JSON value that a client stores in a project under its
// own client name and a key. Marasi keeps it but never reads its meaning (docs/adr/2026-10-14-client-settings-are-opaque.md).
type ClientSetting struct {
	Client    string          `json:"client"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// ConfigRepository defines the interface for managing project-level configuration settings.
type ConfigRepository interface {
	// UpdateSPKI saves the Subject Public Key Information (SPKI) hash to a persistent configuration file.
	// This hash is used to verify the integrity and origin of application files,
	// ensuring they are opened by a trusted Marasi instance.
	UpdateSPKI(spki string) error

	// ListClientSettings returns client's settings whose key starts with
	// prefix, sorted by key. An empty prefix lists every key.
	ListClientSettings(client, prefix string) ([]ClientSetting, error)

	// GetClientSetting returns the setting that client stores under key,
	// or ErrClientSettingNotFound.
	GetClientSetting(client, key string) (ClientSetting, error)

	// SetClientSetting stores value under client and key, replacing only that key.
	SetClientSetting(client, key string, value json.RawMessage) (ClientSetting, error)

	// DeleteClientSetting removes the setting under client and key.
	// Deleting a missing key is not an error.
	DeleteClientSetting(client, key string) error
}
