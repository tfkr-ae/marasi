package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/service"
	"github.com/tfkr-ae/marasi/wordlist"
)

var projectName string

var errInstanceLockHeld = errors.New("instance lock held")

const (
	unixSocketPathLimit = 104
	shutdownTimeout     = 5 * time.Second
	instancePollDelay   = 10 * time.Millisecond
	maxErrorBodySize    = 4 * 1024
)

func init() {
	startCmd.Flags().StringVar(&projectName, "project", "scratchpad", "Project name")
	serviceCmd.AddCommand(startCmd, stopCmd)
	rootCmd.AddCommand(serviceCmd)
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage marasi services",
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a marasi instance",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return startService(ctx, configDir, projectName, instance)
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop a marasi instance",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return stopService(ctx, configDir, instance)
	},
}

func startService(ctx context.Context, configDir, projectName, instanceName string) (resultErr error) {
	if configDir == "" {
		return fmt.Errorf("config dir is empty")
	}

	path, err := projectPath(configDir, projectName)
	if err != nil {
		return err
	}

	socketPath, lockPath, err := resolveInstancePaths(configDir, instanceName)
	if err != nil {
		return err
	}

	proxy, err := marasi.New(marasi.WithConfigDir(configDir))
	if err != nil {
		return fmt.Errorf("starting proxy with config dir: %w", err)
	}

	wordlists, err := wordlist.NewManager(configDir)
	if err != nil {
		return fmt.Errorf("creating wordlist manager : %w", err)
	}

	unlock, err := lockProject(path)
	if err != nil {
		return fmt.Errorf("locking project: %w", err)
	}
	closeProxy := func() error { return nil }
	releaseClaim := func() error { return nil }
	defer func() {
		resultErr = errors.Join(resultErr, cleanupService(closeProxy, unlock, releaseClaim))
	}()

	dbConn, err := db.New(path, proxy.Logger)
	if err != nil {
		return fmt.Errorf("opening project: %w", err)
	}

	repo := db.NewProxyRepo(dbConn)

	err = proxy.WithOptions(
		marasi.WithWordlistManager(wordlists),
		marasi.WithDefaultRepositories(repo),
		marasi.WithBasePipeline(),
		marasi.WithDefaultModifierPipeline(),
	)
	if err != nil {
		repo.Close()
		return fmt.Errorf("starting proxy base options: %w", err)
	}
	closeProxy = proxy.Close

	listener, instanceLock, err := claimInstance(socketPath, lockPath)
	if err != nil {
		return err
	}
	releaseClaim = func() error {
		return releaseInstance(listener, socketPath, instanceLock)
	}

	serviceCtx, stopService := context.WithCancel(ctx)
	defer stopService()
	server := &http.Server{Handler: service.NewServer(proxy, stopService)}
	return serveControlAPI(serviceCtx, server, listener, shutdownTimeout)
}

func stopService(ctx context.Context, configDir, instanceName string) error {
	if configDir == "" {
		return fmt.Errorf("config dir is empty")
	}

	socketPath, lockPath, err := resolveInstancePaths(configDir, instanceName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(socketPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("checking instance socket %s: %w", socketPath, err)
	}

	client := service.NewClient(socketPath)
	defer client.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://marasi/service/stop", nil)
	if err != nil {
		return fmt.Errorf("creating service stop request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("dialing instance socket %s: %w", socketPath, err)
		}
		return waitForInstanceStop(ctx, lockPath)
	}

	if response.StatusCode != http.StatusAccepted {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodySize))
		closeErr := response.Body.Close()
		responseErr := fmt.Errorf("stopping service: %s: %s", response.Status, body)
		return errors.Join(
			responseErr,
			wrapError("reading service stop response", readErr),
			wrapError("closing service stop response", closeErr),
		)
	}

	closeErr := response.Body.Close()
	waitErr := waitForInstanceStop(ctx, lockPath)
	return errors.Join(
		wrapError("closing service stop response", closeErr),
		waitErr,
	)
}

func waitForInstanceStop(ctx context.Context, lockPath string) error {
	ticker := time.NewTicker(instancePollDelay)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		lock, err := acquireInstanceLock(lockPath)
		if err == nil {
			return errors.Join(
				wrapError("unlocking instance probe", unlockFile(lock)),
				wrapError("closing instance probe", lock.Close()),
			)
		}
		if !errors.Is(err, errInstanceLockHeld) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func cleanupService(closeProxy, unlockProject, releaseInstance func() error) error {
	proxyErr := wrapError("closing proxy", closeProxy())
	projectErr := unlockProject()
	instanceErr := releaseInstance()
	return errors.Join(
		proxyErr,
		projectErr,
		instanceErr,
	)
}

func serveControlAPI(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration) error {
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()

		var closeErr error
		if shutdownErr != nil {
			closeErr = server.Close()
		}
		serveErr := <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}

		return errors.Join(
			wrapError("shutting down control server", shutdownErr),
			wrapError("force closing control server", closeErr),
			wrapError("serving control API", serveErr),
		)
	case err := <-serveResult:
		return errors.Join(
			wrapError("serving control API", err),
			wrapError("closing control server", server.Close()),
		)
	}
}

func wrapError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

func projectPath(configDir, name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".marasi")
	if name == "" {
		return "", errors.New("invalid project name: cannot be empty")
	}
	if !filepath.IsLocal(name) {
		return "", errors.New("invalid project name: absolute paths and parent directory references are not allowed")
	}
	if name == "." || strings.ContainsAny(name, `/\`) {
		return "", errors.New("invalid project name: path separators are not allowed")
	}
	if filepath.Base(name) != name {
		return "", errors.New("invalid project name: subdirectories not allowed")
	}

	return filepath.Join(configDir, "projects", name+".marasi"), nil
}

func resolveInstancePaths(configDir, name string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", errors.New("invalid instance name: cannot be empty")
	}
	if name == "." || name == ".." || !filepath.IsLocal(name) {
		return "", "", errors.New("invalid instance name: paths and parent directory references are not allowed")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", "", errors.New("invalid instance name: path separators are not allowed")
	}
	if strings.HasSuffix(name, ".sock") {
		return "", "", errors.New("invalid instance name: .sock suffix is not allowed")
	}

	dir := filepath.Join(configDir, "instances")
	socketPath := filepath.Join(dir, name+".sock")
	lockPath := filepath.Join(dir, name+".lock")
	if len([]byte(socketPath))+1 > unixSocketPathLimit {
		return "", "", fmt.Errorf("instance socket path %q exceeds %d-byte limit", socketPath, unixSocketPathLimit)
	}

	return socketPath, lockPath, nil
}

func claimInstance(socketPath, lockPath string) (net.Listener, *os.File, error) {
	if err := prepareInstancesDir(filepath.Dir(socketPath)); err != nil {
		return nil, nil, err
	}
	lock, err := acquireInstanceLock(lockPath)
	if err != nil {
		return nil, nil, err
	}
	listener, err := listenOnInstanceSocket(socketPath)
	if err != nil {
		unlockFile(lock)
		lock.Close()
		return nil, nil, err
	}

	return listener, lock, nil
}

func prepareInstancesDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("creating instances directory: %w", err)
	}
	if err := secureInstancesDir(path); err != nil {
		return fmt.Errorf("securing instances directory: %w", err)
	}
	return nil
}

func acquireInstanceLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening instance lock %s: %w", path, err)
	}
	if err := secureInstanceFile(path); err != nil {
		lock.Close()
		return nil, fmt.Errorf("securing instance lock %s: %w", path, err)
	}
	if err := lockFile(lock); err != nil {
		lock.Close()
		if isLockUnavailable(err) {
			return nil, fmt.Errorf("instance already running: %w: %v", errInstanceLockHeld, err)
		}
		return nil, fmt.Errorf("locking instance %s: %w", path, err)
	}
	return lock, nil
}

func listenOnInstanceSocket(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("removing stale instance socket %s: %w", path, err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on instance socket %s: %w", path, err)
	}
	if err := secureInstanceFile(path); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, fmt.Errorf("securing instance socket %s: %w", path, err)
	}
	return listener, nil
}

func releaseInstance(listener net.Listener, socketPath string, lock *os.File) error {
	closeListenerErr := listener.Close()
	if errors.Is(closeListenerErr, net.ErrClosed) {
		closeListenerErr = nil
	}
	removeSocketErr := os.Remove(socketPath)
	if errors.Is(removeSocketErr, os.ErrNotExist) {
		removeSocketErr = nil
	}

	return errors.Join(
		wrapError("closing instance listener", closeListenerErr),
		wrapError("removing instance socket", removeSocketErr),
		wrapError("unlocking instance", unlockFile(lock)),
		wrapError("closing instance lock", lock.Close()),
	)
}

func lockProject(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating projects dir: %w", err)
	}

	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening project lock %s: %w", lockPath, err)
	}

	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("project already open: %w", err)
	}

	return func() error {
		unlockErr := unlockFile(f)
		closeErr := f.Close()
		return errors.Join(
			wrapError("unlocking project", unlockErr),
			wrapError("closing project lock", closeErr),
		)
	}, nil
}
