// Package chrome handles launching Chrome to be used with the Marasi proxy
//
// Launched chrome windows are configured with the following:
//   - Custom chrome profiles to not interfere with the default profile
//   - Configuring the proxy settings with the active address and port
//   - Bypass certificate errors for the configured certificate
//   - Load chrome binaries from custom paths
package chrome

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

type PathConfig struct {
	OS   string `mapstructure:"os"`   // OS for the given path
	Path string `mapstructure:"path"` // Custom chrome path
}

// Launcher manages the parameter state required to construct the browser runtime process.
type Launcher struct {
	addr        string
	port        string
	spkiHash    string
	configDir   string
	profile     string
	customPaths []PathConfig
}

type Option func(*Launcher)

// NewLauncher creates a new Launcher instance with default configurations and applies any provided options.
// It initializes the launcher with localhost proxy settings and a default profile name.
//
// Parameters:
//   - options: Variadic list of option functions to configure the launcher
//
// Returns:
//   - *Launcher: Configured launcher instance
func NewLauncher(options ...Option) *Launcher {
	launcher := &Launcher{
		addr:    "127.0.0.1",
		port:    "8080",
		profile: "default-profile",
	}

	for _, option := range options {
		option(launcher)
	}

	return launcher
}

// WithProxy sets the proxy address and port for the Chrome instance.
func WithProxy(addr, port string) Option {
	return func(l *Launcher) {
		l.addr = addr
		l.port = port
	}
}

// WithSPKIHash sets the SPKI hash to be ignored for certificate errors.
// This is required to allow the browser to trust the proxy's MITM certificates.
func WithSPKIHash(hash string) Option {
	return func(l *Launcher) {
		l.spkiHash = hash
	}
}

// WithConfigDir sets the directory where the Chrome user data profile will be stored.
func WithConfigDir(dir string) Option {
	return func(l *Launcher) {
		l.configDir = dir
	}
}

// WithProfile sets the profile name for the Chrome instance.
// If the provided name is empty, it maintains the default profile name.
func WithProfile(name string) Option {
	return func(l *Launcher) {
		if name != "" {
			l.profile = name
		}
	}
}

// WithCustomPaths sets custom executable paths for Chrome to override or append to default OS paths.
func WithCustomPaths(paths []PathConfig) Option {
	return func(l *Launcher) {
		l.customPaths = paths
	}
}

// getChromePath determines the Chrome executable path based on the operating system.
// It checks custom paths first, then falls back to common installation locations
// for Chrome and Chromium on macOS, Windows, and Linux.
//
// Returns:
//   - string: Path to Chrome executable, or empty string if not found
func (l *Launcher) getChromePath() string {
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		paths = []string{
			`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,
			`/Applications/Chromium.app/Contents/MacOS/Chromium`,
			`/usr/local/bin/chrome`,
			`/usr/local/bin/chromium`,
		}
	case "windows":
		paths = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\Chromium\Application\chrome.exe`,
		}
	case "linux":
		paths = []string{
			`/usr/bin/google-chrome`,
			`/usr/bin/chromium-browser`,
			`/usr/bin/chromium`,
			`/snap/bin/chromium`,
		}
	default:
		return ""
	}

	for _, pc := range l.customPaths {
		if pc.OS == runtime.GOOS {
			paths = append([]string{pc.Path}, paths...)
		}
	}

	for _, p := range paths {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}

// Start launches the Chrome instance with the configured flags and profile directory.
// It disables numerous background networking, syncing, and updating features to provide
// a clean, isolated testing environment that routes through the target proxy.
//
// Returns:
//   - error: An error if the configuration directory is missing, the executable is not found,
//     or the process fails to start.
func (l *Launcher) Start() error {
	if l.configDir == "" {
		return fmt.Errorf("configDir is required to start Chrome")
	}

	chromePath := l.getChromePath()
	if chromePath == "" {
		return fmt.Errorf("unsupported operating system or chrome executable was not found")
	}

	profileDir := filepath.Join(l.configDir, "chrome_profiles", l.profile)

	flags := []string{
		fmt.Sprintf("--user-data-dir=%s", profileDir),
		fmt.Sprintf("--proxy-server=http://%s:%s", l.addr, l.port),
		fmt.Sprintf("--ignore-certificate-errors-spki-list=%s", l.spkiHash),
		"--disable-background-networking",
		"--disable-client-side-phishing-detection",
		"--disable-default-apps",
		"--disable-features=NetworkPrediction,OmniboxUIExperimentMaxAutocompleteMatches",
		"--disable-sync",
		"--metrics-recording-only",
		"--disable-domain-reliability",
		"--no-first-run",
		"--disable-component-update",
		"--disable-suggestions-service",
		"--disable-search-geolocation-disclosure",
		"--disable-search-engine-choice",
		"--disable-omnibox-autocomplete-offers",
		"--proxy-bypass-list=<-loopback>",
		"--window-name=Marasi - " + l.profile,
		"about:blank",
	}

	cmd := exec.Command(chromePath, flags...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting chrome: %w", err)
	}

	return nil

}
