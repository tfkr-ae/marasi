package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/service"
	"github.com/tfkr-ae/marasi/wordlist"
)

var projectName string

const unixSocketPathLimit = 104

func init() {
	startCmd.Flags().StringVar(&projectName, "project", "scratchpad", "Project name")
	serviceCmd.AddCommand(startCmd)
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
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		return startService(ctx, configDir, projectName, instance)
	},
}

func startService(ctx context.Context, configDir, projectName, instanceName string) error {
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
	defer unlock()

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
	defer proxy.Close()

	listener, instanceLock, err := claimInstance(socketPath, lockPath)
	if err != nil {
		return err
	}
	defer releaseInstance(listener, socketPath, instanceLock)

	server := &http.Server{Handler: service.NewServer(proxy)}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		if err := server.Close(); err != nil {
			return fmt.Errorf("closing control server: %w", err)
		}
		if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serving control API: %w", err)
		}
		return nil
	case err := <-serveResult:
		return fmt.Errorf("serving control API: %w", err)
	}
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
	if err := os.Chmod(path, 0700); err != nil {
		return fmt.Errorf("securing instances directory: %w", err)
	}
	return nil
}

func acquireInstanceLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening instance lock %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		lock.Close()
		return nil, fmt.Errorf("securing instance lock %s: %w", path, err)
	}
	if err := lockFile(lock); err != nil {
		lock.Close()
		return nil, fmt.Errorf("instance already running: %w", err)
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
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, fmt.Errorf("securing instance socket %s: %w", path, err)
	}
	return listener, nil
}

func releaseInstance(listener net.Listener, socketPath string, lock *os.File) {
	listener.Close()
	os.Remove(socketPath)
	unlockFile(lock)
	lock.Close()
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
		if unlockErr != nil {
			return fmt.Errorf("unlocking project: %w", unlockErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing project lock: %w", closeErr)
		}
		return nil
	}, nil
}
