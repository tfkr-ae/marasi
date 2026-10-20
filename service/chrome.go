package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tfkr-ae/marasi"
	marasichrome "github.com/tfkr-ae/marasi/chrome"
	"github.com/tfkr-ae/marasi/internal/filelock"
)

type chromePath struct {
	OS   string `json:"os"`
	Path string `json:"path"`
}

type chromePathList struct {
	Items []chromePath `json:"items"`
}

type chromeProfile struct {
	Name string `json:"name"`
}

type chromeProfileList struct {
	Items []chromeProfile `json:"items"`
}

type chromeStartResponse struct {
	Status  string       `json:"status"`
	Profile string       `json:"profile"`
	CDP     *CDPEndpoint `json:"cdp,omitempty"`
}

const (
	chromeLockPollDelay  = 10 * time.Millisecond
	chromeConfigLockFile = "marasi_config.lock"
	// chromeCDPWait bounds how long Start waits for a CDP endpoint to answer.
	chromeCDPWait      = 5 * time.Second
	chromeCDPPollDelay = 50 * time.Millisecond
)

var (
	ErrInvalidChromeRequest       = errors.New("invalid chrome request")
	ErrChromePathAlreadyExists    = errors.New("chrome path already exists")
	ErrChromeProfileAlreadyExists = errors.New("chrome profile already exists")
	ErrChromeNotFound             = errors.New("chrome item not found")
	ErrChromeUnavailable          = errors.New("chrome unavailable")
	ErrChromeRunningWithoutCDP    = errors.New("chrome profile running without CDP")
)

// Chrome owns machine Chrome configuration for one service instance.
type Chrome struct {
	proxy      *marasi.Proxy
	listener   interface{ Status() ListenerStatus }
	logWriter  io.Writer
	events     *eventBroadcaster
	operations chan struct{}
	cdpWait    time.Duration
}

// NewChrome creates the Chrome configuration and launch module for a service instance.
func NewChrome(proxy *marasi.Proxy, listener interface{ Status() ListenerStatus }, logWriter io.Writer) *Chrome {
	if logWriter == nil {
		logWriter = io.Discard
	}
	operations := make(chan struct{}, 1)
	operations <- struct{}{}
	return &Chrome{proxy: proxy, listener: listener, logWriter: logWriter, operations: operations, cdpWait: chromeCDPWait}
}

func addChromeRoutes(mux *http.ServeMux, chrome *Chrome) {
	mux.HandleFunc("GET /chrome/path", func(w http.ResponseWriter, r *http.Request) {
		paths, err := chrome.Paths(r.Context())
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, chromePaths(paths))
	})
	mux.HandleFunc("POST /chrome/path", func(w http.ResponseWriter, r *http.Request) {
		path, err := decodeChromePath(r)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		paths, err := chrome.AddPath(r.Context(), path)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		response := chromePaths(paths)
		writeJSON(w, r, http.StatusOK, response)
	})
	mux.HandleFunc("DELETE /chrome/path", func(w http.ResponseWriter, r *http.Request) {
		path, err := decodeChromePath(r)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		paths, err := chrome.RemovePath(r.Context(), path)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		response := chromePaths(paths)
		writeJSON(w, r, http.StatusOK, response)
	})
	mux.HandleFunc("GET /chrome/profile", func(w http.ResponseWriter, r *http.Request) {
		profiles, err := chrome.Profiles(r.Context())
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, chromeProfiles(profiles))
	})
	mux.HandleFunc("POST /chrome/profile", func(w http.ResponseWriter, r *http.Request) {
		name, err := decodeChromeProfile(r)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		profiles, err := chrome.AddProfile(r.Context(), name)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		response := chromeProfiles(profiles)
		writeJSON(w, r, http.StatusOK, response)
	})
	mux.HandleFunc("DELETE /chrome/profile/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !emptyRequestBody(r) {
			writeChromeError(w, r, ErrInvalidChromeRequest)
			return
		}
		profiles, err := chrome.RemoveProfile(r.Context(), r.PathValue("name"))
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		response := chromeProfiles(profiles)
		writeJSON(w, r, http.StatusOK, response)
	})
	mux.HandleFunc("POST /chrome/start", func(w http.ResponseWriter, r *http.Request) {
		profile, options, err := decodeChromeStart(r)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		result, err := chrome.Start(r.Context(), profile, options)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, chromeStartResponse{Status: "started", Profile: result.Profile, CDP: result.CDP})
	})
}

func decodeChromePath(r *http.Request) (marasichrome.PathConfig, error) {
	fields, err := decodeChromeObject(r, false)
	if err != nil || len(fields) != 2 {
		return marasichrome.PathConfig{}, ErrInvalidChromeRequest
	}
	var path marasichrome.PathConfig
	if err := decodeChromeString(fields, "os", &path.OS); err != nil {
		return marasichrome.PathConfig{}, err
	}
	if err := decodeChromeString(fields, "path", &path.Path); err != nil || !validChromePath(path) {
		return marasichrome.PathConfig{}, ErrInvalidChromeRequest
	}
	return path, nil
}

func decodeChromeProfile(r *http.Request) (string, error) {
	fields, err := decodeChromeObject(r, false)
	if err != nil || len(fields) != 1 {
		return "", ErrInvalidChromeRequest
	}
	var name string
	if err := decodeChromeString(fields, "name", &name); err != nil {
		return "", err
	}
	if _, err := validProfileName(name); err != nil {
		return "", err
	}
	return name, nil
}

func decodeChromeStart(r *http.Request) (string, ChromeStartOptions, error) {
	fields, err := decodeChromeObject(r, true)
	if err != nil {
		return "", ChromeStartOptions{}, ErrInvalidChromeRequest
	}
	known := 0
	var profile string
	if _, ok := fields["profile"]; ok {
		known++
		if err := decodeChromeString(fields, "profile", &profile); err != nil || profile == "" {
			return "", ChromeStartOptions{}, ErrInvalidChromeRequest
		}
	}
	var options ChromeStartOptions
	if raw, ok := fields["cdp"]; ok {
		known++
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &options.CDP) != nil {
			return "", ChromeStartOptions{}, ErrInvalidChromeRequest
		}
	}
	if known != len(fields) {
		return "", ChromeStartOptions{}, ErrInvalidChromeRequest
	}
	return profile, options, nil
}

func decodeChromeObject(r *http.Request, allowEmpty bool) (map[string]json.RawMessage, error) {
	if r.Body == nil {
		if allowEmpty {
			return map[string]json.RawMessage{}, nil
		}
		return nil, ErrInvalidChromeRequest
	}
	decoder := json.NewDecoder(r.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, ErrInvalidChromeRequest
	}
	if fields == nil {
		return nil, ErrInvalidChromeRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidChromeRequest
	}
	return fields, nil
}

func decodeChromeString(fields map[string]json.RawMessage, name string, target *string) error {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalidChromeRequest
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return ErrInvalidChromeRequest
	}
	return nil
}

func emptyRequestBody(r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	return err == nil && len(body) == 0
}

func chromePaths(paths []marasichrome.PathConfig) chromePathList {
	items := make([]chromePath, len(paths))
	for i, path := range paths {
		items[i] = chromePath{OS: path.OS, Path: path.Path}
	}
	return chromePathList{Items: items}
}

func chromeProfiles(profiles []string) chromeProfileList {
	items := make([]chromeProfile, len(profiles))
	for i, profile := range profiles {
		items[i] = chromeProfile{Name: profile}
	}
	return chromeProfileList{Items: items}
}

func writeChromeError(w http.ResponseWriter, r *http.Request, err error) {
	var withoutCDP *ChromeRunningWithoutCDPError
	if errors.As(err, &withoutCDP) {
		writeJSON(w, r, http.StatusConflict, struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}{Error: "chrome_running_without_cdp", Message: withoutCDP.Error()})
		return
	}
	status := http.StatusInternalServerError
	code := "internal_server_error"
	switch {
	case errors.Is(err, ErrInvalidChromeRequest):
		status, code = http.StatusBadRequest, "invalid_chrome_request"
	case errors.Is(err, ErrChromeNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrChromePathAlreadyExists):
		status, code = http.StatusConflict, "path_already_exists"
	case errors.Is(err, ErrChromeProfileAlreadyExists):
		status, code = http.StatusConflict, "profile_already_exists"
	case errors.Is(err, ErrListenerInactive):
		status, code = http.StatusConflict, "listener_inactive"
	case errors.Is(err, ErrChromeUnavailable):
		status, code = http.StatusConflict, "chrome_unavailable"
	}
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

// AddProfile registers a Chrome profile without creating its user-data directory.
func (c *Chrome) AddProfile(ctx context.Context, name string) ([]string, error) {
	var profiles []string
	err := c.withConfig(ctx, func() error {
		profile, err := validProfileName(name)
		if err != nil {
			return err
		}
		if slices.Contains(c.proxy.Config.ChromeProfiles, profile) {
			return ErrChromeProfileAlreadyExists
		}
		if err := c.proxy.Config.AddChromeProfile(profile); err != nil {
			return err
		}
		profiles = slices.Clone(c.proxy.Config.ChromeProfiles)
		if c.events != nil {
			c.events.publish("chrome.profile.added", chromeProfiles(profiles))
		}
		return nil
	})
	return profiles, err
}

// Profiles returns configured Chrome profile names in YAML order.
func (c *Chrome) Profiles(ctx context.Context) ([]string, error) {
	var profiles []string
	err := c.withConfig(ctx, func() error {
		profiles = slices.Clone(c.proxy.Config.ChromeProfiles)
		return nil
	})
	return profiles, err
}

// RemoveProfile unregisters a Chrome profile and removes its user-data directory.
func (c *Chrome) RemoveProfile(ctx context.Context, name string) ([]string, error) {
	var profiles []string
	err := c.withConfig(ctx, func() error {
		profile, err := validProfileName(name)
		if err != nil {
			return err
		}
		if !slices.Contains(c.proxy.Config.ChromeProfiles, profile) {
			return ErrChromeNotFound
		}
		if err := c.proxy.Config.DeleteChromeProfile(profile); err != nil {
			return err
		}
		profiles = slices.Clone(c.proxy.Config.ChromeProfiles)
		if c.events != nil {
			c.events.publish("chrome.profile.removed", chromeProfiles(profiles))
		}
		return nil
	})
	return profiles, err
}

// Paths returns configured Chrome executable paths in YAML order.
func (c *Chrome) Paths(ctx context.Context) ([]marasichrome.PathConfig, error) {
	var paths []marasichrome.PathConfig
	err := c.withConfig(ctx, func() error {
		paths = slices.Clone(c.proxy.Config.ChromeDirs)
		return nil
	})
	return paths, err
}

// AddPath adds a Chrome executable path.
func (c *Chrome) AddPath(ctx context.Context, path marasichrome.PathConfig) ([]marasichrome.PathConfig, error) {
	var paths []marasichrome.PathConfig
	err := c.withConfig(ctx, func() error {
		if !validChromePath(path) {
			return ErrInvalidChromeRequest
		}
		if slices.Contains(c.proxy.Config.ChromeDirs, path) {
			return ErrChromePathAlreadyExists
		}
		if err := c.proxy.Config.AddChromePath(path.Path, path.OS); err != nil {
			return err
		}
		paths = slices.Clone(c.proxy.Config.ChromeDirs)
		if c.events != nil {
			c.events.publish("chrome.path.added", chromePaths(paths))
		}
		return nil
	})
	return paths, err
}

// RemovePath removes a configured Chrome executable path.
func (c *Chrome) RemovePath(ctx context.Context, path marasichrome.PathConfig) ([]marasichrome.PathConfig, error) {
	var paths []marasichrome.PathConfig
	err := c.withConfig(ctx, func() error {
		if !validChromePath(path) {
			return ErrInvalidChromeRequest
		}
		if !slices.Contains(c.proxy.Config.ChromeDirs, path) {
			return ErrChromeNotFound
		}
		if err := c.proxy.Config.DeleteChromePath(path.Path, path.OS); err != nil {
			return err
		}
		paths = slices.Clone(c.proxy.Config.ChromeDirs)
		if c.events != nil {
			c.events.publish("chrome.path.removed", chromePaths(paths))
		}
		return nil
	})
	return paths, err
}

// ChromeStartOptions selects how Chrome starts.
type ChromeStartOptions struct {
	// CDP starts the chrome profile with a CDP endpoint and reports it.
	CDP bool
}

// ChromeStartResult reports the chrome profile started and, only when CDP was
// requested, its CDP endpoint.
type ChromeStartResult struct {
	Profile string
	CDP     *CDPEndpoint
}

// CDPEndpoint is the CDP endpoint of a started chrome profile. Reused reports
// that the endpoint belongs to a Chrome that was already running.
type CDPEndpoint struct {
	HTTP   string `json:"http"`
	WS     string `json:"ws"`
	Reused bool   `json:"reused"`
}

// ChromeRunningWithoutCDPError reports that no CDP endpoint appeared after
// starting a chrome profile with CDP: Chrome handed off to a running chrome
// profile that has none, or did not start.
type ChromeRunningWithoutCDPError struct {
	Profile string
}

func (e *ChromeRunningWithoutCDPError) Error() string {
	return fmt.Sprintf("chrome profile %s is running without a CDP endpoint; close it and start it again with --cdp", e.Profile)
}

func (e *ChromeRunningWithoutCDPError) Is(target error) bool {
	return target == ErrChromeRunningWithoutCDP
}

// Start spawns Chrome against the active proxy listener and reports the
// profile used. With CDP requested it waits for the chrome profile's CDP
// endpoint to answer and reports it too.
func (c *Chrome) Start(ctx context.Context, profile string, options ChromeStartOptions) (ChromeStartResult, error) {
	result := ChromeStartResult{Profile: profile}
	// started is the launcher of a Chrome started with CDP whose endpoint is
	// still to be awaited.
	var started *marasichrome.Launcher
	err := c.withConfig(ctx, func() error {
		if result.Profile == "" {
			result.Profile = "default-profile"
		} else {
			name, err := validProfileName(result.Profile)
			if err != nil {
				return err
			}
			result.Profile = name
		}
		if result.Profile != "default-profile" {
			if !slices.Contains(c.proxy.Config.ChromeProfiles, result.Profile) {
				return ErrChromeNotFound
			}
		}

		status := c.listener.Status()
		if status.Status != ListenerActive || status.ProxyListener == nil {
			return ErrListenerInactive
		}
		address, port, err := net.SplitHostPort(*status.ProxyListener)
		if err != nil {
			return fmt.Errorf("reading active proxy listener endpoint: %w", err)
		}
		launcherOptions := []marasichrome.Option{
			marasichrome.WithProxy(address, port),
			marasichrome.WithSPKIHash(c.proxy.SPKIHash),
			marasichrome.WithConfigDir(c.proxy.ConfigDir),
			marasichrome.WithProfile(result.Profile),
			marasichrome.WithCustomPaths(c.proxy.Config.ChromeDirs),
		}
		launcher := marasichrome.NewLauncher(launcherOptions...)
		if !options.CDP {
			return c.launch(launcher)
		}
		// A chrome profile running with a live CDP endpoint ignores the CDP
		// switch, so start it without one to open a window and reuse the endpoint.
		if endpoint, ok := launcher.LiveCDPEndpoint(ctx); ok {
			if err := c.launch(launcher); err != nil {
				return err
			}
			result.CDP = &CDPEndpoint{HTTP: endpoint.HTTP, WS: endpoint.WS, Reused: true}
			return nil
		}
		if err := launcher.RemoveDevToolsActivePort(); err != nil {
			return err
		}
		launcher = marasichrome.NewLauncher(append(launcherOptions, marasichrome.WithCDP(true))...)
		if err := c.launch(launcher); err != nil {
			return err
		}
		started = launcher
		return nil
	})
	if err != nil || started == nil {
		return result, err
	}
	// Wait for the endpoint only after the config lock is released, so other
	// chrome operations do not queue behind Chrome's startup (ADR-0013).
	endpoint, err := c.waitForCDPEndpoint(ctx, started, result.Profile)
	if err != nil {
		return result, err
	}
	result.CDP = &CDPEndpoint{HTTP: endpoint.HTTP, WS: endpoint.WS}
	return result, nil
}

// launch starts Chrome and logs why it could not.
func (c *Chrome) launch(launcher *marasichrome.Launcher) error {
	if err := launcher.Start(); err != nil {
		fmt.Fprintf(c.logWriter, "starting Chrome: %v\n", err)
		return fmt.Errorf("%w: %v", ErrChromeUnavailable, err)
	}
	return nil
}

// waitForCDPEndpoint polls until the started chrome profile's CDP endpoint
// answers. When none answers within cdpWait, Chrome handed off to a chrome
// profile already running without CDP, or did not start.
func (c *Chrome) waitForCDPEndpoint(ctx context.Context, launcher *marasichrome.Launcher, profile string) (marasichrome.CDPEndpoint, error) {
	deadline := time.Now().Add(c.cdpWait)
	ticker := time.NewTicker(chromeCDPPollDelay)
	defer ticker.Stop()
	for {
		if endpoint, ok := launcher.LiveCDPEndpoint(ctx); ok {
			return endpoint, nil
		}
		if !time.Now().Before(deadline) {
			return marasichrome.CDPEndpoint{}, &ChromeRunningWithoutCDPError{Profile: profile}
		}
		select {
		case <-ctx.Done():
			return marasichrome.CDPEndpoint{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func validProfileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return "", ErrInvalidChromeRequest
	}
	return name, nil
}

func validChromePath(path marasichrome.PathConfig) bool {
	if path.Path == "" {
		return false
	}
	switch path.OS {
	case "darwin", "linux", "windows":
		return true
	default:
		return false
	}
}

func (c *Chrome) withConfig(ctx context.Context, operation func() error) (resultErr error) {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.operations:
	}
	defer func() { c.operations <- struct{}{} }()

	lock, err := c.acquireConfigLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, filelock.Unlock(lock), lock.Close())
	}()

	if err := c.proxy.Config.ReloadChrome(); err != nil {
		return err
	}
	return operation()
}

func (c *Chrome) acquireConfigLock(ctx context.Context) (*os.File, error) {
	return acquireConfigLock(ctx, c.proxy.ConfigDir)
}

// acquireConfigLock takes the lock every instance holds while it rereads
// and writes marasi_config.yaml (ADR-0013).
func acquireConfigLock(ctx context.Context, configDir string) (*os.File, error) {
	return acquireFileLock(ctx, filepath.Join(configDir, chromeConfigLockFile), "chrome config")
}

// acquireFileLock polls for an exclusive lock on path until ctx is done.
func acquireFileLock(ctx context.Context, path, name string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening %s lock %s: %w", name, path, err)
	}
	ticker := time.NewTicker(chromeLockPollDelay)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, lock.Close())
		}
		if err := filelock.TryLock(lock); err == nil {
			return lock, nil
		} else if !filelock.IsUnavailable(err) {
			return nil, errors.Join(fmt.Errorf("locking %s %s: %w", name, path, err), lock.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), lock.Close())
		case <-ticker.C:
		}
	}
}
