package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestJobsSettingsAndLaunchpadUnlinkCommands(t *testing.T) {
	binary := buildMarasi(t)
	valueFile := filepath.Join(t.TempDir(), "value.json")
	if err := os.WriteFile(valueFile, []byte("{\"columns\": [\"host\"]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setting := `{"client":"marasi-app","key":"ledger/view","value":{"columns":["host"]},"updated_at":"2026-10-15T00:00:00Z"}`
	for _, test := range []struct {
		name     string
		args     []string
		status   int
		response string
		method   string
		path     string
		query    string
		body     string
		stdout   string
		stderr   string
	}{
		{
			name: "jobs list", args: []string{"jobs", "list"},
			status: http.StatusOK, response: `{"items":[{"name":"traffic_index","complete":true,"done":10,"total":10},{"name":"sitemap","complete":false,"done":3,"total":10}]}`,
			method: http.MethodGet, path: "/jobs",
			stdout: "traffic_index  complete  10/10\nsitemap        running   3/10\n",
		},
		{
			name: "jobs rebuild", args: []string{"jobs", "rebuild", "sitemap"},
			status: http.StatusAccepted, response: `{"name":"sitemap","complete":false,"done":0,"total":10}`,
			method: http.MethodPost, path: "/jobs/sitemap/rebuild",
			stderr: "job sitemap rebuild started\n",
		},
		{
			name: "settings list", args: []string{"settings", "list", "marasi-app", "--prefix", "ledger/"},
			status: http.StatusOK, response: `{"items":[` + setting + `]}`,
			method: http.MethodGet, path: "/client-settings/marasi-app", query: "prefix=ledger%2F",
			stdout: "ledger/view  {\"columns\":[\"host\"]}\n",
		},
		{
			name: "settings get", args: []string{"settings", "get", "marasi-app", "ledger/view"},
			status: http.StatusOK, response: setting,
			method: http.MethodGet, path: "/client-settings/marasi-app/ledger/view",
			stdout: "{\"columns\":[\"host\"]}\n",
		},
		{
			name: "settings set", args: []string{"settings", "set", "marasi-app", "ledger/view", `{"columns": ["host"]}`},
			status: http.StatusOK, response: setting,
			method: http.MethodPut, path: "/client-settings/marasi-app/ledger/view", body: `{"value":{"columns":["host"]}}`,
			stderr: "setting marasi-app ledger/view set\n",
		},
		{
			name: "settings set from file", args: []string{"settings", "set", "marasi-app", "ledger/view", "--file", valueFile},
			status: http.StatusOK, response: setting,
			method: http.MethodPut, path: "/client-settings/marasi-app/ledger/view", body: `{"value":{"columns":["host"]}}`,
			stderr: "setting marasi-app ledger/view set\n",
		},
		{
			name: "settings delete", args: []string{"settings", "delete", "marasi-app", "ledger/view"},
			status: http.StatusOK, response: `{"client":"marasi-app","key":"ledger/view"}`,
			method: http.MethodDelete, path: "/client-settings/marasi-app/ledger/view",
			stderr: "setting marasi-app ledger/view deleted\n",
		},
		{
			name: "launchpad unlink", args: []string{"launchpad", "unlink", bulkAsOf, bulkIDA},
			status: http.StatusOK, response: `{"launchpad_id":"` + bulkAsOf + `","request_id":"` + bulkIDA + `"}`,
			method: http.MethodDelete, path: "/launchpad/" + bulkAsOf + "/link/" + bulkIDA,
			stderr: "request " + bulkIDA + " unlinked from launchpad " + bulkAsOf + " successfully\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", test.status, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasi(binary, args...)
			got := sent.snapshot()
			if err != nil || stdout != test.stdout || stderr != test.stderr {
				t.Fatalf("\nwanted:\nstdout %q, stderr %q\ngot:\nstdout %q, stderr %q, error %v", test.stdout, test.stderr, stdout, stderr, err)
			}
			if got.Method != test.method || got.Path != test.path || got.RawQuery != test.query || got.Body != test.body {
				t.Fatalf("\nwanted:\n%s %s?%s body %q\ngot:\n%s %s?%s body %q", test.method, test.path, test.query, test.body, got.Method, got.Path, got.RawQuery, got.Body)
			}
			stdout, stderr, err = runMarasi(binary, append(args, "--json")...)
			if err != nil || stdout != test.response || stderr != "" {
				t.Fatalf("\nwanted:\nstdout %q\ngot:\nstdout %q, stderr %q, error %v", test.response, stdout, stderr, err)
			}
		})
	}
}

func TestJobsSettingsAndLaunchpadUnlinkUsage(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{}`)
	for _, args := range [][]string{
		{"jobs", "list", "extra"},
		{"jobs", "rebuild"},
		{"settings", "list"},
		{"settings", "get", "marasi-app"},
		{"settings", "set", "marasi-app", "k"},
		{"settings", "set", "marasi-app", "k", "not json"},
		{"settings", "set", "marasi-app", "k", "1", "--file", "/dev/null"},
		{"settings", "delete", "marasi-app"},
		{"launchpad", "unlink", bulkAsOf},
		{"launchpad", "unlink", "junk", bulkIDA},
		{"launchpad", "unlink", bulkAsOf, "junk"},
	} {
		commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
		if stdout, stderr, err := runMarasi(binary, commandArgs...); err == nil || stdout != "" {
			t.Fatalf("\nwanted:\nusage error for %v\ngot:\nstdout %q, stderr %q", args, stdout, stderr)
		}
		if got := sent.snapshot(); got.Method != "" {
			t.Fatalf("\nwanted:\nno request for %v\ngot:\n%s %s", args, got.Method, got.Path)
		}
	}
}
