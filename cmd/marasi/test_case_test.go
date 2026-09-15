package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestTestCaseCommands(t *testing.T) {
	binary := buildMarasi(t)
	id := "01938032-1b17-7243-b035-e6a9f4645904"

	t.Run("create sends every supplied field and reports the mutation on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		response := `{"id":"` + id + `","title":"Access control","description":"Try another user","category":"Web","tags":["auth","idor"],"note":"Started","created_at":"2026-09-16T10:00:00Z"}` + "\n"
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "test-case", "create", "--title", "Access control", "--description", "Try another user", "--category", "Web", "--tag", "auth", "--tag", "idor", "--note", "Started")
		if err != nil {
			t.Fatalf("wanted nil error, got %v", err)
		}
		got := sent.snapshot()
		wantBody := `{"title":"Access control","description":"Try another user","category":"Web","tags":["auth","idor"],"note":"Started"}`
		if got.Method != http.MethodPost || got.Path != "/test-case" || got.Body != wantBody {
			t.Fatalf("wanted POST /test-case body %q, got %s %s body %q", wantBody, got.Method, got.Path, got.Body)
		}
		if stdout != "" || stderr != "test case "+id+" created successfully\n" {
			t.Fatalf("wanted empty stdout and mutation on stderr, got stdout %q stderr %q", stdout, stderr)
		}
	})

	for _, test := range []struct {
		name       string
		args       []string
		response   string
		method     string
		path       string
		body       string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "list",
			args:       []string{"test-case", "list"},
			response:   `{"items":[{"id":"` + id + `","title":"Access control","category":"Web","tags":["auth"],"created_at":"2026-09-16T10:00:00Z"}]}` + "\n",
			method:     http.MethodGet,
			path:       "/test-case",
			wantStdout: id + "  Access control  Web  auth  2026-09-16T10:00:00Z\n",
		},
		{
			name:       "get",
			args:       []string{"test-case", "get", id},
			response:   `{"id":"` + id + `","title":"Access control","description":"Try another user","category":"Web","tags":["auth"],"note":"Started","created_at":"2026-09-16T10:00:00Z","items":[],"artifacts":[]}` + "\n",
			method:     http.MethodGet,
			path:       "/test-case/" + id,
			wantStdout: "id: " + id + "\ntitle: Access control\ndescription: Try another user\ncategory: Web\ntags: auth\nnote: Started\ncreated_at: 2026-09-16T10:00:00Z\n",
		},
		{
			name:       "update",
			args:       []string{"test-case", "update", id, "--title", "Authorization"},
			response:   `{"id":"` + id + `","title":"Authorization","description":"Try another user","category":"Web","tags":["auth"],"note":"Started","created_at":"2026-09-16T10:00:00Z"}` + "\n",
			method:     http.MethodPost,
			path:       "/test-case/" + id,
			body:       `{"title":"Authorization"}`,
			wantStderr: "test case " + id + " updated successfully\n",
		},
		{
			name:       "delete",
			args:       []string{"test-case", "delete", id},
			response:   `{"id":"` + id + `"}` + "\n",
			method:     http.MethodDelete,
			path:       "/test-case/" + id,
			wantStderr: "test case " + id + " deleted successfully\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, test.response)
			args := append([]string{"--config-dir", configDir, "--instance", "work"}, test.args...)
			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil {
				t.Fatalf("wanted nil error, got %v", err)
			}
			got := sent.snapshot()
			if got.Method != test.method || got.Path != test.path || got.Body != test.body {
				t.Fatalf("wanted %s %s body %q, got %s %s body %q", test.method, test.path, test.body, got.Method, got.Path, got.Body)
			}
			if stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("wanted stdout %q stderr %q, got stdout %q stderr %q", test.wantStdout, test.wantStderr, stdout, stderr)
			}
		})
	}

	t.Run("passes API bytes through with json before or after the group", func(t *testing.T) {
		for _, args := range [][]string{{"--json", "test-case", "list"}, {"test-case", "list", "--json"}} {
			configDir := serviceConfigDir(t)
			body := " {\n  \"items\": []\n} "
			startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("wanted passthrough %q, empty stderr, nil error; got stdout %q stderr %q error %v", body, stdout, stderr, err)
			}
		}
	})

	t.Run("rejects missing create and update flags before dialing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{{"test-case", "create"}, {"test-case", "create", "--title", ""}, {"test-case", "update", id}, {"test-case", "update", id, "--title", ""}} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("wanted invalid invocation, accepted %v", args)
			}
		}
	})

	t.Run("reports a missing instance and normalizes json API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "test-case", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")

		startCannedControlAPI(t, configDir, "api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "api", "test-case", "get", id, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "getting test case: not_found")
	})

	t.Run("human missing instance names the instance on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "test-case", "list")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("wanted missing instance on stderr, got stdout %q stderr %q error %v", stdout, stderr, err)
		}
	})
}
