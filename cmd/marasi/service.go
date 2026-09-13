package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/internal/filelock"
	"github.com/tfkr-ae/marasi/service"
	"github.com/tfkr-ae/marasi/wordlist"
)

var projectName string
var projectPath string
var proxyAddress string
var proxyPort decimalPort

// decimalPort is a pflag.Value for a TCP port parsed in base 10.
type decimalPort uint16

// proxyServer is the Serve subset used by serveProxy.
type proxyServer interface {
	Serve(net.Listener) error
}

// startupListener signals when Accept is first called so callers can wait until Serve has started.
type startupListener struct {
	net.Listener
	started chan struct{} // closed on the first Accept
	once    sync.Once     // closes started once
}

// Accept records that Serve has started, then accepts the next connection.
func (listener *startupListener) Accept() (net.Conn, error) {
	listener.once.Do(func() { close(listener.started) })
	return listener.Listener.Accept()
}

// Set parses value as a base-10 TCP port.
func (port *decimalPort) Set(value string) error {
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return err
	}
	*port = decimalPort(parsed)
	return nil
}

// String returns the port in base 10.
func (port *decimalPort) String() string {
	return strconv.FormatUint(uint64(*port), 10)
}

// Type returns the flag type name "port".
func (*decimalPort) Type() string {
	return "port"
}

var errInstanceLockHeld = errors.New("instance lock held")

const (
	shutdownTimeout   = 5 * time.Second
	instancePollDelay = 10 * time.Millisecond
	maxErrorBodySize  = 4 * 1024
)

func init() {
	startCmd.Flags().StringVar(&projectName, "project", "scratchpad", "Project name")
	startCmd.Flags().StringVar(&proxyAddress, "address", "127.0.0.1", "Proxy listener address")
	proxyPort = 8080
	startCmd.Flags().Var(&proxyPort, "port", "Proxy listener port")
	serviceCmd.AddCommand(startCmd, stopCmd)
	rootCmd.AddCommand(serviceCmd)
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage marasi services",
}

var startCmd = &cobra.Command{
	Use:     "start",
	Short:   "Start a marasi instance",
	PreRunE: prepareProjectPath,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		if serviceChild {
			return runServiceChild(ctx, configDir, projectPath, instancePath, proxyAddress, uint16(proxyPort))
		}
		return startServiceProcess(ctx, configDir, projectName, instancePath, proxyAddress, uint16(proxyPort), cmd.OutOrStdout(), cmd.ErrOrStderr(), jsonOutput)
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop a marasi instance",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		if err := stopService(ctx, instancePath); err != nil {
			return err
		}
		instanceName := filepath.Base(instancePath)
		if jsonOutput {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Instance string `json:"instance"`
				Status   string `json:"status"`
			}{Instance: instanceName, Status: "stopped"}); err != nil {
				return fmt.Errorf("writing service stop result: %w", err)
			}
			return nil
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "instance %s stopped successfully\n", instanceName); err != nil {
			return fmt.Errorf("writing service stop result: %w", err)
		}
		return nil
	},
}

// startService starts a proxy instance and writes the listener address to stderr when ready.
func startService(ctx context.Context, configDir, projectPath, instancePath, address string, port uint16, stderr io.Writer) (resultErr error) {
	return startServiceReady(ctx, configDir, projectPath, instancePath, address, port, func(proxyListener string) error {
		_, err := fmt.Fprintf(stderr, "proxy listener started on %s\n", proxyListener)
		return err
	})
}

// startServiceReady starts a proxy instance and calls ready with the proxy listener address.
func startServiceReady(ctx context.Context, configDir, projectPath, instancePath, address string, port uint16, ready func(string) error) (resultErr error) {
	socketPath := instancePath + ".sock"
	lockPath := instancePath + ".lock"
	logFile, err := openInstanceLog(instancePath + ".log")
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, wrapError("closing instance log", logFile.Close()))
	}()
	logger := slog.New(slog.NewTextHandler(logFile, nil))

	proxy, err := marasi.New(marasi.WithLogger(logger), marasi.WithConfigDir(configDir))
	if err != nil {
		return fmt.Errorf("starting proxy with config dir: %w", err)
	}

	wordlists, err := wordlist.NewManager(configDir)
	if err != nil {
		return fmt.Errorf("creating wordlist manager : %w", err)
	}

	unlock, err := lockProject(projectPath)
	if err != nil {
		return fmt.Errorf("locking project: %w", err)
	}
	closeProxy := func() error { return nil }
	releaseClaim := func() error { return nil }
	defer func() {
		resultErr = errors.Join(resultErr, cleanupService(closeProxy, unlock, releaseClaim))
	}()

	dbConn, err := db.New(projectPath, proxy.Logger)
	if err != nil {
		return fmt.Errorf("opening project: %w", err)
	}

	repo := db.NewProxyRepo(dbConn)
	extensions, err := repo.GetExtensions()
	if err != nil {
		repo.Close()
		return fmt.Errorf("loading project extensions: %w", err)
	}

	err = proxy.WithOptions(
		marasi.WithWordlistManager(wordlists),
		marasi.WithDefaultRepositories(repo),
		marasi.WithExtensions(extensions),
		marasi.WithBasePipeline(),
		marasi.WithDefaultModifierPipeline(),
		marasi.WithTLSContext(ctx),
	)
	if err != nil {
		repo.Close()
		return fmt.Errorf("starting proxy base options: %w", err)
	}
	closeProxy = proxy.Close

	controlListener, instanceLock, err := claimInstance(socketPath, lockPath)
	if err != nil {
		return err
	}
	releaseClaim = func() error {
		return releaseInstance(controlListener, socketPath, instanceLock)
	}

	serviceCtx, stopService := context.WithCancel(ctx)
	defer stopService()
	serviceServer := service.NewServer(proxy, stopService)
	if err := proxy.WithOptions(
		marasi.WithRequestHandler(serviceServer.HandleRequest),
		marasi.WithResponseHandler(serviceServer.HandleResponse),
	); err != nil {
		return fmt.Errorf("installing traffic event handlers: %w", err)
	}

	portString := strconv.FormatUint(uint64(port), 10)
	proxyListener, err := proxy.GetListener(address, portString)
	if err != nil {
		return fmt.Errorf("binding proxy listener on address %s port %s: %w", address, portString, err)
	}
	proxyServeDone := serveProxy(proxy, proxyListener, logFile)
	closeProxy = func() error {
		closeErr := proxy.Close()
		<-proxyServeDone
		return closeErr
	}

	server := &http.Server{Handler: serviceServer}
	return serveControlAPIReady(serviceCtx, server, controlListener, shutdownTimeout, func() error {
		return ready(proxyListener.Addr().String())
	})
}

// serveProxy serves the proxy in the background and returns a channel that
// closes when Serve returns. It waits until Accept is first called or Serve
// has already returned.
func serveProxy(server proxyServer, listener net.Listener, logWriter io.Writer) <-chan struct{} {
	readyListener := &startupListener{Listener: listener, started: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveErr := server.Serve(readyListener)
		if serveErr != nil {
			fmt.Fprintf(logWriter, "proxy listener stopped: %v\n", serveErr)
		}
	}()
	select {
	case <-readyListener.started:
	case <-done:
	}
	return done
}

// stopService stops the instance at instancePath. A missing socket and lock is success.
func stopService(ctx context.Context, instancePath string) error {
	socketPath := instancePath + ".sock"
	lockPath := instancePath + ".lock"
	if _, err := os.Stat(socketPath); errors.Is(err, os.ErrNotExist) {
		if _, lockErr := os.Stat(lockPath); errors.Is(lockErr, os.ErrNotExist) {
			return nil
		} else if lockErr != nil {
			return fmt.Errorf("checking instance lock %s: %w", lockPath, lockErr)
		}
		return waitForInstanceStop(ctx, lockPath)
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

// waitForInstanceStop polls until lockPath can be locked, or ctx is cancelled.
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
				wrapError("unlocking instance probe", filelock.Unlock(lock)),
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

// cleanupService closes the proxy, unlocks the project, and releases the instance claim.
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

// serveControlAPI serves server on listener until ctx is cancelled, then shuts down within timeout.
func serveControlAPI(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration) error {
	return serveControlAPIReady(ctx, server, listener, timeout, nil)
}

// serveControlAPIReady is serveControlAPI plus a ready hook after the listener starts accepting.
func serveControlAPIReady(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration, ready func() error) error {
	readyListener := &startupListener{Listener: listener, started: make(chan struct{})}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(readyListener)
	}()

	select {
	case <-readyListener.started:
		if ready != nil {
			if err := ready(); err != nil {
				return errors.Join(err, wrapError("closing control server", server.Close()), wrapError("serving control API", <-serveResult))
			}
		}
	case <-ctx.Done():
		return shutdownControlAPI(server, serveResult, timeout)
	case err := <-serveResult:
		return errors.Join(
			wrapError("serving control API", err),
			wrapError("closing control server", server.Close()),
		)
	}

	select {
	case <-ctx.Done():
		return shutdownControlAPI(server, serveResult, timeout)
	case err := <-serveResult:
		return errors.Join(
			wrapError("serving control API", err),
			wrapError("closing control server", server.Close()),
		)
	}
}

// shutdownControlAPI shuts down server within timeout, then waits for Serve to return.
func shutdownControlAPI(server *http.Server, serveResult <-chan error, timeout time.Duration) error {
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
}

// wrapError returns nil if err is nil, otherwise annotates err with action.
func wrapError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

// prepareProjectPath resolves --project into projectPath.
func prepareProjectPath(*cobra.Command, []string) error {
	path, err := resolveProjectPath(configDir, projectName)
	if err != nil {
		return err
	}
	projectPath = path
	return nil
}

// resolveProjectPath returns the project file under configDir for name.
func resolveProjectPath(configDir, name string) (string, error) {
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

// claimInstance locks lockPath and listens on socketPath.
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
		filelock.Unlock(lock)
		lock.Close()
		return nil, nil, err
	}

	return listener, lock, nil
}

// prepareInstancesDir creates path with instance-only permissions.
func prepareInstancesDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("creating instances directory: %w", err)
	}
	if err := secureInstancesDir(path); err != nil {
		return fmt.Errorf("securing instances directory: %w", err)
	}
	return nil
}

// openInstanceLog appends to path, creating it with instance-only permissions.
func openInstanceLog(path string) (*os.File, error) {
	if err := prepareInstancesDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening instance log %s: %w", path, err)
	}
	if err := secureInstanceFile(path); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("securing instance log %s: %w", path, err)
	}
	return logFile, nil
}

// acquireInstanceLock creates path and takes an exclusive lock.
// errInstanceLockHeld means another process holds it.
func acquireInstanceLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening instance lock %s: %w", path, err)
	}
	if err := secureInstanceFile(path); err != nil {
		lock.Close()
		return nil, fmt.Errorf("securing instance lock %s: %w", path, err)
	}
	if err := filelock.TryLock(lock); err != nil {
		lock.Close()
		if filelock.IsUnavailable(err) {
			return nil, fmt.Errorf("instance already running: %w: %v", errInstanceLockHeld, err)
		}
		return nil, fmt.Errorf("locking instance %s: %w", path, err)
	}
	return lock, nil
}

// listenOnInstanceSocket listens on a Unix socket at path after removing a stale file.
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

// releaseInstance closes the listener, removes socketPath, and unlocks the instance lock.
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
		wrapError("unlocking instance", filelock.Unlock(lock)),
		wrapError("closing instance lock", lock.Close()),
	)
}

// lockProject takes an exclusive lock on path so only one process can open the project.
func lockProject(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating projects dir: %w", err)
	}

	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening project lock %s: %w", lockPath, err)
	}

	if err := filelock.TryLock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("project already open: %w", err)
	}

	return func() error {
		unlockErr := filelock.Unlock(f)
		closeErr := f.Close()
		return errors.Join(
			wrapError("unlocking project", unlockErr),
			wrapError("closing project lock", closeErr),
		)
	}, nil
}
