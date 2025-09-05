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
	"slices"

	"github.com/spf13/viper"
)

type ChromePathConfig struct {
	OS   string `mapstructure:"os"`   // OS for the given path
	Path string `mapstructure:"path"` // Custom chrome path

}
type Config struct {
	viper      *viper.Viper
	ConfigDir  string             `mapstructure:"config_dir"` // Current config dir
	DesktopOS  string             `mapstructure:"desktop_os"` // Operating system identifier
	ChromeDirs []ChromePathConfig `mapstructure:"chrome_dirs"`
}

func (cfg *Config) AddChromePath(path, os string) error {
	switch os {
	case "darwin", "linux", "windows":
		cfg.ChromeDirs = append(cfg.ChromeDirs, ChromePathConfig{OS: os, Path: path})
		cfg.viper.Set("chrome_dirs", cfg.ChromeDirs)
		if err := cfg.viper.WriteConfig(); err != nil {
			return fmt.Errorf("failed to save configuration: %w", err)
		}
		if err := cfg.viper.Unmarshal(&cfg); err != nil {
			return fmt.Errorf("unmarshalling config to struct : %w", err)
		}
	default:
		return errors.New("invalid os string")
	}
	return nil
}

func (cfg *Config) DeleteChromePath(path, os string) error {
	chromePath := ChromePathConfig{OS: os, Path: path}
	cfg.ChromeDirs = slices.DeleteFunc(cfg.ChromeDirs, func(c ChromePathConfig) bool {
		return c.OS == chromePath.OS && c.Path == chromePath.Path
	})
	cfg.viper.Set("chrome_dirs", cfg.ChromeDirs)
	if err := cfg.viper.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	if err := cfg.viper.Unmarshal(&cfg); err != nil {
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
