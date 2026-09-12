package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

		got, err := projectPath(configDir, "scratchpad")
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

		got, err := projectPath(configDir, "juiceshop-test")
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

		got, err := projectPath(configDir, "scratchpad.marasi")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should raise an error if project name is empty", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), "")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name is absolute path", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), "/tmp/scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains parent directory reference", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), "../scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains subdirectory", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), "nested/scratchpad")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name contains Windows path separator", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), `nested\scratchpad`)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if project name is current directory", func(t *testing.T) {
		_, err := projectPath(t.TempDir(), ".")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestInstancePaths(t *testing.T) {
	t.Run("should resolve a named instance under instances", func(t *testing.T) {
		configDir := filepath.Join(string(filepath.Separator), "config")

		socketPath, lockPath, err := resolveInstancePaths(configDir, " work ")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		wantSocket := filepath.Join(configDir, "instances", "work.sock")
		if socketPath != wantSocket {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantSocket, socketPath)
		}

		wantLock := filepath.Join(configDir, "instances", "work.lock")
		if lockPath != wantLock {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantLock, lockPath)
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
			_, _, err := resolveInstancePaths(t.TempDir(), name)
			if err == nil {
				t.Fatal("\nwanted:\nerror\ngot:\nnil")
			}
		})
	}

	t.Run("should reject a socket path over the portable limit", func(t *testing.T) {
		configDir := filepath.Join(string(filepath.Separator), "config")
		name := strings.Repeat("a", 104)
		path := filepath.Join(configDir, "instances", name+".sock")

		_, _, err := resolveInstancePaths(configDir, name)
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

func TestClaimInstance(t *testing.T) {
	t.Run("should create owner-only instance resources", func(t *testing.T) {
		socketPath, lockPath, err := resolveInstancePaths(serviceConfigDir(t), "work")
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
		socketPath, lockPath, err := resolveInstancePaths(serviceConfigDir(t), "work")
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
		socketPath, lockPath, err := resolveInstancePaths(serviceConfigDir(t), "work")
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
		workSocketPath, workLockPath, err := resolveInstancePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		personalSocketPath, personalLockPath, err := resolveInstancePaths(configDir, "personal")
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
		socketPath, lockPath, err := resolveInstancePaths(serviceConfigDir(t), "work")
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
			unlockFile(restartedLock)
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

func TestStartService(t *testing.T) {
	t.Run("should stop through the control API", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		socketPath, lockPath, err := resolveInstancePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- startService(ctx, configDir, "scratchpad", "work")
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
			unlockFile(probe)
			probe.Close()
			t.Fatal("\nwanted:\nheld instance lock\ngot:\nfree instance lock")
		}
		projectPath := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if unlock, err := lockProject(projectPath); err == nil {
			unlock()
			t.Fatal("\nwanted:\nheld project lock\ngot:\nfree project lock")
		}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		}
		defer transport.CloseIdleConnections()
		response, err := (&http.Client{Transport: transport}).Post("http://marasi/service/stop", "", nil)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", http.StatusAccepted, response.StatusCode)
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
			unlockFile(instanceLock)
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
		socketPath, _, err := resolveInstancePaths(configDir, "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- startService(ctx, configDir, "scratchpad", "work")
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
		if err := startService(restartContext, configDir, "scratchpad", "work"); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	t.Run("should create the default scratchpad project", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := startService(ctx, configDir, "scratchpad", "default")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		path := filepath.Join(configDir, "projects", "scratchpad.marasi")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	t.Run("should raise an error if the project name is invalid", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := startService(ctx, serviceConfigDir(t), "foo/bar", "default")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should raise an error if the instance name is invalid", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := startService(ctx, serviceConfigDir(t), "scratchpad", "work.sock")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
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
