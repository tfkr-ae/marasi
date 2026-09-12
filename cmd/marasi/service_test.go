package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/internal/filelock"
)

var errServing = errors.New("serving failed")
var errClosingListener = errors.New("listener close failed")
var errClosingProxy = errors.New("proxy close failed")
var errUnlockingProject = errors.New("project unlock failed")
var errReleasingInstance = errors.New("instance release failed")

type failingListener struct {
	closed   bool
	closeErr error
}

func (l *failingListener) Accept() (net.Conn, error) { return nil, errServing }
func (l *failingListener) Addr() net.Addr            { return &net.TCPAddr{} }
func (l *failingListener) Close() error {
	l.closed = true
	return l.closeErr
}

func TestProjectPath(t *testing.T) {
	t.Run("should resolve scratchpad under projects", func(t *testing.T) {
		configDir := t.TempDir()

		got, err := resolveProjectPath(configDir, "scratchpad")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should resolve a named project under projects", func(t *testing.T) {
		configDir := t.TempDir()

		got, err := resolveProjectPath(configDir, "juiceshop-test")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(configDir, "projects", "juiceshop-test.marasi")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should strip a trailing .marasi suffix", func(t *testing.T) {
		configDir := t.TempDir()

		got, err := resolveProjectPath(configDir, "scratchpad.marasi")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should raise an error if project name is empty", func(t *testing.T) {
		_, err := resolveProjectPath(t.TempDir(), "")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name is absolute path", func(t *testing.T) {
		_, err := resolveProjectPath(t.TempDir(), "/tmp/scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains parent directory reference", func(t *testing.T) {
		_, err := resolveProjectPath(t.TempDir(), "../scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains subdirectory", func(t *testing.T) {
		_, err := resolveProjectPath(t.TempDir(), "nested/scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains Windows path separator", func(t *testing.T) {
		_, err := resolveProjectPath(t.TempDir(), `nested\scratchpad`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name is current directory", func(t *testing.T) {
		_, err := resolveProjectPath(t.TempDir(), ".")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestInstancePath(t *testing.T) {
	t.Run("should resolve a named instance under instances", func(t *testing.T) {
		configDir := filepath.Join(string(filepath.Separator), "config")

		got, err := resolveInstancePath(configDir, " work ")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(configDir, "instances", "work")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	invalidNames := []string{
		"",
		"   ",
		".",
		"..",
		"/tmp/work",
		"../work",
		"nested/work",
		`nested\work`,
		"work.sock",
	}
	for _, name := range invalidNames {
		t.Run("should reject invalid instance name "+name, func(t *testing.T) {
			_, err := resolveInstancePath(t.TempDir(), name)
			if err == nil {
				t.Fatal("\nwanted:\nerror\ngot:\nnil")
			}
		})
	}

	t.Run("should reject a socket path over the portable limit", func(t *testing.T) {
		configDir := filepath.Join(string(filepath.Separator), "config")
		name := strings.Repeat("a", 104)
		path := filepath.Join(configDir, "instances", name+".sock")

		_, err := resolveInstancePath(configDir, name)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "104") {
			t.Fatalf("\nwanted:\nerror containing %q and 104\ngot:\n%v", path, err)
		}
	})
}

func TestInstanceFlag(t *testing.T) {
	t.Run("should default to the default instance", func(t *testing.T) {
		flag := rootCmd.PersistentFlags().Lookup("instance")
		if flag == nil {
			t.Fatal("\nwanted:\ninstance flag\ngot:\nnil")
		}
		if flag.DefValue != "default" {
			t.Fatalf("\nwanted:\ndefault\ngot:\n%s", flag.DefValue)
		}
	})
}

func TestCommandPreparation(t *testing.T) {
	t.Run("should prepare the instance path before service stop runs", func(t *testing.T) {
		oldRunE := stopCmd.RunE
		oldConfigDir, oldInstancePath := configDir, instancePath
		t.Cleanup(func() {
			stopCmd.RunE = oldRunE
			configDir, instancePath = oldConfigDir, oldInstancePath
			rootCmd.SetArgs(nil)
		})

		dir := serviceConfigDir(t)
		var got string
		stopCmd.RunE = func(*cobra.Command, []string) error {
			got = instancePath
			return nil
		}
		rootCmd.SetArgs([]string{"--config-dir", dir, "--instance", " work ", "service", "stop"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := filepath.Join(dir, "instances", "work")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should prepare instance and project paths before service start runs", func(t *testing.T) {
		oldRootPreRunE := rootCmd.PersistentPreRunE
		oldStartPreRunE, oldRunE := startCmd.PreRunE, startCmd.RunE
		oldConfigDir, oldInstancePath, oldProjectPath := configDir, instancePath, projectPath
		t.Cleanup(func() {
			rootCmd.PersistentPreRunE = oldRootPreRunE
			startCmd.PreRunE, startCmd.RunE = oldStartPreRunE, oldRunE
			configDir, instancePath, projectPath = oldConfigDir, oldInstancePath, oldProjectPath
			rootCmd.SetArgs(nil)
		})

		var order []string
		rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
			if err := oldRootPreRunE(cmd, args); err != nil {
				return err
			}
			order = append(order, "instance")
			return nil
		}
		startCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
			if err := oldStartPreRunE(cmd, args); err != nil {
				return err
			}
			order = append(order, "project")
			return nil
		}
		startCmd.RunE = func(*cobra.Command, []string) error {
			order = append(order, "run")
			return nil
		}
		dir := serviceConfigDir(t)
		rootCmd.SetArgs([]string{"--config-dir", dir, "--instance", "work", "service", "start", "--project", " juice-shop.marasi "})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got := strings.Join(order, ","); got != "instance,project,run" {
			t.Fatalf("\nwanted:\ninstance,project,run\ngot:\n%s", got)
		}
		wantInstancePath := filepath.Join(dir, "instances", "work")
		if instancePath != wantInstancePath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantInstancePath, instancePath)
		}
		wantProjectPath := filepath.Join(dir, "projects", "juice-shop.marasi")
		if projectPath != wantProjectPath {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantProjectPath, projectPath)
		}
	})

	t.Run("should not prepare a project for service stop", func(t *testing.T) {
		oldRunE := stopCmd.RunE
		oldProjectPath := projectPath
		t.Cleanup(func() {
			stopCmd.RunE = oldRunE
			projectPath = oldProjectPath
			rootCmd.SetArgs(nil)
		})

		projectPath = "unchanged"
		stopCmd.RunE = func(*cobra.Command, []string) error { return nil }
		rootCmd.SetArgs([]string{"--config-dir", serviceConfigDir(t), "service", "stop"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if projectPath != "unchanged" {
			t.Fatalf("\nwanted:\nunchanged\ngot:\n%s", projectPath)
		}
	})

	t.Run("should stop before project preparation when instance preparation fails", func(t *testing.T) {
		oldPreRunE, oldRunE := startCmd.PreRunE, startCmd.RunE
		oldInstancePath := instancePath
		t.Cleanup(func() {
			startCmd.PreRunE, startCmd.RunE = oldPreRunE, oldRunE
			instancePath = oldInstancePath
			rootCmd.SetArgs(nil)
		})

		projectPrepared, ran := false, false
		startCmd.PreRunE = func(*cobra.Command, []string) error {
			projectPrepared = true
			return nil
		}
		startCmd.RunE = func(*cobra.Command, []string) error {
			ran = true
			return nil
		}
		instancePath = "unchanged"
		rootCmd.SetArgs([]string{"--config-dir", "", "service", "start"})

		if err := rootCmd.Execute(); err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if projectPrepared || ran {
			t.Fatalf("\nwanted:\nno descendant execution\ngot:\nproject prepared %t, ran %t", projectPrepared, ran)
		}
		if instancePath != "unchanged" {
			t.Fatalf("\nwanted:\nunchanged\ngot:\n%s", instancePath)
		}
	})

	t.Run("should stop before execution when project preparation fails", func(t *testing.T) {
		oldRunE := startCmd.RunE
		oldProjectPath := projectPath
		t.Cleanup(func() {
			startCmd.RunE = oldRunE
			projectPath = oldProjectPath
			rootCmd.SetArgs(nil)
		})

		ran := false
		startCmd.RunE = func(*cobra.Command, []string) error {
			ran = true
			return nil
		}
		projectPath = "unchanged"
		rootCmd.SetArgs([]string{"--config-dir", serviceConfigDir(t), "service", "start", "--project", "foo/bar"})

		if err := rootCmd.Execute(); err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if ran {
			t.Fatal("\nwanted:\nno command execution\ngot:\ncommand ran")
		}
		if projectPath != "unchanged" {
			t.Fatalf("\nwanted:\nunchanged\ngot:\n%s", projectPath)
		}
	})

	t.Run("should run root preparation before a descendant persistent hook", func(t *testing.T) {
		oldPreRunE, oldRunE := serviceCmd.PersistentPreRunE, stopCmd.RunE
		oldInstancePath := instancePath
		t.Cleanup(func() {
			serviceCmd.PersistentPreRunE, stopCmd.RunE = oldPreRunE, oldRunE
			instancePath = oldInstancePath
			rootCmd.SetArgs(nil)
		})

		dir := serviceConfigDir(t)
		want := filepath.Join(dir, "instances", "work")
		var order []string
		instancePath = "unchanged"
		serviceCmd.PersistentPreRunE = func(*cobra.Command, []string) error {
			if instancePath != want {
				return fmt.Errorf("instance path not prepared: %s", instancePath)
			}
			order = append(order, "descendant")
			return nil
		}
		stopCmd.RunE = func(*cobra.Command, []string) error {
			order = append(order, "run")
			return nil
		}
		rootCmd.SetArgs([]string{"--config-dir", dir, "--instance", "work", "service", "stop"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got := strings.Join(order, ","); got != "descendant,run" {
			t.Fatalf("\nwanted:\ndescendant,run\ngot:\n%s", got)
		}
	})

	t.Run("should pass command cancellation to service start", func(t *testing.T) {
		oldConfigDir, oldInstancePath, oldProjectPath := configDir, instancePath, projectPath
		t.Cleanup(func() {
			configDir, instancePath, projectPath = oldConfigDir, oldInstancePath, oldProjectPath
			rootCmd.SetArgs(nil)
			rootCmd.SetContext(nil)
			startCmd.SetContext(nil)
		})

		dir := serviceConfigDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		startCmd.SetContext(nil)
		rootCmd.SetArgs([]string{"--config-dir", dir, "--instance", "work", "service", "start", "--project", "scratchpad"})
		result := make(chan error, 1)
		go func() { result <- rootCmd.ExecuteContext(ctx) }()

		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("\nwanted:\ncanceled command\ngot:\ntimeout")
		}
		if _, err := os.Stat(filepath.Join(dir, "instances", "work.sock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\nremoved socket\ngot:\n%v", err)
		}
	})
}

func instanceResourcePaths(configDir, name string) (string, string, error) {
	path, err := resolveInstancePath(configDir, name)
	if err != nil {
		return "", "", err
	}
	return path + ".sock", path + ".lock", nil
}

func TestClaimInstance(t *testing.T) {
	t.Run("should create owner-only instance resources", func(t *testing.T) {
		socketPath, lockPath, err := instanceResourcePaths(serviceConfigDir(t), "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		listener, lock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer releaseInstance(listener, socketPath, lock)

		if runtime.GOOS == "windows" {
			return
		}
		for path, want := range map[string]os.FileMode{
			filepath.Dir(socketPath): 0700,
			lockPath:                 0600,
			socketPath:               0600,
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Fatalf("\nwanted:\n%#o\ngot:\n%#o", want, got)
			}
		}
	})

	t.Run("should replace a stale socket after acquiring the lock", func(t *testing.T) {
		socketPath, lockPath, err := instanceResourcePaths(serviceConfigDir(t), "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.WriteFile(socketPath, []byte("stale"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		listener, lock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer releaseInstance(listener, socketPath, lock)

		info, err := os.Lstat(socketPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("\nwanted:\nsocket\ngot:\n%v", info.Mode())
		}
	})

	t.Run("should reject a concurrent owner without disturbing it", func(t *testing.T) {
		socketPath, lockPath, err := instanceResourcePaths(serviceConfigDir(t), "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		firstListener, firstLock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer releaseInstance(firstListener, socketPath, firstLock)

		secondListener, secondLock, err := claimInstance(socketPath, lockPath)
		if err == nil {
			releaseInstance(secondListener, socketPath, secondLock)
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if _, err := os.Stat(socketPath); err != nil {
			t.Fatalf("\nwanted:\nrunning instance socket\ngot:\n%v", err)
		}
	})

	t.Run("should allow different instance names", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		workSocketPath, workLockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		personalSocketPath, personalLockPath, err := instanceResourcePaths(configDir, "personal")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		workListener, workLock, err := claimInstance(workSocketPath, workLockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer releaseInstance(workListener, workSocketPath, workLock)
		personalListener, personalLock, err := claimInstance(personalSocketPath, personalLockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer releaseInstance(personalListener, personalSocketPath, personalLock)
	})

	t.Run("should leave the lock and permit restart after close", func(t *testing.T) {
		socketPath, lockPath, err := instanceResourcePaths(serviceConfigDir(t), "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		firstListener, firstLock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		releaseInstance(firstListener, socketPath, firstLock)

		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("\nwanted:\nlock file\ngot:\n%v", err)
		}
		if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\nremoved socket\ngot:\n%v", err)
		}

		secondListener, secondLock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer releaseInstance(secondListener, socketPath, secondLock)
	})

	t.Run("should preserve errors and complete every release step", func(t *testing.T) {
		dir := t.TempDir()
		socketPath := filepath.Join(dir, "work.sock")
		if err := os.Mkdir(socketPath, 0700); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.WriteFile(filepath.Join(socketPath, "keep"), nil, 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		lockPath := filepath.Join(dir, "work.lock")
		lock, err := acquireInstanceLock(lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		listener := &failingListener{closeErr: errClosingListener}

		err = releaseInstance(listener, socketPath, lock)
		if !errors.Is(err, errClosingListener) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", errClosingListener, err)
		}
		if !strings.Contains(err.Error(), "removing instance socket") {
			t.Fatalf("\nwanted:\nremove error\ngot:\n%v", err)
		}
		if _, err := lock.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("\nwanted:\nclosed lock\ngot:\n%v", err)
		}

		restartedLock, err := acquireInstanceLock(lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer func() {
			filelock.Unlock(restartedLock)
			restartedLock.Close()
		}()
	})
}

func serviceConfigDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}

	dir, err := os.MkdirTemp("/tmp", "marasi-")
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestLockProject(t *testing.T) {
	t.Run("should create the projects directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "projects", "scratchpad.marasi")

		unlock, err := lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer unlock()

		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if !info.IsDir() {
			t.Fatalf("\nwanted:\ndirectory\ngot:\n%v", info.Mode())
		}
	})

	t.Run("should raise an error if the project is already open", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "projects", "scratchpad.marasi")

		unlock, err := lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer unlock()

		_, err = lockProject(path)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should allow opening the project after it is closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "projects", "scratchpad.marasi")

		unlock, err := lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := unlock(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		unlock, err = lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer unlock()
	})

	t.Run("should preserve unlock and close errors", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "projects", "scratchpad.marasi")
		unlock, err := lockProject(path)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := unlock(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = unlock()
		if !strings.Contains(err.Error(), "unlocking project") {
			t.Fatalf("\nwanted:\nunlock error\ngot:\n%v", err)
		}
		if !strings.Contains(err.Error(), "closing project lock") {
			t.Fatalf("\nwanted:\nclose error\ngot:\n%v", err)
		}
	})
}

func TestCleanupService(t *testing.T) {
	t.Run("should clean up in completion order and preserve errors", func(t *testing.T) {
		var order []string
		err := cleanupService(
			func() error {
				order = append(order, "proxy")
				return errClosingProxy
			},
			func() error {
				order = append(order, "project")
				return errUnlockingProject
			},
			func() error {
				order = append(order, "instance")
				return errReleasingInstance
			},
		)

		if got := strings.Join(order, ","); got != "proxy,project,instance" {
			t.Fatalf("\nwanted:\nproxy,project,instance\ngot:\n%s", got)
		}
		for _, want := range []error{errClosingProxy, errUnlockingProject, errReleasingInstance} {
			if !errors.Is(err, want) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, err)
			}
		}
	})
}

func TestStopService(t *testing.T) {
	t.Run("should stop the selected instance and wait for cleanup", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		listener, lock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		released := false
		requestReceived := make(chan struct{}, 1)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/service/stop" {
				t.Errorf("\nwanted:\nPOST /service/stop\ngot:\n%s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
			requestReceived <- struct{}{}
		})}
		go server.Serve(listener)
		defer func() {
			server.Close()
			if !released {
				releaseInstance(listener, socketPath, lock)
			}
		}()

		result := make(chan error, 1)
		go func() {
			result <- stopService(context.Background(), filepath.Join(configDir, "instances", "work"))
		}()
		select {
		case <-requestReceived:
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\nstop request\ngot:\ntimeout")
		}

		select {
		case err := <-result:
			t.Fatalf("\nwanted:\ncommand waiting for cleanup\ngot:\n%v", err)
		case <-time.After(50 * time.Millisecond):
		}

		if err := releaseInstance(listener, socketPath, lock); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		released = true
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\ncommand completion\ngot:\ntimeout")
		}
	})

	t.Run("should succeed without creating resources when the socket is missing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if err := stopService(context.Background(), filepath.Join(configDir, "instances", "work")); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		for _, path := range []string{socketPath, lockPath} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("\nwanted:\nmissing %s\ngot:\n%v", path, err)
			}
		}
	})

	t.Run("should leave a stale socket when no instance owns the lock", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.WriteFile(socketPath, []byte("stale"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if err := stopService(context.Background(), filepath.Join(configDir, "instances", "work")); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got, err := os.ReadFile(socketPath); err != nil || string(got) != "stale" {
			t.Fatalf("\nwanted:\nstale socket\ngot:\n%s, %v", got, err)
		}
		probe, err := acquireInstanceLock(lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nreleased probe lock\ngot:\n%v", err)
		}
		defer func() {
			filelock.Unlock(probe)
			probe.Close()
		}()
	})

	t.Run("should wait after a failed dial while the instance lock is held", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.WriteFile(socketPath, []byte("stale"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		lock, err := acquireInstanceLock(lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		locked := true
		defer func() {
			if locked {
				filelock.Unlock(lock)
				lock.Close()
			}
		}()

		result := make(chan error, 1)
		go func() {
			result <- stopService(context.Background(), filepath.Join(configDir, "instances", "work"))
		}()
		select {
		case err := <-result:
			t.Fatalf("\nwanted:\ncommand waiting for lock release\ngot:\n%v", err)
		case <-time.After(50 * time.Millisecond):
		}

		if err := errors.Join(filelock.Unlock(lock), lock.Close()); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		locked = false
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\ncommand completion\ngot:\ntimeout")
		}
	})

	t.Run("should let repeated stops wait for the same cleanup", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		listener, lock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		released := false
		requests := make(chan struct{}, 2)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			requests <- struct{}{}
		})}
		go server.Serve(listener)
		defer func() {
			server.Close()
			if !released {
				releaseInstance(listener, socketPath, lock)
			}
		}()

		results := make(chan error, 2)
		go func() { results <- stopService(context.Background(), filepath.Join(configDir, "instances", "work")) }()
		select {
		case <-requests:
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\nfirst stop request\ngot:\ntimeout")
		}
		go func() { results <- stopService(context.Background(), filepath.Join(configDir, "instances", "work")) }()
		select {
		case <-requests:
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\nsecond stop request\ngot:\ntimeout")
		}

		for range 2 {
			select {
			case err := <-results:
				t.Fatalf("\nwanted:\ncommands waiting for cleanup\ngot:\n%v", err)
			case <-time.After(50 * time.Millisecond):
			}
		}
		if err := releaseInstance(listener, socketPath, lock); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		released = true
		for range 2 {
			select {
			case err := <-results:
				if err != nil {
					t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("\nwanted:\ncommand completion\ngot:\ntimeout")
			}
		}
	})

	t.Run("should report a rejected response with a bounded body", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		listener, lock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, strings.Repeat("x", maxErrorBodySize+100))
		})}
		go server.Serve(listener)
		defer func() {
			server.Close()
			releaseInstance(listener, socketPath, lock)
		}()

		err = stopService(context.Background(), filepath.Join(configDir, "instances", "work"))
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "500 Internal Server Error") {
			t.Fatalf("\nwanted:\nHTTP status\ngot:\n%v", err)
		}
		if got := strings.Count(err.Error(), "x"); got != maxErrorBodySize {
			t.Fatalf("\nwanted:\n%d response bytes\ngot:\n%d", maxErrorBodySize, got)
		}
	})

	t.Run("should reject redirects without following them", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		listener, lock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/service/stop" {
				http.Redirect(w, r, "/accepted", http.StatusTemporaryRedirect)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		})}
		go server.Serve(listener)
		defer func() {
			server.Close()
			releaseInstance(listener, socketPath, lock)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		err = stopService(ctx, filepath.Join(configDir, "instances", "work"))
		if err == nil || !strings.Contains(err.Error(), "307 Temporary Redirect") {
			t.Fatalf("\nwanted:\nredirect status error\ngot:\n%v", err)
		}
	})

	t.Run("should stop waiting when its context is canceled", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.WriteFile(socketPath, []byte("stale"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		lock, err := acquireInstanceLock(lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer func() {
			filelock.Unlock(lock)
			lock.Close()
		}()
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- stopService(ctx, filepath.Join(configDir, "instances", "work")) }()

		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\ncanceled command\ngot:\ntimeout")
		}
	})
}

func TestStopCommand(t *testing.T) {
	t.Run("should accept no arguments and keep the project flag exclusive to start", func(t *testing.T) {
		if err := stopCmd.Args(stopCmd, nil); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := stopCmd.Args(stopCmd, []string{"extra"}); err == nil {
			t.Fatal("\nwanted:\nargument error\ngot:\nnil")
		}
		if flag := stopCmd.Flag("config-dir"); flag == nil {
			t.Fatal("\nwanted:\nconfig-dir flag\ngot:\nnil")
		}
		if flag := stopCmd.Flag("instance"); flag == nil {
			t.Fatal("\nwanted:\ninstance flag\ngot:\nnil")
		}
		if flag := stopCmd.Flag("project"); flag != nil {
			t.Fatalf("\nwanted:\nno project flag\ngot:\n%s", flag.Name)
		}
	})

	t.Run("should write nothing after a successful stop", func(t *testing.T) {
		oldConfigDir, oldInstance, oldInstancePath := configDir, instance, instancePath
		defer func() {
			configDir, instance, instancePath = oldConfigDir, oldInstance, oldInstancePath
		}()
		configDir = serviceConfigDir(t)
		instance = "work"
		instancePath = filepath.Join(configDir, "instances", instance)
		stopCmd.SetContext(context.Background())
		var output bytes.Buffer
		stopCmd.SetOut(&output)
		stopCmd.SetErr(&output)
		defer func() {
			stopCmd.SetContext(nil)
			stopCmd.SetOut(nil)
			stopCmd.SetErr(nil)
		}()

		if err := stopCmd.RunE(stopCmd, nil); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if output.Len() != 0 {
			t.Fatalf("\nwanted:\nno output\ngot:\n%s", output.String())
		}
	})

	t.Run("should stop waiting when the command context is canceled", func(t *testing.T) {
		oldConfigDir, oldInstance, oldInstancePath := configDir, instance, instancePath
		defer func() {
			configDir, instance, instancePath = oldConfigDir, oldInstance, oldInstancePath
			rootCmd.SetArgs(nil)
			rootCmd.SetContext(nil)
			stopCmd.SetContext(nil)
		}()
		configDir = serviceConfigDir(t)
		instance = "work"
		socketPath, lockPath, err := instanceResourcePaths(configDir, instance)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := os.WriteFile(socketPath, []byte("stale"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		lock, err := acquireInstanceLock(lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer func() {
			filelock.Unlock(lock)
			lock.Close()
		}()
		ctx, cancel := context.WithCancel(context.Background())
		stopCmd.SetContext(nil)
		rootCmd.SetArgs([]string{"--config-dir", configDir, "--instance", instance, "service", "stop"})
		result := make(chan error, 1)
		go func() { result <- rootCmd.ExecuteContext(ctx) }()

		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
			}
		case <-time.After(time.Second):
			t.Fatal("\nwanted:\ncanceled command\ngot:\ntimeout")
		}
	})
}

func TestStartService(t *testing.T) {
	t.Run("should stop through the control API", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- startService(ctx, configDir, filepath.Join(configDir, "projects", "scratchpad.marasi"), filepath.Join(configDir, "instances", "work"))
		}()
		finished := false
		defer func() {
			if !finished {
				cancel()
				<-result
			}
		}()

		waitForPath(t, socketPath)
		if probe, err := acquireInstanceLock(lockPath); err == nil {
			filelock.Unlock(probe)
			probe.Close()
			t.Fatal("\nwanted:\nheld instance lock\ngot:\nfree instance lock")
		}
		projectPath := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if unlock, err := lockProject(projectPath); err == nil {
			unlock()
			t.Fatal("\nwanted:\nheld project lock\ngot:\nfree project lock")
		}
		if err := stopService(context.Background(), filepath.Join(configDir, "instances", "work")); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = <-result
		finished = true
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\nremoved socket\ngot:\n%v", err)
		}
		instanceLock, err := acquireInstanceLock(lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer func() {
			filelock.Unlock(instanceLock)
			instanceLock.Close()
		}()
		projectLock, err := lockProject(projectPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer projectLock()
	})

	t.Run("should serve HTTP on the named instance socket until cancellation", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, _, err := instanceResourcePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- startService(ctx, configDir, filepath.Join(configDir, "projects", "scratchpad.marasi"), filepath.Join(configDir, "instances", "work"))
		}()
		finished := false
		defer func() {
			if !finished {
				cancel()
				<-result
			}
		}()

		waitForPath(t, socketPath)
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		}
		defer transport.CloseIdleConnections()
		response, err := (&http.Client{Transport: transport}).Get("http://marasi/")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusNotFound, response.StatusCode)
		}

		cancel()
		if err := <-result; err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		finished = true
		if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\nremoved socket\ngot:\n%v", err)
		}

		restartContext, stopRestart := context.WithCancel(context.Background())
		stopRestart()
		if err := startService(restartContext, configDir, filepath.Join(configDir, "projects", "scratchpad.marasi"), filepath.Join(configDir, "instances", "work")); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	t.Run("should create the default scratchpad project", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := startService(ctx, configDir, filepath.Join(configDir, "projects", "scratchpad.marasi"), filepath.Join(configDir, "instances", "default"))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		path := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

}

func TestServeControlAPI(t *testing.T) {
	t.Run("should give an active request time to finish", func(t *testing.T) {
		requestStarted := make(chan struct{})
		finishRequest := make(chan struct{})
		server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(requestStarted)
			<-finishRequest
		})}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- serveControlAPI(ctx, server, listener, time.Second)
		}()
		requestResult := make(chan error, 1)
		go func() {
			response, err := http.Get("http://" + listener.Addr().String())
			if err == nil {
				response.Body.Close()
			}
			requestResult <- err
		}()

		<-requestStarted
		cancel()
		select {
		case err := <-result:
			t.Fatalf("\nwanted:\nserver to wait for request\ngot:\n%v", err)
		case <-time.After(50 * time.Millisecond):
		}

		close(finishRequest)
		if err := <-result; err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := <-requestResult; err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	t.Run("should force close a stuck request after the deadline", func(t *testing.T) {
		requestStarted := make(chan struct{})
		server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(requestStarted)
			<-request.Context().Done()
		})}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- serveControlAPI(ctx, server, listener, 50*time.Millisecond)
		}()
		go http.Get("http://" + listener.Addr().String())

		<-requestStarted
		cancel()
		err = <-result
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.DeadlineExceeded, err)
		}
	})

	t.Run("should return an unexpected serving failure", func(t *testing.T) {
		listener := &failingListener{}

		err := serveControlAPI(context.Background(), &http.Server{}, listener, time.Second)
		if !errors.Is(err, errServing) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", errServing, err)
		}
		if !listener.closed {
			t.Fatal("\nwanted:\nclosed listener\ngot:\nopen listener")
		}
	})

	t.Run("should return ErrServerClosed without a shutdown request", func(t *testing.T) {
		server := &http.Server{}
		if err := server.Close(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = serveControlAPI(context.Background(), server, listener, time.Second)
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", http.ErrServerClosed, err)
		}
	})
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("\nwanted:\npath %s to exist\ngot:\ntimeout", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
