package marasi

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/martian/mitm"
	"github.com/spf13/viper"
	"github.com/tfkr-ae/marasi/chrome"
	"github.com/tfkr-ae/marasi/internal/filelock"
)

type Config struct {
	viper          *viper.Viper
	ConfigDir      string              `mapstructure:"config_dir"` // Current config dir
	DesktopOS      string              `mapstructure:"desktop_os"` // Operating system identifier
	ChromeDirs     []chrome.PathConfig `mapstructure:"chrome_dirs"`
	ChromeProfiles []string            `mapstructure:"chrome_profiles"`
}

// AddChromeProfile adds a chrome profile to the configuration.
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

	profiles := append(slices.Clone(cfg.ChromeProfiles), profileName)
	cfg.viper.Set("chrome_profiles", profiles)

	if err := cfg.viper.WriteConfig(); err != nil {
		cfg.viper.Set("chrome_profiles", cfg.ChromeProfiles)
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	cfg.ChromeProfiles = profiles

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

	profiles := slices.DeleteFunc(slices.Clone(cfg.ChromeProfiles), func(profile string) bool {
		return profile == profileName
	})

	cfg.viper.Set("chrome_profiles", profiles)

	if err := cfg.viper.WriteConfig(); err != nil {
		cfg.viper.Set("chrome_profiles", cfg.ChromeProfiles)
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	cfg.ChromeProfiles = profiles

	return nil
}

func (cfg *Config) AddChromePath(path, os string) error {
	if path == "" {
		return errors.New("invalid chrome path: cannot be empty")
	}
	switch os {
	case "darwin", "linux", "windows":
		chromePath := chrome.PathConfig{OS: os, Path: path}
		if slices.Contains(cfg.ChromeDirs, chromePath) {
			return fmt.Errorf("chrome path for %s %q already exists", os, path)
		}
		paths := append(slices.Clone(cfg.ChromeDirs), chromePath)
		cfg.viper.Set("chrome_dirs", paths)
		if err := cfg.viper.WriteConfig(); err != nil {
			cfg.viper.Set("chrome_dirs", cfg.ChromeDirs)
			return fmt.Errorf("failed to save configuration: %w", err)
		}
		cfg.ChromeDirs = paths
	default:
		return errors.New("invalid os string")
	}
	return nil
}

func (cfg *Config) DeleteChromePath(path, os string) error {
	chromePath := chrome.PathConfig{OS: os, Path: path}
	if !slices.Contains(cfg.ChromeDirs, chromePath) {
		return fmt.Errorf("chrome path for %s %q does not exist", os, path)
	}
	paths := slices.DeleteFunc(slices.Clone(cfg.ChromeDirs), func(c chrome.PathConfig) bool {
		return c.OS == chromePath.OS && c.Path == chromePath.Path
	})
	cfg.viper.Set("chrome_dirs", paths)
	if err := cfg.viper.WriteConfig(); err != nil {
		cfg.viper.Set("chrome_dirs", cfg.ChromeDirs)
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	cfg.ChromeDirs = paths
	return nil
}

// ReloadChrome rereads machine Chrome configuration into cfg.
func (cfg *Config) ReloadChrome() error {
	config := viper.New()
	config.SetConfigFile(filepath.Join(cfg.ConfigDir, "marasi_config.yaml"))
	config.SetConfigType("yaml")
	config.SetDefault("chrome_dirs", []chrome.PathConfig{})
	config.SetDefault("chrome_profiles", []string{})
	if err := config.ReadInConfig(); err != nil {
		return fmt.Errorf("reading configuration: %w", err)
	}
	loaded := Config{viper: config, ConfigDir: cfg.ConfigDir, DesktopOS: cfg.DesktopOS}
	if err := config.Unmarshal(&loaded); err != nil {
		return fmt.Errorf("unmarshalling config to struct: %w", err)
	}
	if loaded.ChromeDirs == nil {
		loaded.ChromeDirs = []chrome.PathConfig{}
	}
	if loaded.ChromeProfiles == nil {
		loaded.ChromeProfiles = []string{}
	}
	*cfg = loaded
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

const certificateLockPollDelay = 10 * time.Millisecond

func initializeCertificateAuthority(ctx context.Context, configDir string, logger *slog.Logger) (certificate *x509.Certificate, privateKey any, resultErr error) {
	lock, err := acquireCertificateLock(ctx, configDir)
	if err != nil {
		return nil, nil, fmt.Errorf("acquiring certificate lock: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, wrapCertificateError("releasing certificate lock", releaseCertificateLock(lock)))
	}()

	certExists, err := fileExists(filepath.Join(configDir, certFile))
	if err != nil {
		return nil, nil, err
	}
	keyExists, err := fileExists(filepath.Join(configDir, keyFile))
	if err != nil {
		return nil, nil, err
	}
	if certExists && keyExists {
		logger.Info("Loading existing certificate")
		certificate, privateKey, err = loadCertAndKey(configDir)
		if err != nil {
			return nil, nil, fmt.Errorf("loading cert and key from disk: %w", err)
		}
		return certificate, privateKey, nil
	}

	logger.Info("Certificate does not exist, creating a new one")
	certificate, privateKey, err = mitm.NewAuthority("Marasi", "Marasi Authority", 365*3*24*time.Hour)
	if err != nil {
		return nil, nil, fmt.Errorf("creating new mitm authority: %w", err)
	}
	if err := saveCertAndKey(certificate, privateKey, configDir); err != nil {
		return nil, nil, fmt.Errorf("saving cert and key to disk: %w", err)
	}
	return certificate, privateKey, nil
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("checking certificate file %s: %w", path, err)
}

func acquireCertificateLock(ctx context.Context, configDir string) (*os.File, error) {
	lockPath := filepath.Join(configDir, "certificate.lock")
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening certificate lock %s: %w", lockPath, err)
	}

	ticker := time.NewTicker(certificateLockPollDelay)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), wrapCertificateError("closing certificate lock", lock.Close()))
		default:
		}

		if err := filelock.TryLock(lock); err == nil {
			return lock, nil
		} else if !filelock.IsUnavailable(err) {
			return nil, errors.Join(
				fmt.Errorf("locking certificate lock %s: %w", lockPath, err),
				wrapCertificateError("closing certificate lock", lock.Close()),
			)
		}

		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), wrapCertificateError("closing certificate lock", lock.Close()))
		case <-ticker.C:
		}
	}
}

func releaseCertificateLock(lock *os.File) error {
	return errors.Join(filelock.Unlock(lock), lock.Close())
}

func wrapCertificateError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

func saveCertAndKey(certificate *x509.Certificate, privateKey any, configDir string) error {
	privateKeyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("unable to marshal private key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyBytes})

	certTemp, err := writeCertificateTemp(configDir, ".marasi-cert-*.tmp", certPEM)
	if err != nil {
		return err
	}
	defer os.Remove(certTemp)
	keyTemp, err := writeCertificateTemp(configDir, ".marasi-key-*.tmp", keyPEM)
	if err != nil {
		return err
	}
	defer os.Remove(keyTemp)

	certPath := filepath.Join(configDir, certFile)
	keyPath := filepath.Join(configDir, keyFile)
	if err := removeCertificatePair(certPath, keyPath); err != nil {
		return err
	}
	if err := os.Rename(certTemp, certPath); err != nil {
		return fmt.Errorf("publishing certificate: %w", err)
	}
	if err := os.Rename(keyTemp, keyPath); err != nil {
		return fmt.Errorf("publishing private key: %w", err)
	}
	return nil
}

func writeCertificateTemp(configDir, pattern string, contents []byte) (_ string, resultErr error) {
	file, err := os.CreateTemp(configDir, pattern)
	if err != nil {
		return "", fmt.Errorf("creating temporary certificate file: %w", err)
	}
	tempPath := file.Name()
	defer func() {
		resultErr = errors.Join(resultErr, wrapCertificateError("closing temporary certificate file", file.Close()))
		if resultErr != nil {
			os.Remove(tempPath)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return "", fmt.Errorf("writing temporary certificate file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("syncing temporary certificate file: %w", err)
	}
	return tempPath, nil
}

func removeCertificatePair(paths ...string) error {
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing incomplete certificate file %s: %w", path, err)
		}
	}
	return nil
}

func loadCertAndKey(configDir string) (*x509.Certificate, interface{}, error) {
	certPath := filepath.Join(configDir, certFile)
	keyPath := filepath.Join(configDir, keyFile)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read cert file: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("failed to decode cert PEM block")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
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
	privateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse private key: %w", err)
	}
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("private key cannot sign certificates")
	}
	certPublicKey, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal certificate public key: %w", err)
	}
	privatePublicKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal private key public key: %w", err)
	}
	if !bytes.Equal(certPublicKey, privatePublicKey) {
		return nil, nil, errors.New("certificate and private key do not match")
	}

	return certificate, privateKey, nil
}
