package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestFindingCommands(t *testing.T) {
	binary := buildMarasi(t)
	id := "01938032-1b17-7243-b035-e6a9f4645904"
	testCaseID := "0193802f-f0e7-73d9-a764-06d21e367809"

	t.Run("create sends every supplied field and reports on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		response := `{"id":"` + id + `","test_case_id":"` + testCaseID + `","title":"Access control","severity":"High","cvss_vector":"CVSS:3.1/example","cvss_score":8.1,"writeup":"Details","treatment_plan":"Fix it","created_at":"2026-09-16T10:00:00Z"}` + "\n"
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "finding", "create", "--title", "Access control", "--severity", "High", "--cvss-vector", "CVSS:3.1/example", "--cvss-score", "8.1", "--writeup", "Details", "--treatment-plan", "Fix it", "--test-case", testCaseID)
		if err != nil {
			t.Fatalf("wanted nil error, got %v", err)
		}
		got := sent.snapshot()
		wantBody := `{"title":"Access control","severity":"High","cvss_vector":"CVSS:3.1/example","cvss_score":8.1,"writeup":"Details","treatment_plan":"Fix it","test_case_id":"` + testCaseID + `"}`
		if got.Method != http.MethodPost || got.Path != "/finding" || got.Body != wantBody {
			t.Fatalf("wanted POST /finding body %q, got %s %s body %q", wantBody, got.Method, got.Path, got.Body)
		}
		if stdout != "" || stderr != "finding "+id+" created successfully\n" {
			t.Fatalf("wanted mutation on stderr, got stdout %q stderr %q", stdout, stderr)
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
			args:       []string{"finding", "list"},
			response:   `{"items":[{"id":"` + id + `","title":"Access control","severity":"High","test_case_id":"` + testCaseID + `","cvss_score":8.1,"created_at":"2026-09-16T10:00:00Z"}]}` + "\n",
			method:     http.MethodGet,
			path:       "/finding",
			wantStdout: id + "  Access control  High  " + testCaseID + "  8.1  2026-09-16T10:00:00Z\n",
		},
		{
			name:       "get",
			args:       []string{"finding", "get", id},
			response:   `{"id":"` + id + `","test_case_id":null,"title":"Access control","severity":"High","cvss_vector":"CVSS:3.1/example","cvss_score":8.1,"writeup":"Details","treatment_plan":"Fix it","created_at":"2026-09-16T10:00:00Z","items":[],"artifacts":[]}` + "\n",
			method:     http.MethodGet,
			path:       "/finding/" + id,
			wantStdout: "id: " + id + "\ntest_case_id: \ntitle: Access control\nseverity: High\ncvss_vector: CVSS:3.1/example\ncvss_score: 8.1\nwriteup: Details\ntreatment_plan: Fix it\ncreated_at: 2026-09-16T10:00:00Z\n",
		},
		{
			name:       "update clears test case",
			args:       []string{"finding", "update", id, "--clear-test-case"},
			response:   `{"id":"` + id + `","test_case_id":null,"title":"Access control","severity":"High","cvss_vector":"","cvss_score":0,"writeup":"","treatment_plan":"","created_at":"2026-09-16T10:00:00Z"}` + "\n",
			method:     http.MethodPost,
			path:       "/finding/" + id,
			body:       `{"test_case_id":null}`,
			wantStderr: "finding " + id + " updated successfully\n",
		},
		{
			name:       "delete",
			args:       []string{"finding", "delete", id},
			response:   `{"id":"` + id + `"}` + "\n",
			method:     http.MethodDelete,
			path:       "/finding/" + id,
			wantStderr: "finding " + id + " deleted successfully\n",
		},
		{
			name:       "link",
			args:       []string{"finding", "link", id, "--request", testCaseID},
			response:   `{"finding_id":"` + id + `","id":"` + testCaseID + `"}` + "\n",
			method:     http.MethodPost,
			path:       "/finding/" + id + "/traffic",
			body:       `{"id":"` + testCaseID + `"}`,
			wantStderr: "request " + testCaseID + " linked to finding " + id + " successfully\n",
		},
		{
			name:       "unlink",
			args:       []string{"finding", "unlink", id, "--request", testCaseID},
			response:   `{"finding_id":"` + id + `","id":"` + testCaseID + `"}` + "\n",
			method:     http.MethodDelete,
			path:       "/finding/" + id + "/traffic/" + testCaseID,
			wantStderr: "request " + testCaseID + " unlinked from finding " + id + " successfully\n",
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

	t.Run("passes json bytes through before or after the group", func(t *testing.T) {
		for _, args := range [][]string{{"--json", "finding", "list"}, {"finding", "list", "--json"}} {
			configDir := serviceConfigDir(t)
			body := " {\n  \"items\": []\n} "
			startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("wanted passthrough %q, got stdout %q stderr %q error %v", body, stdout, stderr, err)
			}
		}
	})

	t.Run("rejects missing or conflicting flags before dialing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"finding", "create"}, {"finding", "create", "--title", ""}, {"finding", "update", id},
			{"finding", "update", id, "--title", ""}, {"finding", "update", id, "--test-case", testCaseID, "--clear-test-case"},
			{"finding", "link", id}, {"finding", "unlink", id},
		} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("wanted invalid invocation, accepted %v", args)
			}
		}
	})

	t.Run("reports missing instances and json API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "finding", "list")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("wanted missing instance on stderr, got stdout %q stderr %q error %v", stdout, stderr, err)
		}

		startCannedControlAPI(t, configDir, "api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "api", "finding", "get", id, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "getting finding: not_found")
	})
}
