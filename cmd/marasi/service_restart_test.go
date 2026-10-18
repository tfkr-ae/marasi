package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildMarasiVersion builds the marasi command with version set to v.
func buildMarasiVersion(t *testing.T, v string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "marasi")
	command := exec.Command("go", "build", "-ldflags", "-X main.version="+v, "-o", binary, ".")
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("\nwanted:\nbuilt marasi command\ngot:\n%s\n%v", output, err)
	}
	return binary
}

// installBuild replaces the installed build at installed with the file at next,
// the way an update does: a rename over the running executable.
func installBuild(t *testing.T, next, installed string) {
	t.Helper()
	if err := os.Rename(next, installed); err != nil {
		t.Fatalf("installing build: %v", err)
	}
}

type restartedStatus struct {
	Status        string  `json:"status"`
	Version       string  `json:"version"`
	Instance      string  `json:"instance"`
	Project       string  `json:"project"`
	ProxyListener *string `json:"proxy_listener"`
}

func readInstanceStatus(t *testing.T, socketPath string) restartedStatus {
	t.Helper()
	code, body := doInstanceRequest(t, socketPath, http.MethodGet, "/service/status", "")
	if code != http.StatusOK {
		t.Fatalf("status: %d %s", code, body)
	}
	var status restartedStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decoding status %q: %v", body, err)
	}
	return status
}

// readEventNamed reads events until one named want.
func readEventNamed(t *testing.T, reader *bufio.Reader, want string) (string, string) {
	t.Helper()
	for range 50 {
		name, data := readControlEvent(t, reader)
		if name == want {
			return name, data
		}
	}
	t.Fatalf("wanted %s event", want)
	return "", ""
}

func TestServiceRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("restart fixture renames over a running executable and uses Unix control sockets")
	}

	t.Run("should hand the name, project, and listener to a successor from the installed build", func(t *testing.T) {
		binary := buildMarasiVersion(t, "old-build")
		next := buildMarasiVersion(t, "new-build")
		configDir := serviceConfigDir(t)
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })
		proxyAddress := startNamedInstance(t, binary, configDir, "work", "--project-name", "restart")
		socketPath := filepath.Join(configDir, "instances", "work.sock")
		before := readInstanceStatus(t, socketPath)
		installBuild(t, next, binary)

		stdout, stderr, err := runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "service", "restart")
		if err != nil || stderr != "" {
			t.Fatalf("restart: stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		var result struct {
			Instance      string  `json:"instance"`
			Status        string  `json:"status"`
			Version       string  `json:"version"`
			ProxyListener *string `json:"proxy_listener"`
		}
		if err := json.Unmarshal([]byte(stdout), &result); err != nil || strings.Count(stdout, "\n") != 1 {
			t.Fatalf("restart output %q: %v", stdout, err)
		}
		if result.Instance != "work" || result.Status != "restarted" || result.Version != "new-build" || result.ProxyListener == nil || *result.ProxyListener != proxyAddress {
			t.Fatalf("\nwanted:\nwork restarted on new-build at %s\ngot:\n%s", proxyAddress, stdout)
		}

		after := readInstanceStatus(t, socketPath)
		if after.Version != "new-build" || after.Instance != "work" || after.Project != before.Project || after.ProxyListener == nil || *after.ProxyListener != proxyAddress {
			t.Fatalf("\nwanted:\nnew-build serving work, %s, %s\ngot:\n%+v", before.Project, proxyAddress, after)
		}
		getViaProxy(t, proxyAddress, "http://127.0.0.1:1/")
	})

	t.Run("should keep the old build serving when the successor exits at start", func(t *testing.T) {
		binary := buildMarasiVersion(t, "old-build")
		// The CLI runs from a copy, since the installed build becomes a broken one.
		cli := filepath.Join(t.TempDir(), "marasi")
		contents, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cli, contents, 0700); err != nil {
			t.Fatal(err)
		}
		configDir := serviceConfigDir(t)
		t.Cleanup(func() { runMarasi(cli, "--config-dir", configDir, "--instance", "work", "service", "stop") })
		proxyAddress := startNamedInstance(t, binary, configDir, "work", "--project-name", "restart")
		socketPath := filepath.Join(configDir, "instances", "work.sock")
		before := readInstanceStatus(t, socketPath)
		_, events, closeEvents := connectInstanceEvents(t, socketPath)
		defer closeEvents()

		broken := filepath.Join(t.TempDir(), "broken")
		if err := os.WriteFile(broken, []byte("#!/bin/sh\necho broken build >&2\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		installBuild(t, broken, binary)

		logPath := filepath.Join(configDir, "instances", "work.log")
		stdout, stderr, err := runMarasi(cli, "--json", "--config-dir", configDir, "--instance", "work", "service", "restart")
		assertJSONCommandError(t, stdout, stderr, err, "successor log: "+logPath)

		name, data := readEventNamed(t, events, "service.restarting")
		if name != "service.restarting" {
			t.Fatalf("wanted service.restarting, got %s %s", name, data)
		}
		name, data = readEventNamed(t, events, "service.restart_failed")
		var failed struct {
			Instance string `json:"instance"`
			Error    string `json:"error"`
			Log      string `json:"log"`
		}
		if err := json.Unmarshal([]byte(data), &failed); err != nil || failed.Instance != "work" || failed.Log != logPath || failed.Error == "" {
			t.Fatalf("\nwanted:\nservice.restart_failed for work with log %s\ngot:\n%s %s", logPath, name, data)
		}
		logContents, err := os.ReadFile(logPath)
		if err != nil || !strings.Contains(string(logContents), "broken build") {
			t.Fatalf("wanted the successor's output in %s, got %q, %v", logPath, logContents, err)
		}

		after := readInstanceStatus(t, socketPath)
		if after.Version != "old-build" || after.Project != before.Project || after.ProxyListener == nil || *after.ProxyListener != proxyAddress {
			t.Fatalf("\nwanted:\nold-build still serving %s at %s\ngot:\n%+v", before.Project, proxyAddress, after)
		}
		getViaProxy(t, proxyAddress, "http://127.0.0.1:1/")
		stdout, stderr, err = runMarasi(cli, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("wanted the reopened project to serve traffic, got stdout %q, stderr %q, %v", stdout, stderr, err)
		}
	})

	t.Run("should refuse while Checkpoint items are held unless forced", func(t *testing.T) {
		binary := buildMarasi(t)
		configDir := serviceConfigDir(t)
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })
		proxyAddress := startNamedInstance(t, binary, configDir, "work", "--project-name", "restart")
		socketPath := filepath.Join(configDir, "instances", "work.sock")
		if _, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "intercept", "on"); err != nil {
			t.Fatalf("enabling intercept: %s %v", stderr, err)
		}
		held := startProxyGet(t, proxyAddress, "http://127.0.0.1:1/held")
		id := waitForCheckpointID(t, binary, configDir, "request")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "restart")
		wantRefusal := restartWarning +
			"Error: restarting service: 1 Checkpoint items are held:\n" +
			"  " + id + " request GET http://127.0.0.1:1/held HTTP/1.1\n" +
			"forward or drop them, or rerun with --force to drop them\n"
		if err == nil || stdout != "" || stderr != wantRefusal {
			t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q, stderr %q, error %v", wantRefusal, stdout, stderr, err)
		}
		stdout, _, err = runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "service", "restart")
		assertJSONCommandError(t, stdout, "", err, "restarting service: checkpoint_items_held")
		if got := waitForCheckpointID(t, binary, configDir, "request"); got != id {
			t.Fatalf("wanted %s still held after the refusal, got %s", id, got)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "restart", "--force")
		wantRestart := restartWarning + "instance work restarted on version dev\nproxy listener: " + proxyAddress + "\n"
		if err != nil || stdout != "" || stderr != wantRestart {
			t.Fatalf("\nwanted:\n%s\ngot:\nstdout %q, stderr %q, error %v", wantRestart, stdout, stderr, err)
		}
		<-held
		stdout, _, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "checkpoint", "list")
		if err != nil || stdout != "" {
			t.Fatalf("wanted no held items in the successor, got %q, %v", stdout, err)
		}
		if status := readInstanceStatus(t, socketPath); status.ProxyListener == nil || *status.ProxyListener != proxyAddress {
			t.Fatalf("wanted the successor on %s, got %+v", proxyAddress, status)
		}
	})

	t.Run("should tell the user to start an instance when none is running", func(t *testing.T) {
		binary := buildMarasi(t)
		configDir := serviceConfigDir(t)
		want := "instance work is not running; start one with marasi service start --instance work"
		stdout, stderr, err := runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "service", "restart")
		assertJSONCommandError(t, stdout, stderr, err, want)
	})

	t.Run("should start the successor with no proxy listener when it was stopped", func(t *testing.T) {
		binary := buildMarasi(t)
		configDir := serviceConfigDir(t)
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })
		startNamedInstance(t, binary, configDir, "work", "--project-name", "restart")
		socketPath := filepath.Join(configDir, "instances", "work.sock")
		if code, body := doInstanceRequest(t, socketPath, http.MethodPost, "/listener/stop", ""); code != http.StatusOK {
			t.Fatalf("stopping listener: %d %s", code, body)
		}

		stdout, stderr, err := runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "service", "restart")
		if err != nil || stderr != "" || stdout != `{"instance":"work","status":"restarted","version":"dev","proxy_listener":null}`+"\n" {
			t.Fatalf("restart: stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		if status := readInstanceStatus(t, socketPath); status.ProxyListener != nil {
			t.Fatalf("wanted the successor with no proxy listener, got %+v", status)
		}
	})
}
