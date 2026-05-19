package marasi

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/viper"
	"github.com/tfkr-ae/marasi/chrome"
)

type Config struct {
	viper          *viper.Viper
	ConfigDir      string              `mapstructure:"config_dir"` // Current config dir
	DesktopOS      string              `mapstructure:"desktop_os"` // Operating system identifier
	ChromeDirs     []chrome.PathConfig `mapstructure:"chrome_dirs"`
	ChromeProfiles []string            `mapstructure:"chrome_profiles"`
}

// AddChromeProfile Adds a chrome profile to the configuration
// The path is created based on the name and will be in ConfigDir/chrome_profiles/{profileName}
func (cfg *Config) AddChromeProfile(name string) error {
	profileName := strings.TrimSpace(name)

	if profileName == "" {
		return errors.New("invalid profile name: cannot be empty")
	}

	if !filepath.IsLocal(profileName) {
		return errors.New("invalid profile name: absolute paths and parent directory references are not allowed")
	}

	if filepath.Base(profileName) != profileName {
		return errors.New("invalid profile name: subdirectories are not allowed")
	}

	if slices.Contains(cfg.ChromeProfiles, profileName) {
		return fmt.Errorf("chrome profile %q already exists", profileName)
	}

	profileDir := filepath.Join(cfg.ConfigDir, "chrome_profiles", profileName)
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return fmt.Errorf("failed to create chrome profile directory: %w", err)
	}

	cfg.ChromeProfiles = append(cfg.ChromeProfiles, profileName)
	cfg.viper.Set("chrome_profiles", cfg.ChromeProfiles)

	if err := cfg.viper.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	if err := cfg.viper.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshalling config to struct: %w", err)
	}

	return nil
}

// DeleteChromeProfile deletes a chrome profile from the configuration
// and removes its profile directory from disk.
func (cfg *Config) DeleteChromeProfile(name string) error {
	profileName := strings.TrimSpace(name)

	if profileName == "" {
		return errors.New("invalid profile name: cannot be empty")
	}

	if !filepath.IsLocal(profileName) || filepath.Base(profileName) != profileName {
		return errors.New("invalid profile name")
	}

	if !slices.Contains(cfg.ChromeProfiles, profileName) {
		return fmt.Errorf("chrome profile %q does not exist", profileName)
	}

	profileDir := filepath.Join(cfg.ConfigDir, "chrome_profiles", profileName)

	if err := os.RemoveAll(profileDir); err != nil {
		return fmt.Errorf("failed to delete chrome profile directory: %w", err)
	}

	cfg.ChromeProfiles = slices.DeleteFunc(cfg.ChromeProfiles, func(profile string) bool {
		return profile == profileName
	})

	cfg.viper.Set("chrome_profiles", cfg.ChromeProfiles)

	if err := cfg.viper.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	if err := cfg.viper.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshalling config to struct: %w", err)
	}

	return nil
}

func (cfg *Config) AddChromePath(path, os string) error {
	switch os {
	case "darwin", "linux", "windows":
		cfg.ChromeDirs = append(cfg.ChromeDirs, chrome.PathConfig{OS: os, Path: path})
		cfg.viper.Set("chrome_dirs", cfg.ChromeDirs)
		if err := cfg.viper.WriteConfig(); err != nil {
			return fmt.Errorf("failed to save configuration: %w", err)
		}
		if err := cfg.viper.Unmarshal(cfg); err != nil {
			return fmt.Errorf("unmarshalling config to struct : %w", err)
		}
	default:
		return errors.New("invalid os string")
	}
	return nil
}

func (cfg *Config) DeleteChromePath(path, os string) error {
	chromePath := chrome.PathConfig{OS: os, Path: path}
	cfg.ChromeDirs = slices.DeleteFunc(cfg.ChromeDirs, func(c chrome.PathConfig) bool {
		return c.OS == chromePath.OS && c.Path == chromePath.Path
	})
	cfg.viper.Set("chrome_dirs", cfg.ChromeDirs)
	if err := cfg.viper.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	if err := cfg.viper.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshalling config to struct : %w", err)
	}
	return nil
}

// getSPKIHash computes the SHA-256 hash of the certificate's Subject Public Key Info
// and returns it as a base64-encoded string.
//
// Parameters:
//   - cert: The X.509 certificate to hash
//
// Returns:
//   - string: Base64-encoded SPKI hash
func getSPKIHash(cert *x509.Certificate) string {
	// Compute SPKI hash (SHA-256)
	spkiHash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)

	// Encode hash to base64 for display
	spkiHashBase64 := base64.StdEncoding.EncodeToString(spkiHash[:])

	return spkiHashBase64
}
func saveCertAndKey(cert *x509.Certificate, priv interface{}, configDir string) error {
	certPath := path.Join(configDir, certFile)
	keyPath := path.Join(configDir, keyFile)
	certOut, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("failed to open cert file for writing: %w", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
		return fmt.Errorf("failed to write data to cert file: %w", err)
	}

	keyOut, err := os.Create(keyPath)
	if err != nil {
		return fmt.Errorf("failed to open key file for writing: %w", err)
	}
	defer keyOut.Close()
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("unable to marshal private key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		return fmt.Errorf("failed to write data to key file: %w", err)
	}

	return nil
}
func loadCertAndKey(configDir string) (*x509.Certificate, interface{}, error) {
	certPath := path.Join(configDir, certFile)
	keyPath := path.Join(configDir, keyFile)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read cert file: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("failed to decode cert PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read key file: %w", err)
	}
	block, _ = pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, nil, fmt.Errorf("failed to decode key PEM block")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	return cert, priv, nil
}
