package main

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
		{
			name:       "should start with a CDP endpoint and print its HTTP URL",
			instance:   "start-cdp",
			args:       []string{"chrome", "start", "--cdp"},
			response:   `{"status":"started","profile":"default-profile","cdp":{"http":"http://127.0.0.1:53211","ws":"ws://127.0.0.1:53211/devtools/browser/0b5e1a52","reused":false}}` + "\n",
			method:     http.MethodPost,
			path:       "/chrome/start",
			body:       `{"cdp":true}`,
			wantStderr: "chrome started with profile default-profile\nCDP endpoint http://127.0.0.1:53211\n",
		},
		{
			name:       "should start a selected profile with a CDP endpoint",
			instance:   "start-cdp-profile",
			args:       []string{"chrome", "start", "--profile", "pentest", "--cdp"},
			response:   `{"status":"started","profile":"pentest","cdp":{"http":"http://127.0.0.1:53211","ws":"ws://127.0.0.1:53211/devtools/browser/0b5e1a52","reused":false}}` + "\n",
			method:     http.MethodPost,
			path:       "/chrome/start",
			body:       `{"profile":"pentest","cdp":true}`,
			wantStderr: "chrome started with profile pentest\nCDP endpoint http://127.0.0.1:53211\n",
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
			{"chrome", "start", "--cdp"},
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

	t.Run("should list --cdp in start help", func(t *testing.T) {
		stdout, _, err := runMarasi(binary, "chrome", "start", "--help")
		if err != nil || !strings.Contains(stdout, "--cdp") || !strings.Contains(stdout, "CDP endpoint") {
			t.Fatalf("\nwanted:\nhelp listing --cdp and the CDP endpoint\ngot:\n%s, %v", stdout, err)
		}
		for _, avoided := range []string{"debugging port", "DevTools port", "remote debugging URL"} {
			if strings.Contains(stdout, avoided) {
				t.Fatalf("\nwanted:\nhelp without %q\ngot:\n%s", avoided, stdout)
			}
		}
	})

	t.Run("should report a reused CDP endpoint", func(t *testing.T) {
		reused := `{"status":"started","profile":"pentest","cdp":{"http":"http://127.0.0.1:53211","ws":"ws://127.0.0.1:53211/devtools/browser/0b5e1a52","reused":true}}` + "\n"

		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "reused-json", http.StatusOK, reused)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "reused-json", "chrome", "start", "--profile", "pentest", "--cdp", "--json")
		if err != nil || stdout != reused || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", reused, stdout, stderr, err)
		}

		configDir = serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "reused-human", http.StatusOK, reused)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "reused-human", "chrome", "start", "--profile", "pentest", "--cdp")
		want := "chrome started with profile pentest\nCDP endpoint http://127.0.0.1:53211 (reused)\n"
		if err != nil || stdout != "" || stderr != want {
			t.Fatalf("\nwanted:\nempty stdout, stderr %q, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should report a profile running without CDP", func(t *testing.T) {
		refusal := `{"error":"chrome_running_without_cdp","message":"chrome profile pentest is running without a CDP endpoint; close it and start it again with --cdp"}`
		want := "starting chrome: chrome_running_without_cdp: chrome profile pentest is running without a CDP endpoint; close it and start it again with --cdp"

		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "no-cdp", http.StatusConflict, refusal)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "no-cdp", "chrome", "start", "--profile", "pentest", "--cdp", "--json")
		assertJSONCommandError(t, stdout, stderr, err, want)

		configDir = serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "no-cdp-human", http.StatusConflict, refusal)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "no-cdp-human", "chrome", "start", "--profile", "pentest", "--cdp")
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || stdout != "" || !strings.Contains(stderr, want) {
			t.Fatalf("\nwanted:\nexit status 1 and stderr containing %q\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
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
