package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceStatusCommand(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should print exact status for the selected named instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		canonicalConfigDir, err := filepath.EvalSymlinks(configDir)
		if err != nil {
			t.Fatal(err)
		}
		project := filepath.Join(canonicalConfigDir, "juice-shop.marasi")
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, fmt.Sprintf(`{"status":"running","version":"13.09.2026","instance":"work","project":%q,"proxy_listener":"127.0.0.1:8080"}`, project))

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/service/status" || got.RawQuery != "" {
			t.Fatalf("\nwanted:\nGET /service/status\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
		want := fmt.Sprintf("status: running\nversion: 13.09.2026\ninstance: work\nproject: %s\nproxy listener: 127.0.0.1:8080\n", project)
		if stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", want, stdout, stderr)
		}
	})

	t.Run("should print an inactive proxy listener", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		canonicalConfigDir, err := filepath.EvalSymlinks(configDir)
		if err != nil {
			t.Fatal(err)
		}
		project := filepath.Join(canonicalConfigDir, "scratchpad.marasi")
		startCannedControlAPI(t, configDir, "work", http.StatusOK, fmt.Sprintf(`{"status":"running","version":"dev","instance":"work","project":%q,"proxy_listener":null}`, project))

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		want := fmt.Sprintf("status: running\nversion: dev\ninstance: work\nproject: %s\nproxy listener: inactive\n", project)
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should pass a successful response through byte for byte in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := " {\n  \"unexpected\": true\n} "
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status", "--json")
		if err != nil || stdout != body || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", body, stdout, stderr, err)
		}
	})

	t.Run("should report a missing named instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", " work ", "service", "status")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("\nwanted:\ninstance work is not running on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")
	})

	t.Run("should format API failures in human and JSON modes", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusInternalServerError, `{"error":"internal_server_error"}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		if err == nil || stdout != "" || !strings.Contains(stderr, "getting service status: 500 Internal Server Error") {
			t.Fatalf("\nwanted:\nHTTP status error on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "getting service status: internal_server_error")
	})

	for name, body := range map[string]string{
		"missing API error":    `{}`,
		"malformed API error":  `{`,
		"non-string API error": `{"error":42}`,
	} {
		t.Run("should use HTTP status for "+name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "work", http.StatusBadGateway, body)

			stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
			if err == nil || stdout != "" || !strings.Contains(stderr, "getting service status: 502 Bad Gateway") {
				t.Fatalf("\nwanted:\nHTTP status error on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
			}

			stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status", "--json")
			assertJSONCommandError(t, stdout, stderr, err, "getting service status: 502 Bad Gateway")
		})
	}

	t.Run("should reject a project path with an unresolved symlink", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		target := filepath.Join(configDir, "target.marasi")
		if err := os.WriteFile(target, nil, 0600); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(configDir, "alias.marasi")
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("creating symlink: %v", err)
		}
		body := fmt.Sprintf(`{"status":"running","version":"dev","instance":"work","project":%q,"proxy_listener":null}`, alias)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		if err == nil || stdout != "" {
			t.Fatalf("\nwanted:\ncanonical project error with empty stdout\ngot:\nstdout %q, error %v", stdout, err)
		}
	})

	for name, body := range map[string]string{
		"malformed JSON":         `{`,
		"missing field":          fmt.Sprintf(`{"status":"running","version":"dev","instance":"work","project":%q}`, filepath.Join(string(filepath.Separator), "work", "scratchpad.marasi")),
		"wrong status":           fmt.Sprintf(`{"status":"stopped","version":"dev","instance":"work","project":%q,"proxy_listener":null}`, filepath.Join(string(filepath.Separator), "work", "scratchpad.marasi")),
		"empty field":            fmt.Sprintf(`{"status":"running","version":"","instance":"work","project":%q,"proxy_listener":null}`, filepath.Join(string(filepath.Separator), "work", "scratchpad.marasi")),
		"non-canonical instance": fmt.Sprintf(`{"status":"running","version":"dev","instance":" work ","project":%q,"proxy_listener":null}`, filepath.Join(string(filepath.Separator), "work", "scratchpad.marasi")),
		"non-canonical project":  `{"status":"running","version":"dev","instance":"work","project":"scratchpad.marasi","proxy_listener":null}`,
	} {
		t.Run("should reject "+name+" in human mode and pass it through in JSON mode", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

			stdout, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
			if err == nil || stdout != "" {
				t.Fatalf("\nwanted:\nhuman rendering error with empty stdout\ngot:\nstdout %q, error %v", stdout, err)
			}

			stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status", "--json")
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\nwanted:\nJSON passthrough %q\ngot:\nstdout %q, stderr %q, error %v", body, stdout, stderr, err)
			}
		})
	}

	t.Run("should reject positional arguments and command-specific flags", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"--config-dir", configDir, "service", "status", "extra"},
			{"--config-dir", configDir, "service", "status", "--project", "scratchpad"},
		} {
			if _, _, err := runMarasi(binary, args...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should query a running built service", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		projectDir, err := os.MkdirTemp(".", "status-project-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(projectDir) })
		project := filepath.Join(projectDir, "juice-shop.marasi")
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

		_, startStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--project", project, "--port", "0")
		if err != nil {
			t.Fatalf("starting service: %v", err)
		}
		const prefix = "proxy listener started on "
		index := strings.Index(startStderr, prefix)
		if index < 0 {
			t.Fatalf("\nwanted:\nproxy listener startup output\ngot:\n%s", startStderr)
		}
		listener := strings.TrimSpace(startStderr[index+len(prefix):])
		absoluteProject, err := filepath.Abs(project)
		if err != nil {
			t.Fatal(err)
		}
		canonicalProject, err := filepath.EvalSymlinks(absoluteProject)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("status: running\nversion: dev\ninstance: work\nproject: %s\nproxy listener: %s\n", canonicalProject, listener)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})
}

func TestServiceListCommand(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should list running instances by name and ignore --instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		canonicalConfigDir, err := filepath.EvalSymlinks(configDir)
		if err != nil {
			t.Fatal(err)
		}
		project := filepath.Join(canonicalConfigDir, "scratchpad.marasi")
		alpha := startCannedControlAPI(t, configDir, "alpha", http.StatusOK, fmt.Sprintf(`{"status":"running","version":"dev","instance":"alpha","project":%q,"proxy_listener":null}`, project))
		zeta := startCannedControlAPI(t, configDir, "zeta", http.StatusOK, fmt.Sprintf(`{"status":"running","version":"13.09.2026","instance":"zeta","project":%q,"proxy_listener":"127.0.0.1:8080"}`, project))
		instancesDir := filepath.Join(configDir, "instances")
		for _, name := range []string{"orphan.lock", "orphan.log"} {
			if err := os.WriteFile(filepath.Join(instancesDir, name), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", strings.Repeat("x", 120), "service", "list")
		want := fmt.Sprintf("status: running\nversion: dev\ninstance: alpha\nproject: %s\nproxy listener: inactive\n\nstatus: running\nversion: 13.09.2026\ninstance: zeta\nproject: %s\nproxy listener: 127.0.0.1:8080\n", project, project)
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
		for name, response := range map[string]*cannedControlRequest{"alpha": alpha, "zeta": zeta} {
			got := response.snapshot()
			if got.Method != http.MethodGet || got.Path != "/service/status" || got.RawQuery != "" {
				t.Errorf("%s: wanted GET /service/status, got %s %s?%s", name, got.Method, got.Path, got.RawQuery)
			}
		}
	})

	t.Run("should omit stale sockets and mark unreadable status responses unhealthy", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		canonicalConfigDir, err := filepath.EvalSymlinks(configDir)
		if err != nil {
			t.Fatal(err)
		}
		project := filepath.Join(canonicalConfigDir, "scratchpad.marasi")
		startCannedControlAPI(t, configDir, "bad", http.StatusInternalServerError, `{"error":"internal_server_error"}`)
		startCannedControlAPIHandler(t, configDir, "dropped", func(w http.ResponseWriter, _ *http.Request) {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijacking control connection: %v", err)
				return
			}
			connection.Close()
		})
		startCannedControlAPI(t, configDir, "garbage", http.StatusOK, `{"unexpected":true}`)
		startCannedControlAPI(t, configDir, "running", http.StatusOK, fmt.Sprintf(`{"status":"running","version":"dev","instance":"running","project":%q,"proxy_listener":null}`, project))
		if err := os.WriteFile(filepath.Join(configDir, "instances", "stale.sock"), nil, 0600); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "service", "list")
		want := fmt.Sprintf("status: unhealthy\ninstance: bad\n\nstatus: unhealthy\ninstance: dropped\n\nstatus: unhealthy\ninstance: garbage\n\nstatus: running\nversion: dev\ninstance: running\nproject: %s\nproxy listener: inactive\n", project)
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should omit a socket removed before its dial", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		canonicalConfigDir, err := filepath.EvalSymlinks(configDir)
		if err != nil {
			t.Fatal(err)
		}
		project := filepath.Join(canonicalConfigDir, "scratchpad.marasi")
		zetaSocketPath, _, err := instanceResourcePaths(configDir, "zeta")
		if err != nil {
			t.Fatal(err)
		}
		startCannedControlAPI(t, configDir, "zeta", http.StatusOK, fmt.Sprintf(`{"status":"running","version":"dev","instance":"zeta","project":%q,"proxy_listener":null}`, project))
		startCannedControlAPIHandler(t, configDir, "alpha", func(w http.ResponseWriter, _ *http.Request) {
			if err := os.Remove(zetaSocketPath); err != nil {
				t.Errorf("removing zeta socket: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"running","version":"dev","instance":"alpha","project":%q,"proxy_listener":null}`, project)
		})

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "service", "list")
		want := fmt.Sprintf("status: running\nversion: dev\ninstance: alpha\nproject: %s\nproxy listener: inactive\n", project)
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("wanted stdout %q, empty stderr, nil error; got stdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should print one JSON items object with both row shapes", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		canonicalConfigDir, err := filepath.EvalSymlinks(configDir)
		if err != nil {
			t.Fatal(err)
		}
		project := filepath.Join(canonicalConfigDir, "scratchpad.marasi")
		startCannedControlAPI(t, configDir, "broken", http.StatusInternalServerError, `{"error":"internal_server_error"}`)
		startCannedControlAPI(t, configDir, "running", http.StatusOK, fmt.Sprintf(`{"status":"running","version":"dev","instance":"running","project":%q,"proxy_listener":null}`, project))
		want := fmt.Sprintf("{\"items\":[{\"status\":\"unhealthy\",\"instance\":\"broken\"},{\"status\":\"running\",\"version\":\"dev\",\"instance\":\"running\",\"project\":%q,\"proxy_listener\":null}]}\n", project)
		for _, args := range [][]string{
			{"--json", "--config-dir", configDir, "service", "list"},
			{"--config-dir", configDir, "service", "list", "--json"},
		} {
			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil || stdout != want || stderr != "" {
				t.Fatalf("%v: wanted stdout %q, empty stderr, nil error; got stdout %q, stderr %q, error %v", args, want, stdout, stderr, err)
			}
		}
	})

	t.Run("should treat a missing instances directory as an empty list without creating it", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		instancesDir := filepath.Join(configDir, "instances")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "service", "list")
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("wanted empty success, got stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		if _, err := os.Stat(instancesDir); !os.IsNotExist(err) {
			t.Fatalf("wanted no instances directory, got stat error %v", err)
		}

		stdout, stderr, err = runMarasi(binary, "--json", "--config-dir", configDir, "service", "list")
		if err != nil || stdout != "{\"items\":[]}\n" || stderr != "" {
			t.Fatalf("wanted empty JSON list, got stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("should report directory read failures and reject invalid invocations before scanning", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		instancesDir := filepath.Join(configDir, "instances")
		if err := os.WriteFile(instancesDir, nil, 0600); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "service", "list")
		if err == nil || stdout != "" || !strings.Contains(stderr, instancesDir) {
			t.Fatalf("wanted directory error naming %q and no rows, got stdout %q, stderr %q, error %v", instancesDir, stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--json", "--config-dir", configDir, "service", "list")
		assertJSONCommandError(t, stdout, stderr, err, instancesDir)

		for _, args := range [][]string{
			{"--config-dir", configDir, "service", "list", "extra"},
			{"--config-dir", configDir, "service", "list", "--project", "scratchpad"},
		} {
			stdout, stderr, err = runMarasi(binary, args...)
			if err == nil || stdout != "" || strings.Contains(stderr, instancesDir) {
				t.Fatalf("%v should fail before reading %q, got stdout %q, stderr %q, error %v", args, instancesDir, stdout, stderr, err)
			}
		}

		stdout, stderr, err = runMarasi(binary, "--json", "--config-dir", configDir, "service", "list", "--help")
		if err != nil || stderr != "" || !strings.Contains(stdout, "List service instances") || strings.HasPrefix(stdout, "{") {
			t.Fatalf("wanted human help in JSON mode, got stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})
}
