package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const upstreamTestPEM = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"

func TestUpstreamCommands(t *testing.T) {
	binary := buildMarasi(t)
	caDir := t.TempDir()
	caFile := filepath.Join(caDir, "burp.pem")
	if err := os.WriteFile(caFile, []byte(upstreamTestPEM), 0600); err != nil {
		t.Fatalf("writing CA file: %v", err)
	}
	listed := `{"items":[{"name":"burp","scheme":"http","host":"127.0.0.1","port":8080,"has_credentials":true,"has_ca":true},{"name":"jump","scheme":"socks5","host":"::1","port":1080,"has_credentials":false,"has_ca":false}]}` + "\n"

	for _, test := range []struct {
		name       string
		args       []string
		method     string
		path       string
		body       string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "should list entries as a table without secrets",
			args:       []string{"upstream", "list"},
			method:     http.MethodGet,
			path:       "/upstream-proxy",
			wantStdout: "NAME  SCHEME  HOST       PORT  CREDENTIALS  CA\nburp  http    127.0.0.1  8080  yes          yes\njump  socks5  ::1        1080  no           no\nNAME  SCHEME  HOST       PORT  CREDENTIALS  CA\n",
		},
		{
			name:       "should add an entry with a URL",
			args:       []string{"upstream", "add", "burp", "http://alice:hunter2@127.0.0.1:8080"},
			method:     http.MethodPost,
			path:       "/upstream-proxy",
			body:       `{"name":"burp","url":"http://alice:hunter2@127.0.0.1:8080"}`,
			wantStderr: "upstream proxy burp added\n",
		},
		{
			name:       "should add an entry with the CA file's PEM",
			args:       []string{"upstream", "add", "burp", "http://127.0.0.1:8080", "--ca", caFile},
			method:     http.MethodPost,
			path:       "/upstream-proxy",
			body:       `{"name":"burp","url":"http://127.0.0.1:8080","ca":"-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"}`,
			wantStderr: "upstream proxy burp added\n",
		},
		{
			name:       "should update the URL",
			args:       []string{"upstream", "update", "burp", "--url", "socks5://127.0.0.1:1080"},
			method:     http.MethodPost,
			path:       "/upstream-proxy/burp",
			body:       `{"url":"socks5://127.0.0.1:1080"}`,
			wantStderr: "upstream proxy burp updated\n",
		},
		{
			name:       "should update the CA",
			args:       []string{"upstream", "update", "burp", "--ca", caFile},
			method:     http.MethodPost,
			path:       "/upstream-proxy/burp",
			body:       `{"ca":"-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"}`,
			wantStderr: "upstream proxy burp updated\n",
		},
		{
			name:       "should remove the CA",
			args:       []string{"upstream", "update", "burp", "--no-ca"},
			method:     http.MethodPost,
			path:       "/upstream-proxy/burp",
			body:       `{"ca":null}`,
			wantStderr: "upstream proxy burp updated\n",
		},
		{
			name:       "should update the URL and remove the CA together",
			args:       []string{"upstream", "update", "burp", "--url", "http://127.0.0.1:8081", "--no-ca"},
			method:     http.MethodPost,
			path:       "/upstream-proxy/burp",
			body:       `{"url":"http://127.0.0.1:8081","ca":null}`,
			wantStderr: "upstream proxy burp updated\n",
		},
		{
			name:       "should remove an entry",
			args:       []string{"upstream", "remove", "burp"},
			method:     http.MethodDelete,
			path:       "/upstream-proxy/burp",
			wantStderr: "upstream proxy burp removed\n",
		},
		{
			name:       "should select an upstream proxy for the open project",
			args:       []string{"upstream", "use", "burp"},
			method:     http.MethodPut,
			path:       "/upstream-proxy-selection",
			body:       `{"name":"burp"}`,
			wantStderr: "upstream proxy burp selected\n",
		},
		{
			name:       "should clear the selection",
			args:       []string{"upstream", "use", "--none"},
			method:     http.MethodPut,
			path:       "/upstream-proxy-selection",
			body:       `{"name":""}`,
			wantStderr: "upstream proxy selection cleared: connecting directly\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "up", http.StatusOK, listed)
			args := append([]string{"--config-dir", configDir, "--instance", "up"}, test.args...)

			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v %s", err, stderr)
			}
			got := sent.snapshot()
			if got.Method != test.method || got.Path != test.path || got.Body != test.body || got.ContentType != requestContentType(test.body) {
				t.Fatalf("\nwanted:\n%s %s body %q content type %q\ngot:\n%s %s body %q content type %q", test.method, test.path, test.body, requestContentType(test.body), got.Method, got.Path, got.Body, got.ContentType)
			}
			if stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q", test.wantStdout, test.wantStderr, stdout, stderr)
			}
			for _, secret := range []string{"alice", "hunter2", "BEGIN CERTIFICATE"} {
				if strings.Contains(stdout+stderr, secret) {
					t.Fatalf("\nwanted:\nno %q in output\ngot:\nstdout %q, stderr %q", secret, stdout, stderr)
				}
			}
		})
	}
}

func TestUpstreamCommandContract(t *testing.T) {
	binary := buildMarasi(t)
	caFile := filepath.Join(t.TempDir(), "burp.pem")
	if err := os.WriteFile(caFile, []byte(upstreamTestPEM), 0600); err != nil {
		t.Fatalf("writing CA file: %v", err)
	}

	t.Run("should pass every successful response through byte for byte in JSON mode", func(t *testing.T) {
		for index, args := range [][]string{
			{"upstream", "list"},
			{"upstream", "add", "burp", "http://127.0.0.1:8080"},
			{"upstream", "add", "burp", "http://127.0.0.1:8080", "--ca", caFile},
			{"upstream", "update", "burp", "--url", "http://127.0.0.1:8081"},
			{"upstream", "update", "burp", "--no-ca"},
			{"upstream", "remove", "burp"},
			{"upstream", "use", "burp"},
			{"upstream", "use", "--none"},
		} {
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

	t.Run("should map API refusals to errors", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			args     []string
			status   int
			response string
			want     string
		}{
			{"list unavailable", []string{"upstream", "list"}, http.StatusInternalServerError, `{"error":"internal_server_error"}`, "listing upstream proxies: internal_server_error"},
			{"add duplicate", []string{"upstream", "add", "burp", "http://127.0.0.1:8080"}, http.StatusConflict, `{"error":"upstream_proxy_already_exists"}`, "adding upstream proxy: upstream_proxy_already_exists"},
			{"add invalid", []string{"upstream", "add", "burp", "ftp://127.0.0.1:21"}, http.StatusBadRequest, `{"error":"invalid_upstream_proxy","message":"invalid upstream proxy: the URL scheme must be http, https or socks5"}`, "adding upstream proxy: invalid_upstream_proxy: invalid upstream proxy: the URL scheme must be http, https or socks5"},
			{"update missing", []string{"upstream", "update", "burp", "--url", "http://127.0.0.1:8080"}, http.StatusNotFound, `{"error":"not_found"}`, "updating upstream proxy: not_found"},
			{"remove missing", []string{"upstream", "remove", "burp"}, http.StatusNotFound, `{"error":"not_found"}`, "removing upstream proxy: not_found"},
			{"use missing", []string{"upstream", "use", "burp"}, http.StatusNotFound, `{"error":"not_found"}`, "selecting upstream proxy: not_found"},
		} {
			t.Run(test.name, func(t *testing.T) {
				configDir := serviceConfigDir(t)
				startCannedControlAPI(t, configDir, "api", test.status, test.response)
				args := append([]string{"--config-dir", configDir, "--instance", "api", "--json"}, test.args...)
				stdout, stderr, err := runMarasi(binary, args...)
				assertJSONCommandError(t, stdout, stderr, err, test.want)
			})
		}

		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "upstream", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")
	})

	t.Run("should report an unreadable CA file without calling the instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "ca", http.StatusOK, `{"items":[]}`)
		missing := filepath.Join(t.TempDir(), "missing.pem")
		for _, args := range [][]string{
			{"upstream", "add", "burp", "http://127.0.0.1:8080", "--ca", missing},
			{"upstream", "update", "burp", "--ca", missing},
		} {
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "ca", "--json"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			assertJSONCommandError(t, stdout, stderr, err, "reading CA file")
		}
		if got := sent.snapshot(); got.Method != "" {
			t.Fatalf("\nwanted:\nno request\ngot:\n%s %s", got.Method, got.Path)
		}
	})

	t.Run("should enforce arguments and flags", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"upstream", "list", "extra"},
			{"upstream", "add"},
			{"upstream", "add", "burp"},
			{"upstream", "add", "burp", "http://127.0.0.1:8080", "extra"},
			{"upstream", "update"},
			{"upstream", "update", "burp"},
			{"upstream", "update", "burp", "--ca", "x.pem", "--no-ca"},
			{"upstream", "remove"},
			{"upstream", "remove", "burp", "extra"},
			{"upstream", "use"},
			{"upstream", "use", "burp", "--none"},
			{"upstream", "use", "burp", "jump"},
		} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should leave an empty list empty", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "empty", http.StatusOK, `{"items":[]}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "empty", "upstream", "list")
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nempty streams, nil error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})
}
