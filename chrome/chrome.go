// Package chrome handles launching Chrome to be used with the Marasi proxy
//
// Launched chrome windows are configured with the following:
//   - Custom chrome profiles to not interfere with the default profile
//   - Configuring the proxy settings with the active address and port
//   - Bypass certificate errors for the configured certificate
//   - Load chrome binaries from custom paths
//   - Quiet Chrome's own background requests with switches and disabled
//     features, without bypassing or blocking any host (ADR-0056)
package chrome

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"slices"
	"strings"
)

// disabledFeatures are the Chrome features turned off in every started Chrome
// because they send background requests through the proxy. None of them
// changes how a target site behaves (ADR-0056). NetworkPrediction, Translate,
// DialMediaRouteProvider, CertificateTransparencyComponentUpdater and
// InterestFeedContentSuggestions are not declared as features at Chromium 155,
// so Chrome ignores them there.
var disabledFeatures = []string{
	"NetworkPrediction",                         // DNS prefetch and preconnect to predicted hosts
	"OmniboxUIExperimentMaxAutocompleteMatches", // omnibox autocomplete match-count experiment
	"AimEnabled",                                // AI Mode eligibility (www.google.com/async/folae)
	"OptimizationHints",                         // optimization guide downloads (optimizationguide-pa.googleapis.com)
	"NetworkTimeServiceQuerying",                // clock sync (clients2.google.com/time)
	"Translate",                                 // page translation and language-list fetches (translate.googleapis.com)
	"MediaRouter",                               // Cast media-device discovery
	"DialMediaRouteProvider",                    // DIAL media-device discovery on the local network
	"AutofillServerCommunication",               // autofill field-type queries (content-autofill.googleapis.com)
	"CertificateTransparencyComponentUpdater",   // Certificate Transparency log-list downloads
	"InterestFeedContentSuggestions",            // New Tab Page feed suggestions
}

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
	cdp         bool
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
// It returns the first executable path or the final operating-system lookup error.
func (l *Launcher) getChromePath() (string, error) {
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
		return "", fmt.Errorf("unsupported operating system %q", runtime.GOOS)
	}

	customPaths := make([]string, 0, len(l.customPaths))
	for _, pc := range l.customPaths {
		if pc.OS == runtime.GOOS {
			customPaths = append(customPaths, pc.Path)
		}
	}
	paths = append(customPaths, paths...)

	var lastErr error
	for _, p := range paths {
		if _, err := exec.LookPath(p); err == nil {
			return p, nil
		} else {
			lastErr = err
		}
	}
	return "", fmt.Errorf("finding Chrome executable: %w", lastErr)
}

// Start launches the Chrome instance with the configured flags and profile directory.
// It always passes the switches and disabled features that stop Chrome's own
// background requests without changing how a target site behaves. No host
// bypasses the proxy or is blocked: --proxy-bypass-list=<-loopback> removes
// Chrome's implicit loopback bypass, so loopback requests go through the
// proxy too (ADR-0056).
//
// Returns:
//   - error: An error if the configuration directory is missing, the executable is not found,
//     or the process fails to start.
func (l *Launcher) Start() error {
	if l.configDir == "" {
		return fmt.Errorf("configDir is required to start Chrome")
	}

	chromePath, err := l.getChromePath()
	if err != nil {
		return err
	}

	profileDir := l.ProfileDir()

	flags := []string{
		fmt.Sprintf("--user-data-dir=%s", profileDir),
		fmt.Sprintf("--proxy-server=http://%s", net.JoinHostPort(l.addr, l.port)),
		fmt.Sprintf("--ignore-certificate-errors-spki-list=%s", l.spkiHash),
		"--disable-background-networking",
		"--disable-default-apps",
		"--disable-component-extensions-with-background-pages",
		"--disable-features=" + strings.Join(disabledFeatures, ","),
		"--disable-sync",
		"--metrics-recording-only",
		"--disable-domain-reliability",
		"--disable-breakpad",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-component-update",
		"--disable-search-engine-choice-screen",
		"--proxy-bypass-list=<-loopback>",
		"--window-name=Marasi - " + l.profile,
		"about:blank",
	}
	if l.cdp {
		flags = slices.Insert(flags, len(flags)-1, "--remote-debugging-port=0")
	}

	cmd := exec.Command(chromePath, flags...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting chrome: %w", err)
	}

	return nil

}
