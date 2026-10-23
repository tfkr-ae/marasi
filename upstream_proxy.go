package marasi

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"

	"github.com/tfkr-ae/marasi/domain"
)

// configFileMode is the permission marasi_config.yaml is written with: it can
// hold upstream proxy credentials, so other local users must not read it.
const configFileMode os.FileMode = 0600

var (
	// ErrInvalidUpstreamProxy reports an upstream proxy name, URL or CA that
	// cannot be used. Its messages never contain the URL, which can hold
	// credentials.
	ErrInvalidUpstreamProxy = errors.New("invalid upstream proxy")
	// ErrUpstreamProxyExists reports an add of a name already in the list.
	ErrUpstreamProxyExists = errors.New("upstream proxy already exists")
	// ErrUpstreamProxyNotFound reports a name missing from the list.
	ErrUpstreamProxyNotFound = errors.New("upstream proxy not found")
)

// UpstreamProxyScheme is a scheme an upstream proxy URL can use.
type UpstreamProxyScheme string

const (
	UpstreamProxyHTTP   UpstreamProxyScheme = "http"
	UpstreamProxyHTTPS  UpstreamProxyScheme = "https"
	UpstreamProxySOCKS5 UpstreamProxyScheme = "socks5"
)

// UpstreamProxyConfig is one entry of the upstream_proxies section of
// marasi_config.yaml (docs/adr/2026-10-23-upstream-proxies-are-listed-per-machine-and-selected-per-project.md).
type UpstreamProxyConfig struct {
	// Name matches the client-name pattern.
	Name string `mapstructure:"name" yaml:"name"`
	// URL is scheme://[user[:pass]@]host:port.
	URL string `mapstructure:"url" yaml:"url"`
	// CA is an extra trusted CA certificate as PEM, copied in when added.
	// Empty means none.
	CA string `mapstructure:"ca" yaml:"ca,omitempty"`
}

// UpstreamProxyUpdate changes an entry. A nil field is left as it is, and
// an empty CA removes the CA.
type UpstreamProxyUpdate struct {
	URL *string
	CA  *string
}

// UpstreamProxyAddress is an entry's URL without its credentials.
type UpstreamProxyAddress struct {
	Scheme         UpstreamProxyScheme
	Host           string
	Port           int
	HasCredentials bool
}

// UpstreamProxySelectionKey is the client setting under domain.ReservedClient
// that holds a project's upstream proxy selection as a JSON string. A
// missing key means none.
const UpstreamProxySelectionKey = "upstream_proxy.active"

// LoadUpstreamProxySelection reads a project's selection, "" for none.
func LoadUpstreamProxySelection(repository domain.ConfigRepository) (string, error) {
	setting, err := repository.GetClientSetting(domain.ReservedClient, UpstreamProxySelectionKey)
	if errors.Is(err, domain.ErrClientSettingNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var name string
	if err := json.Unmarshal(setting.Value, &name); err != nil {
		return "", fmt.Errorf("%w: the selection is not a JSON string", ErrInvalidUpstreamProxy)
	}
	return name, nil
}

// StoreUpstreamProxySelection writes a project's selection, deleting it for
// none ("").
func StoreUpstreamProxySelection(repository domain.ConfigRepository, name string) error {
	if name == "" {
		return repository.DeleteClientSetting(domain.ReservedClient, UpstreamProxySelectionKey)
	}
	value, err := json.Marshal(name)
	if err != nil {
		return err
	}
	_, err = repository.SetMarasiSetting(UpstreamProxySelectionKey, value)
	return err
}

// ValidateUpstreamProxyName checks name against the client-name pattern.
func ValidateUpstreamProxyName(name string) error {
	if domain.ValidateClientName(name) != nil {
		return fmt.Errorf("%w: name %q must match ^[a-z0-9][a-z0-9._-]{0,63}$", ErrInvalidUpstreamProxy, name)
	}
	return nil
}

// ParseUpstreamProxyURL parses an upstream proxy URL: scheme http, https or
// socks5, optional user:pass@, a host and a port, and nothing else. The
// returned URL keeps the credentials.
func ParseUpstreamProxyURL(raw string) (*url.URL, UpstreamProxyAddress, error) {
	// url.Parse errors quote the input, which can hold credentials.
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, UpstreamProxyAddress{}, fmt.Errorf("%w: the URL does not parse", ErrInvalidUpstreamProxy)
	}
	scheme := UpstreamProxyScheme(parsed.Scheme)
	switch scheme {
	case UpstreamProxyHTTP, UpstreamProxyHTTPS, UpstreamProxySOCKS5:
	default:
		return nil, UpstreamProxyAddress{}, fmt.Errorf("%w: the URL scheme must be http, https or socks5", ErrInvalidUpstreamProxy)
	}
	if parsed.Opaque != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, UpstreamProxyAddress{}, fmt.Errorf("%w: the URL must have only a scheme, credentials, a host and a port", ErrInvalidUpstreamProxy)
	}
	if parsed.Hostname() == "" {
		return nil, UpstreamProxyAddress{}, fmt.Errorf("%w: the URL has no host", ErrInvalidUpstreamProxy)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, UpstreamProxyAddress{}, fmt.Errorf("%w: the URL needs a port from 1 to 65535", ErrInvalidUpstreamProxy)
	}
	return parsed, UpstreamProxyAddress{
		Scheme:         scheme,
		Host:           parsed.Hostname(),
		Port:           port,
		HasCredentials: parsed.User != nil,
	}, nil
}

// ParseUpstreamProxyCA parses a PEM bundle that must hold at least one
// certificate and returns them.
func ParseUpstreamProxyCA(data string) ([]*x509.Certificate, error) {
	var certificates []*x509.Certificate
	rest := []byte(data)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: the CA holds a certificate that does not parse", ErrInvalidUpstreamProxy)
		}
		certificates = append(certificates, certificate)
	}
	if len(certificates) == 0 {
		return nil, fmt.Errorf("%w: the CA must be PEM holding at least one certificate", ErrInvalidUpstreamProxy)
	}
	return certificates, nil
}

// validateUpstreamProxy checks every field of entry.
func validateUpstreamProxy(entry UpstreamProxyConfig) error {
	if err := ValidateUpstreamProxyName(entry.Name); err != nil {
		return err
	}
	if _, _, err := ParseUpstreamProxyURL(entry.URL); err != nil {
		return err
	}
	if entry.CA != "" {
		if _, err := ParseUpstreamProxyCA(entry.CA); err != nil {
			return err
		}
	}
	return nil
}

// AddUpstreamProxy appends entry to the upstream proxy list and saves the
// configuration. Callers hold the config lock and reread first
// (docs/adr/2026-10-06-chrome-config-lock-reread.md).
func (cfg *Config) AddUpstreamProxy(entry UpstreamProxyConfig) error {
	if err := validateUpstreamProxy(entry); err != nil {
		return err
	}
	if cfg.upstreamProxyIndex(entry.Name) >= 0 {
		return fmt.Errorf("%w: %s", ErrUpstreamProxyExists, entry.Name)
	}
	return cfg.saveUpstreamProxies(append(slices.Clone(cfg.UpstreamProxies), entry))
}

// UpdateUpstreamProxy changes the named entry and saves the configuration.
// Callers hold the config lock and reread first.
func (cfg *Config) UpdateUpstreamProxy(name string, update UpstreamProxyUpdate) error {
	index := cfg.upstreamProxyIndex(name)
	if index < 0 {
		return fmt.Errorf("%w: %s", ErrUpstreamProxyNotFound, name)
	}
	proxies := slices.Clone(cfg.UpstreamProxies)
	if update.URL != nil {
		proxies[index].URL = *update.URL
	}
	if update.CA != nil {
		proxies[index].CA = *update.CA
	}
	if err := validateUpstreamProxy(proxies[index]); err != nil {
		return err
	}
	return cfg.saveUpstreamProxies(proxies)
}

// RemoveUpstreamProxy removes the named entry and saves the configuration.
// Callers hold the config lock and reread first.
func (cfg *Config) RemoveUpstreamProxy(name string) error {
	index := cfg.upstreamProxyIndex(name)
	if index < 0 {
		return fmt.Errorf("%w: %s", ErrUpstreamProxyNotFound, name)
	}
	return cfg.saveUpstreamProxies(slices.Delete(slices.Clone(cfg.UpstreamProxies), index, index+1))
}

func (cfg *Config) upstreamProxyIndex(name string) int {
	return slices.IndexFunc(cfg.UpstreamProxies, func(entry UpstreamProxyConfig) bool {
		return entry.Name == name
	})
}

// saveUpstreamProxies writes proxies as upstream_proxies with owner-only
// permissions, because entries can hold credentials.
func (cfg *Config) saveUpstreamProxies(proxies []UpstreamProxyConfig) error {
	previous := cfg.viper.Get("upstream_proxies")
	cfg.viper.Set("upstream_proxies", proxies)
	if err := cfg.writeConfig(); err != nil {
		cfg.viper.Set("upstream_proxies", previous)
		return err
	}
	cfg.UpstreamProxies = proxies
	return nil
}

// writeConfig saves the configuration with configFileMode. The mode is set
// before writing, because viper keeps the mode of a file that exists.
func (cfg *Config) writeConfig() error {
	path := cfg.viper.ConfigFileUsed()
	if err := os.Chmod(path, configFileMode); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("restricting configuration permissions: %w", err)
	}
	cfg.viper.SetConfigPermissions(configFileMode)
	if err := cfg.viper.WriteConfig(); err != nil {
		return fmt.Errorf("saving configuration: %w", err)
	}
	return nil
}
