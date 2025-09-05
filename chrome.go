package marasi

import (
	"fmt"
	"os/exec"
	"path"
	"runtime"
)

// getChromePath determines the Chrome executable path based on the operating system.
// It checks common installation locations for Chrome and Chromium on macOS, Windows, and Linux.
//
// Returns:
//   - string: Path to Chrome executable, or empty string if not found
func getChromePath(customPaths []ChromePathConfig) string {
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		paths = []string{
			`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,
			`/Applications/Chromium.app/Contents/MacOS/Chromium`,
			`/usr/local/bin/chrome`,   // Alternative common symlink
			`/usr/local/bin/chromium`, // Alternative common symlink for Chromium
		}
		for _, path := range customPaths {
			if path.OS == "darwin" {
				paths = append(paths, path.Path)
			}
		}
	case "windows":
		paths = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\Chromium\Application\chrome.exe`,
		}
		for _, path := range customPaths {
			if path.OS == "windows" {
				paths = append(paths, path.Path)
			}
		}
	case "linux":
		paths = []string{
			`/usr/bin/google-chrome`,
			`/usr/bin/chromium-browser`,
			`/usr/bin/chromium`,
			`/snap/bin/chromium`,
		}
		for _, path := range customPaths {
			if path.OS == "linux" {
				paths = append(paths, path.Path)
			}
		}
	default:
		return ""
	}

	// Find the first valid path
	for _, path := range paths {
		if _, err := exec.LookPath(path); err == nil {
			return path
		}
	}
	return ""
}

// StartChrome launches Chrome with proxy configuration and security settings.
// It configures Chrome to use the proxy server, creates an isolated user profile,
// and disables various Chrome features that might interfere with testing.
//
// Returns:
//   - error: Chrome launch error if executable not found or process fails to start
func (proxy *Proxy) StartChrome() error {
	// Determine Chrome path based on OS
	chromePath := getChromePath(proxy.Config.ChromeDirs)
	if chromePath == "" {
		return fmt.Errorf("unsupported operating system")
	}

	// Set flags for Chrome
	flags := []string{
		fmt.Sprintf("--user-data-dir=%s", path.Join(proxy.ConfigDir, "chrome-profile")),
		fmt.Sprintf("--proxy-server=http://%s:%s", proxy.Addr, proxy.Port),
		fmt.Sprintf("--ignore-certificate-errors-spki-list=%s", proxy.SPKIHash),
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
		"--disable-omnibox-autocomplete-offers", // Disables autocomplete suggestions
		"--proxy-bypass-list=<-loopback>",
		"about:blank",
	}

	// Start Chrome process
	cmd := exec.Command(chromePath, flags...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting chrome : %w", err)
	}

	return nil
}
