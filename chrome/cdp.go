package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// devToolsActivePortFile is the file Chrome writes into its user-data-dir
// when it listens for CDP: line 1 is the port, line 2 the browser path.
const devToolsActivePortFile = "DevToolsActivePort"

// cdpProbeTimeout bounds one /json/version request to a CDP endpoint.
const cdpProbeTimeout = time.Second

// cdpVersionLimit bounds how many bytes of a /json/version response are read.
const cdpVersionLimit = 1 << 16

// CDPEndpoint is the CDP endpoint of a running chrome profile.
type CDPEndpoint struct {
	HTTP string // http://127.0.0.1:<port>
	WS   string // ws://127.0.0.1:<port>/devtools/browser/<id>
}

// WithCDP starts Chrome with a CDP endpoint on a port Chrome picks.
func WithCDP(enabled bool) Option {
	return func(l *Launcher) {
		l.cdp = enabled
	}
}

// ProfileDir returns the chrome profile's user-data-dir.
func (l *Launcher) ProfileDir() string {
	return filepath.Join(l.configDir, "chrome_profiles", l.profile)
}

// LiveCDPEndpoint returns the chrome profile's CDP endpoint when its
// DevToolsActivePort file points at an endpoint that answers. The endpoint is
// live only when /json/version answers and its webSocketDebuggerUrl ends with
// the file's browser path. Any other result means there is no live endpoint
// and any DevToolsActivePort left behind is stale.
func (l *Launcher) LiveCDPEndpoint(ctx context.Context) (CDPEndpoint, bool) {
	content, err := os.ReadFile(filepath.Join(l.ProfileDir(), devToolsActivePortFile))
	if err != nil {
		return CDPEndpoint{}, false
	}
	port, browserPath, ok := parseDevToolsActivePort(string(content))
	if !ok {
		return CDPEndpoint{}, false
	}
	host := net.JoinHostPort("127.0.0.1", port)
	ctx, cancel := context.WithTimeout(ctx, cdpProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/json/version", nil)
	if err != nil {
		return CDPEndpoint{}, false
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return CDPEndpoint{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return CDPEndpoint{}, false
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, cdpVersionLimit)).Decode(&version); err != nil {
		return CDPEndpoint{}, false
	}
	if !strings.HasSuffix(version.WebSocketDebuggerURL, browserPath) {
		return CDPEndpoint{}, false
	}
	return CDPEndpoint{HTTP: "http://" + host, WS: "ws://" + host + browserPath}, true
}

// RemoveDevToolsActivePort deletes the chrome profile's DevToolsActivePort
// file so that only a file the next Chrome writes is read.
func (l *Launcher) RemoveDevToolsActivePort() error {
	err := os.Remove(filepath.Join(l.ProfileDir(), devToolsActivePortFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", devToolsActivePortFile, err)
	}
	return nil
}

func parseDevToolsActivePort(content string) (port, browserPath string, ok bool) {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) < 2 {
		return "", "", false
	}
	port = strings.TrimSpace(lines[0])
	browserPath = strings.TrimSpace(lines[1])
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return "", "", false
	}
	if !strings.HasPrefix(browserPath, "/devtools/browser/") {
		return "", "", false
	}
	return port, browserPath, true
}
