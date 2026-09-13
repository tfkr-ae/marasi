package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestServiceStatusCommand(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should print exact status for the selected named instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"status":"running","version":"13.09.2026","instance":"work","project":"juice-shop","proxy_listener":"127.0.0.1:8080"}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if sent.Method != http.MethodGet || sent.Path != "/service/status" || sent.RawQuery != "" {
			t.Fatalf("\nwanted:\nGET /service/status\ngot:\n%s %s?%s", sent.Method, sent.Path, sent.RawQuery)
		}
		want := "status: running\nversion: 13.09.2026\ninstance: work\nproject: juice-shop\nproxy listener: 127.0.0.1:8080\n"
		if stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", want, stdout, stderr)
		}
	})

	t.Run("should print an inactive proxy listener", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"status":"running","version":"dev","instance":"work","project":"scratchpad","proxy_listener":null}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		want := "status: running\nversion: dev\ninstance: work\nproject: scratchpad\nproxy listener: inactive\n"
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

	for name, body := range map[string]string{
		"malformed JSON":         `{`,
		"missing field":          `{"status":"running","version":"dev","instance":"work","project":"scratchpad"}`,
		"wrong status":           `{"status":"stopped","version":"dev","instance":"work","project":"scratchpad","proxy_listener":null}`,
		"empty field":            `{"status":"running","version":"","instance":"work","project":"scratchpad","proxy_listener":null}`,
		"non-canonical instance": `{"status":"running","version":"dev","instance":" work ","project":"scratchpad","proxy_listener":null}`,
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
		t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

		_, startStderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--project", "juice-shop", "--port", "0")
		if err != nil {
			t.Fatalf("starting service: %v", err)
		}
		const prefix = "proxy listener started on "
		index := strings.Index(startStderr, prefix)
		if index < 0 {
			t.Fatalf("\nwanted:\nproxy listener startup output\ngot:\n%s", startStderr)
		}
		listener := strings.TrimSpace(startStderr[index+len(prefix):])
		want := fmt.Sprintf("status: running\nversion: dev\ninstance: work\nproject: juice-shop\nproxy listener: %s\n", listener)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "status")
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})
}
