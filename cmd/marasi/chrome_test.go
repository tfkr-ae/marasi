package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestChromePathCommand(t *testing.T) {
	binary := buildMarasi(t)

	for _, test := range []struct {
		name       string
		instance   string
		args       []string
		response   string
		method     string
		body       string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "should add a path using this operating system by default",
			instance:   "add",
			args:       []string{"chrome", "path", "add", "--path", "/custom/chrome"},
			response:   `{"items":[{"os":"` + runtime.GOOS + `","path":"/custom/chrome"}]}` + "\n",
			method:     http.MethodPost,
			body:       `{"os":"` + runtime.GOOS + `","path":"/custom/chrome"}`,
			wantStderr: "chrome path added\n",
		},
		{
			name:       "should remove a path for the selected operating system",
			instance:   "remove",
			args:       []string{"chrome", "path", "remove", "--path", "/custom/chrome", "--os", "linux"},
			response:   `{"items":[]}` + "\n",
			method:     http.MethodDelete,
			body:       `{"os":"linux","path":"/custom/chrome"}`,
			wantStderr: "chrome path removed\n",
		},
		{
			name:       "should list paths as blocks",
			instance:   "list",
			args:       []string{"chrome", "path", "list"},
			response:   `{"items":[{"os":"darwin","path":"/Applications/Chrome"},{"os":"linux","path":"/usr/bin/chromium"}]}` + "\n",
			method:     http.MethodGet,
			wantStdout: "os: darwin\npath: /Applications/Chrome\n\nos: linux\npath: /usr/bin/chromium\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, test.instance, http.StatusOK, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", test.instance}, test.args...)

			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			if got.Method != test.method || got.Path != "/chrome/path" || got.Body != test.body || got.ContentType != requestContentType(test.body) {
				t.Fatalf("\nwanted:\n%s /chrome/path body %q content type %q\ngot:\n%s %s body %q content type %q", test.method, test.body, requestContentType(test.body), got.Method, got.Path, got.Body, got.ContentType)
			}
			if stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q", test.wantStdout, test.wantStderr, stdout, stderr)
			}
		})
	}
}

func TestChromeProfileAndStartCommands(t *testing.T) {
	binary := buildMarasi(t)

	for _, test := range []struct {
		name       string
		instance   string
		args       []string
		response   string
		method     string
		path       string
		body       string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "should add a profile",
			instance:   "profile-add",
			args:       []string{"chrome", "profile", "add", "pentest"},
			response:   `{"items":[{"name":"pentest"}]}` + "\n",
			method:     http.MethodPost,
			path:       "/chrome/profile",
			body:       `{"name":"pentest"}`,
			wantStderr: "chrome profile pentest added\n",
		},
		{
			name:       "should remove a profile",
			instance:   "profile-remove",
			args:       []string{"chrome", "profile", "remove", "pentest"},
			response:   `{"items":[]}` + "\n",
			method:     http.MethodDelete,
			path:       "/chrome/profile/pentest",
			wantStderr: "chrome profile pentest removed\n",
		},
		{
			name:       "should list profiles one per line",
			instance:   "profile-list",
			args:       []string{"chrome", "profile", "list"},
			response:   `{"items":[{"name":"pentest"},{"name":"admin"}]}` + "\n",
			method:     http.MethodGet,
			path:       "/chrome/profile",
			wantStdout: "pentest\nadmin\n",
		},
		{
			name:       "should start with an omitted profile",
			instance:   "start-default",
			args:       []string{"chrome", "start"},
			response:   `{"status":"started","profile":"default-profile"}` + "\n",
			method:     http.MethodPost,
			path:       "/chrome/start",
			body:       `{}`,
			wantStderr: "chrome started with profile default-profile\n",
		},
		{
			name:       "should start with a selected profile",
			instance:   "start-profile",
			args:       []string{"chrome", "start", "--profile", "pentest"},
			response:   `{"status":"started","profile":"pentest"}` + "\n",
			method:     http.MethodPost,
			path:       "/chrome/start",
			body:       `{"profile":"pentest"}`,
			wantStderr: "chrome started with profile pentest\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, test.instance, http.StatusOK, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", test.instance}, test.args...)

			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := sent.snapshot()
			if got.Method != test.method || got.Path != test.path || got.Body != test.body || got.ContentType != requestContentType(test.body) {
				t.Fatalf("\nwanted:\n%s %s body %q content type %q\ngot:\n%s %s body %q content type %q", test.method, test.path, test.body, requestContentType(test.body), got.Method, got.Path, got.Body, got.ContentType)
			}
			if stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q", test.wantStdout, test.wantStderr, stdout, stderr)
			}
		})
	}
}

func TestChromeCommandContract(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should pass every successful response through byte for byte in JSON mode", func(t *testing.T) {
		commands := [][]string{
			{"chrome", "path", "add", "--path", "/chrome"},
			{"chrome", "path", "remove", "--path", "/chrome"},
			{"chrome", "path", "list"},
			{"chrome", "profile", "add", "pentest"},
			{"chrome", "profile", "remove", "pentest"},
			{"chrome", "profile", "list"},
			{"chrome", "start"},
		}
		for index, args := range commands {
			configDir := serviceConfigDir(t)
			instanceName := "json-" + string(rune('a'+index))
			body := " {\n  \"unexpected\": true\n} "
			startCannedControlAPI(t, configDir, instanceName, http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", instanceName, "--json"}, args...)

			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\n%v wanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", args, body, stdout, stderr, err)
			}
		}
	})

	t.Run("should normalize unavailable instances and API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "chrome", "path", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

		for _, test := range []struct {
			name      string
			args      []string
			operation string
		}{
			{"path list", []string{"chrome", "path", "list"}, "listing chrome paths"},
			{"path add", []string{"chrome", "path", "add", "--path", "/chrome"}, "adding chrome path"},
			{"path remove", []string{"chrome", "path", "remove", "--path", "/chrome"}, "removing chrome path"},
			{"profile list", []string{"chrome", "profile", "list"}, "listing chrome profiles"},
			{"profile add", []string{"chrome", "profile", "add", "pentest"}, "adding chrome profile"},
			{"profile remove", []string{"chrome", "profile", "remove", "pentest"}, "removing chrome profile"},
			{"start", []string{"chrome", "start"}, "starting chrome"},
		} {
			t.Run(test.name, func(t *testing.T) {
				configDir := serviceConfigDir(t)
				startCannedControlAPI(t, configDir, "api", http.StatusConflict, `{"error":"listener_inactive"}`)
				args := append([]string{"--config-dir", configDir, "--instance", "api", "--json"}, test.args...)
				stdout, stderr, err := runMarasi(binary, args...)
				assertJSONCommandError(t, stdout, stderr, err, test.operation+": listener_inactive")
			})
		}

		configDir = serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "malformed", http.StatusBadGateway, `{`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "malformed", "chrome", "start", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "starting chrome: 502 Bad Gateway")
	})

	t.Run("should enforce command arguments and required flags", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"chrome", "path", "add"},
			{"chrome", "path", "remove"},
			{"chrome", "path", "list", "extra"},
			{"chrome", "profile", "add"},
			{"chrome", "profile", "add", "one", "two"},
			{"chrome", "profile", "remove"},
			{"chrome", "profile", "list", "extra"},
			{"chrome", "start", "extra"},
			{"chrome", "start", "--unknown"},
		} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should send an explicitly empty start profile", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "empty", http.StatusBadRequest, `{"error":"invalid_chrome_request"}`)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "empty", "chrome", "start", "--profile=", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "starting chrome: invalid_chrome_request")
		if got := sent.snapshot().Body; got != `{"profile":""}` {
			t.Fatalf("\nwanted:\nexplicit empty profile body\ngot:\n%s", got)
		}
	})

	t.Run("should normalize cancellation", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		started, release := startBlockingControlAPI(t, configDir, "work")
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "chrome", "path", "list", "--json")
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatalf("starting chrome path list command: %v", err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			command.Process.Kill()
			t.Fatal("chrome request did not reach the control API")
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			command.Process.Kill()
			t.Fatalf("interrupting chrome command: %v", err)
		}
		err := command.Wait()
		close(release)
		assertJSONCommandError(t, stdout.String(), stderr.String(), err, "context canceled")
	})

	t.Run("should leave empty lists empty", func(t *testing.T) {
		for _, args := range [][]string{{"chrome", "path", "list"}, {"chrome", "profile", "list"}} {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "empty-list", http.StatusOK, `{"items":[]}`+"\n")
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "empty-list"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != "" || stderr != "" {
				t.Fatalf("\n%v wanted:\nempty streams, nil error\ngot:\nstdout %q, stderr %q, error %v", args, stdout, stderr, err)
			}
		}
	})
}

func TestChromeCommandLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses a shell executable")
	}
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

	_, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--project-name", "chrome-cli", "--port", "0")
	if err != nil {
		t.Fatalf("starting service: %v", err)
	}

	fakeChrome := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(fakeChrome, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing fake Chrome executable: %v", err)
	}

	_, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "chrome", "path", "add", "--path", fakeChrome)
	if err != nil || stderr != "chrome path added\n" {
		t.Fatalf("adding Chrome path: stderr %q, error %v", stderr, err)
	}
	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "chrome", "path", "list")
	wantPaths := "os: " + runtime.GOOS + "\npath: " + fakeChrome + "\n"
	if err != nil || stdout != wantPaths || stderr != "" {
		t.Fatalf("listing Chrome paths: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	_, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "chrome", "profile", "add", "pentest")
	if err != nil || stderr != "chrome profile pentest added\n" {
		t.Fatalf("adding Chrome profile: stderr %q, error %v", stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "chrome", "profile", "list")
	if err != nil || stdout != "pentest\n" || stderr != "" {
		t.Fatalf("listing Chrome profiles: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	_, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "chrome", "start", "--profile", "pentest")
	if err != nil || stderr != "chrome started with profile pentest\n" {
		t.Fatalf("starting Chrome: stderr %q, error %v", stderr, err)
	}
	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "stop"); err != nil {
		t.Fatalf("stopping listener: %v", err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "chrome", "start", "--json")
	assertJSONCommandError(t, stdout, stderr, err, "starting chrome: listener_inactive")

	_, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "chrome", "path", "remove", "--path", fakeChrome)
	if err != nil || stderr != "chrome path removed\n" {
		t.Fatalf("removing Chrome path: stderr %q, error %v", stderr, err)
	}
	_, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "chrome", "profile", "remove", "pentest")
	if err != nil || stderr != "chrome profile pentest removed\n" {
		t.Fatalf("removing Chrome profile: stderr %q, error %v", stderr, err)
	}
	for _, args := range [][]string{{"chrome", "path", "list"}, {"chrome", "profile", "list"}} {
		commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
		stdout, stderr, err := runMarasi(binary, commandArgs...)
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("checking removal with %v: stdout %q, stderr %q, error %v", args, stdout, stderr, err)
		}
	}
}

func requestContentType(body string) string {
	if body == "" {
		return ""
	}
	return "application/json"
}
