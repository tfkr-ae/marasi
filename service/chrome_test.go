package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	marasichrome "github.com/tfkr-ae/marasi/chrome"
	"github.com/tfkr-ae/marasi/internal/filelock"
)

func newChromeTestProxy(t *testing.T, configDir string) *marasi.Proxy {
	t.Helper()
	proxy, err := marasi.New(marasi.WithConfigDir(configDir))
	if err != nil {
		t.Fatalf("creating test proxy: %v", err)
	}
	return proxy
}

func TestChromeProfiles(t *testing.T) {
	t.Run("should register a profile without creating its directory", func(t *testing.T) {
		configDir := t.TempDir()
		proxy := newChromeTestProxy(t, configDir)
		chrome := NewChrome(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)

		profiles, err := chrome.AddProfile(context.Background(), " pentest ")
		if err != nil {
			t.Fatalf("adding profile: %v", err)
		}
		if len(profiles) != 1 || profiles[0] != "pentest" {
			t.Fatalf("\nwanted:\n[pentest]\ngot:\n%v", profiles)
		}
		if _, err := os.Stat(filepath.Join(configDir, "chrome_profiles", "pentest")); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nprofile directory not to exist\ngot:\n%v", err)
		}
	})

	t.Run("should delete a registered profile directory", func(t *testing.T) {
		configDir := t.TempDir()
		proxy := newChromeTestProxy(t, configDir)
		module := NewChrome(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		remover := NewChrome(newChromeTestProxy(t, configDir), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		if _, err := module.AddProfile(context.Background(), "pentest"); err != nil {
			t.Fatalf("adding profile: %v", err)
		}
		profileDir := filepath.Join(configDir, "chrome_profiles", "pentest")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatalf("creating profile directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "Preferences"), []byte("{}"), 0600); err != nil {
			t.Fatalf("writing profile file: %v", err)
		}

		profiles, err := remover.RemoveProfile(context.Background(), "pentest")
		if err != nil {
			t.Fatalf("removing profile: %v", err)
		}
		if len(profiles) != 0 {
			t.Fatalf("\nwanted:\nno profiles\ngot:\n%v", profiles)
		}
		if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nprofile directory removed\ngot:\n%v", err)
		}
	})

	t.Run("should reject duplicate and missing profiles", func(t *testing.T) {
		module := NewChrome(newChromeTestProxy(t, t.TempDir()), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		if _, err := module.AddProfile(context.Background(), "pentest"); err != nil {
			t.Fatalf("adding profile: %v", err)
		}
		if _, err := module.AddProfile(context.Background(), "pentest"); !errors.Is(err, ErrChromeProfileAlreadyExists) {
			t.Fatalf("\nwanted:\nprofile already exists\ngot:\n%v", err)
		}
		if _, err := module.RemoveProfile(context.Background(), "missing"); !errors.Is(err, ErrChromeNotFound) {
			t.Fatalf("\nwanted:\nprofile not found\ngot:\n%v", err)
		}
	})

	t.Run("should keep a profile registered when its directory cannot be removed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses Unix directory permissions")
		}
		configDir := t.TempDir()
		proxy := newChromeTestProxy(t, configDir)
		module := NewChrome(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		if _, err := module.AddProfile(context.Background(), "pentest"); err != nil {
			t.Fatalf("adding profile: %v", err)
		}
		profilesDir := filepath.Join(configDir, "chrome_profiles")
		profileDir := filepath.Join(profilesDir, "pentest")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatalf("creating profile directory: %v", err)
		}
		if err := os.Chmod(profilesDir, 0500); err != nil {
			t.Fatalf("making profile directory read-only: %v", err)
		}
		defer os.Chmod(profilesDir, 0700)

		if _, err := module.RemoveProfile(context.Background(), "pentest"); err == nil {
			t.Fatal("\nwanted:\nprofile removal error\ngot:\nnil")
		}
		profiles, err := module.Profiles(context.Background())
		if err != nil || len(profiles) != 1 || profiles[0] != "pentest" {
			t.Fatalf("\nwanted:\n[pentest]\ngot:\n%v, %v", profiles, err)
		}
	})

}

func TestChromePaths(t *testing.T) {
	t.Run("should classify invalid duplicate and missing paths", func(t *testing.T) {
		module := NewChrome(newChromeTestProxy(t, t.TempDir()), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		path := marasichrome.PathConfig{OS: "linux", Path: "/chrome"}
		if _, err := module.AddPath(context.Background(), path); err != nil {
			t.Fatalf("adding path: %v", err)
		}
		if _, err := module.AddPath(context.Background(), path); !errors.Is(err, ErrChromePathAlreadyExists) {
			t.Fatalf("\nwanted:\npath already exists\ngot:\n%v", err)
		}
		if _, err := module.RemovePath(context.Background(), marasichrome.PathConfig{OS: "linux", Path: "/missing"}); !errors.Is(err, ErrChromeNotFound) {
			t.Fatalf("\nwanted:\npath not found\ngot:\n%v", err)
		}
		if _, err := module.AddPath(context.Background(), marasichrome.PathConfig{OS: "plan9", Path: "/chrome"}); !errors.Is(err, ErrInvalidChromeRequest) {
			t.Fatalf("\nwanted:\ninvalid chrome request\ngot:\n%v", err)
		}
	})

	t.Run("should reread the config before mutations and lists", func(t *testing.T) {
		configDir := t.TempDir()
		firstProxy := newChromeTestProxy(t, configDir)
		secondProxy := newChromeTestProxy(t, configDir)
		listener := &statusListener{status: ListenerStatus{Status: ListenerInactive}}
		first := NewChrome(firstProxy, listener, io.Discard)
		second := NewChrome(secondProxy, listener, io.Discard)

		firstPath := marasichrome.PathConfig{OS: "linux", Path: "/first/chrome"}
		secondPath := marasichrome.PathConfig{OS: "darwin", Path: "/second/chrome"}
		if _, err := first.AddPath(context.Background(), firstPath); err != nil {
			t.Fatalf("adding first path: %v", err)
		}
		if _, err := second.AddPath(context.Background(), secondPath); err != nil {
			t.Fatalf("adding second path: %v", err)
		}

		paths, err := first.Paths(context.Background())
		if err != nil {
			t.Fatalf("listing paths: %v", err)
		}
		if len(paths) != 2 || paths[0] != firstPath || paths[1] != secondPath {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", []marasichrome.PathConfig{firstPath, secondPath}, paths)
		}
		paths, err = second.RemovePath(context.Background(), firstPath)
		if err != nil {
			t.Fatalf("removing first path: %v", err)
		}
		if len(paths) != 1 || paths[0] != secondPath {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", []marasichrome.PathConfig{secondPath}, paths)
		}
	})

	t.Run("should serialize overlapping mutations without losing an add", func(t *testing.T) {
		configDir := t.TempDir()
		module := NewChrome(newChromeTestProxy(t, configDir), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		paths := []marasichrome.PathConfig{
			{OS: "linux", Path: "/first/chrome"},
			{OS: "darwin", Path: "/second/chrome"},
		}
		errs := make(chan error, len(paths))
		start := make(chan struct{})
		for _, path := range paths {
			go func() {
				<-start
				_, err := module.AddPath(context.Background(), path)
				errs <- err
			}()
		}
		close(start)
		for range paths {
			if err := <-errs; err != nil {
				t.Fatalf("adding path: %v", err)
			}
		}
		got, err := module.Paths(context.Background())
		if err != nil {
			t.Fatalf("listing paths: %v", err)
		}
		if len(got) != len(paths) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", paths, got)
		}
	})

	t.Run("should preserve overlapping adds from separate processes", func(t *testing.T) {
		configDir := t.TempDir()
		module := NewChrome(newChromeTestProxy(t, configDir), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		barrier := filepath.Join(configDir, "start-adds")
		executable, err := os.Executable()
		if err != nil {
			t.Fatalf("finding test executable: %v", err)
		}

		commands := make([]*exec.Cmd, 2)
		outputs := make([]bytes.Buffer, len(commands))
		for i := range commands {
			ready := filepath.Join(configDir, "ready-"+string(rune('0'+i)))
			command := exec.Command(executable, "-test.run=^TestChromeProcessAdd$")
			command.Env = append(os.Environ(),
				"MARASI_CHROME_PROCESS_CONFIG="+configDir,
				"MARASI_CHROME_PROCESS_READY="+ready,
				"MARASI_CHROME_PROCESS_BARRIER="+barrier,
				"MARASI_CHROME_PROCESS_PATH=/process/"+string(rune('0'+i)),
			)
			command.Stdout = &outputs[i]
			command.Stderr = &outputs[i]
			if err := command.Start(); err != nil {
				t.Fatalf("starting helper process: %v", err)
			}
			commands[i] = command
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("helper process did not become ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		if err := os.WriteFile(barrier, nil, 0600); err != nil {
			t.Fatalf("releasing helper processes: %v", err)
		}
		for i, command := range commands {
			if err := command.Wait(); err != nil {
				t.Fatalf("helper process failed: %v\n%s", err, outputs[i].String())
			}
		}

		paths, err := module.Paths(context.Background())
		if err != nil {
			t.Fatalf("listing paths: %v", err)
		}
		if len(paths) != 2 {
			t.Fatalf("\nwanted:\ntwo process additions\ngot:\n%v", paths)
		}
	})
}

func TestChromeProcessAdd(t *testing.T) {
	configDir := os.Getenv("MARASI_CHROME_PROCESS_CONFIG")
	if configDir == "" {
		return
	}
	if err := os.WriteFile(os.Getenv("MARASI_CHROME_PROCESS_READY"), nil, 0600); err != nil {
		t.Fatalf("writing process ready file: %v", err)
	}
	barrier := os.Getenv("MARASI_CHROME_PROCESS_BARRIER")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for process barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	module := NewChrome(newChromeTestProxy(t, configDir), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
	path := marasichrome.PathConfig{OS: "linux", Path: os.Getenv("MARASI_CHROME_PROCESS_PATH")}
	if _, err := module.AddPath(context.Background(), path); err != nil {
		t.Fatalf("adding process path: %v", err)
	}
}

func TestChromeLockCancellation(t *testing.T) {
	t.Run("should honor cancellation while waiting for the config file lock", func(t *testing.T) {
		configDir := t.TempDir()
		module := NewChrome(newChromeTestProxy(t, configDir), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		lock, err := os.OpenFile(filepath.Join(configDir, "marasi_config.yaml"), os.O_RDWR, 0600)
		if err != nil {
			t.Fatalf("opening config lock: %v", err)
		}
		defer lock.Close()
		if err := filelock.TryLock(lock); err != nil {
			t.Fatalf("locking config: %v", err)
		}
		defer filelock.Unlock(lock)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err = module.Paths(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("\nwanted:\ncontext deadline exceeded\ngot:\n%v", err)
		}
	})
}

func TestChromeStart(t *testing.T) {
	t.Run("should start the default profile against the active listener endpoint", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test executable is a shell script")
		}
		configDir := t.TempDir()
		argsPath := filepath.Join(configDir, "args")
		executable := filepath.Join(configDir, "chrome")
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n"
		if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
			t.Fatalf("writing fake Chrome: %v", err)
		}
		secondArgsPath := filepath.Join(configDir, "second-args")
		secondExecutable := filepath.Join(configDir, "second-chrome")
		secondScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + secondArgsPath + "\"\n"
		if err := os.WriteFile(secondExecutable, []byte(secondScript), 0700); err != nil {
			t.Fatalf("writing second fake Chrome: %v", err)
		}

		proxy := newChromeTestProxy(t, configDir)
		proxy.Addr = "stale.invalid"
		proxy.Port = "1"
		proxy.SPKIHash = "test-spki"
		module := NewChrome(proxy, &statusListener{status: activeListenerStatus("[::1]:43210")}, io.Discard)
		writer := NewChrome(newChromeTestProxy(t, configDir), &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		if _, err := writer.AddPath(context.Background(), marasichrome.PathConfig{OS: runtime.GOOS, Path: executable}); err != nil {
			t.Fatalf("adding executable path: %v", err)
		}
		if _, err := writer.AddPath(context.Background(), marasichrome.PathConfig{OS: runtime.GOOS, Path: secondExecutable}); err != nil {
			t.Fatalf("adding second executable path: %v", err)
		}

		profile, err := module.Start(context.Background(), "")
		if err != nil {
			t.Fatalf("starting Chrome: %v", err)
		}
		if profile != "default-profile" {
			t.Fatalf("\nwanted:\ndefault-profile\ngot:\n%s", profile)
		}

		var args []byte
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			args, err = os.ReadFile(argsPath)
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("reading fake Chrome arguments: %v", err)
		}
		if _, err := os.Stat(secondArgsPath); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nfirst configured executable to start\ngot second executable result:\n%v", err)
		}
		got := string(args)
		for _, want := range []string{
			"--proxy-server=http://[::1]:43210",
			"--ignore-certificate-errors-spki-list=test-spki",
			"--user-data-dir=" + filepath.Join(configDir, "chrome_profiles", "default-profile"),
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("\nwanted arguments containing:\n%s\ngot:\n%s", want, got)
			}
		}
		profiles, err := module.Profiles(context.Background())
		if err != nil || len(profiles) != 0 {
			t.Fatalf("\nwanted:\nno registered profiles\ngot:\n%v, %v", profiles, err)
		}
	})

	t.Run("should reject inactive listeners and missing named profiles", func(t *testing.T) {
		configDir := t.TempDir()
		proxy := newChromeTestProxy(t, configDir)
		inactive := NewChrome(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, io.Discard)
		if _, err := inactive.Start(context.Background(), " default-profile "); !errors.Is(err, ErrListenerInactive) {
			t.Fatalf("\nwanted:\nlistener inactive\ngot:\n%v", err)
		}

		active := NewChrome(proxy, &statusListener{status: activeListenerStatus("127.0.0.1:43210")}, io.Discard)
		if _, err := active.Start(context.Background(), "missing"); !errors.Is(err, ErrChromeNotFound) {
			t.Fatalf("\nwanted:\nchrome profile not found\ngot:\n%v", err)
		}
	})

	t.Run("should classify spawn failures and write the operating system error to the log", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("invalid executable behavior differs on Windows")
		}
		configDir := t.TempDir()
		badExecutable := filepath.Join(configDir, "bad-chrome")
		if err := os.WriteFile(badExecutable, []byte("not an executable format"), 0700); err != nil {
			t.Fatalf("writing bad executable: %v", err)
		}
		proxy := newChromeTestProxy(t, configDir)
		var log bytes.Buffer
		module := NewChrome(proxy, &statusListener{status: activeListenerStatus("127.0.0.1:43210")}, &log)
		if _, err := module.AddPath(context.Background(), marasichrome.PathConfig{OS: runtime.GOOS, Path: badExecutable}); err != nil {
			t.Fatalf("adding bad executable: %v", err)
		}

		if _, err := module.Start(context.Background(), "default-profile"); !errors.Is(err, ErrChromeUnavailable) {
			t.Fatalf("\nwanted:\nchrome unavailable\ngot:\n%v", err)
		}
		if !strings.Contains(log.String(), "starting Chrome:") || !strings.Contains(log.String(), "exec format error") {
			t.Fatalf("\nwanted:\noperating system spawn error in log\ngot:\n%s", log.String())
		}
	})
}
