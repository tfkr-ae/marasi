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
	Status  string `json:"status"`
	Profile string `json:"profile"`
}

const chromeLockPollDelay = 10 * time.Millisecond

var (
	ErrInvalidChromeRequest       = errors.New("invalid chrome request")
	ErrChromePathAlreadyExists    = errors.New("chrome path already exists")
	ErrChromeProfileAlreadyExists = errors.New("chrome profile already exists")
	ErrChromeNotFound             = errors.New("chrome item not found")
	ErrChromeUnavailable          = errors.New("chrome unavailable")
)

// Chrome owns machine Chrome configuration for one service instance.
type Chrome struct {
	proxy      *marasi.Proxy
	listener   interface{ Status() ListenerStatus }
	logWriter  io.Writer
	events     *eventBroadcaster
	operations chan struct{}
}

// NewChrome creates the Chrome configuration and launch module for a service instance.
func NewChrome(proxy *marasi.Proxy, listener interface{ Status() ListenerStatus }, logWriter io.Writer) *Chrome {
	if logWriter == nil {
		logWriter = io.Discard
	}
	operations := make(chan struct{}, 1)
	operations <- struct{}{}
	return &Chrome{proxy: proxy, listener: listener, logWriter: logWriter, operations: operations}
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
		profile, err := decodeChromeStart(r)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		profile, err = chrome.Start(r.Context(), profile)
		if err != nil {
			writeChromeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, chromeStartResponse{Status: "started", Profile: profile})
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

func decodeChromeStart(r *http.Request) (string, error) {
	fields, err := decodeChromeObject(r, true)
	if err != nil || len(fields) > 1 {
		return "", ErrInvalidChromeRequest
	}
	if len(fields) == 0 {
		return "", nil
	}
	var profile string
	if err := decodeChromeString(fields, "profile", &profile); err != nil || profile == "" {
		return "", ErrInvalidChromeRequest
	}
	return profile, nil
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

// Start spawns Chrome against the active proxy listener and returns the profile used.
func (c *Chrome) Start(ctx context.Context, profile string) (string, error) {
	startedProfile := profile
	err := c.withConfig(ctx, func() error {
		if startedProfile == "" {
			startedProfile = "default-profile"
		} else {
			name, err := validProfileName(startedProfile)
			if err != nil {
				return err
			}
			startedProfile = name
		}
		if startedProfile != "default-profile" {
			if !slices.Contains(c.proxy.Config.ChromeProfiles, startedProfile) {
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
		launcher := marasichrome.NewLauncher(
			marasichrome.WithProxy(address, port),
			marasichrome.WithSPKIHash(c.proxy.SPKIHash),
			marasichrome.WithConfigDir(c.proxy.ConfigDir),
			marasichrome.WithProfile(startedProfile),
			marasichrome.WithCustomPaths(c.proxy.Config.ChromeDirs),
		)
		if err := launcher.Start(); err != nil {
			fmt.Fprintf(c.logWriter, "starting Chrome: %v\n", err)
			return fmt.Errorf("%w: %v", ErrChromeUnavailable, err)
		}
		return nil
	})
	return startedProfile, err
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
	path := filepath.Join(c.proxy.ConfigDir, "marasi_config.yaml")
	lock, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening chrome config lock %s: %w", path, err)
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
			return nil, errors.Join(fmt.Errorf("locking chrome config %s: %w", path, err), lock.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), lock.Close())
		case <-ticker.C:
		}
	}
}
