package main

import (
	"net/http"
	"testing"
)

func TestTestCaseChecklistCommand(t *testing.T) {
	binary := buildMarasi(t)
	body := `{"title":"Team checklist","description":"Current scope","version":"2","items":[{"title":"Check authorization","description":"Try another user","category":"Access control"},{"title":"Check headers","description":"Inspect responses","category":"Configuration"}]}` + "\n"

	t.Run("prints item titles and categories on stdout", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "test-case", "checklist")

		if err != nil {
			t.Fatalf("wanted nil error, got %v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/test-case/checklist" || got.Body != "" {
			t.Fatalf("wanted GET /test-case/checklist, got %s %s body %q", got.Method, got.Path, got.Body)
		}
		want := "Check authorization  Access control\nCheck headers        Configuration\n"
		if stdout != want || stderr != "" {
			t.Fatalf("wanted stdout %q and empty stderr, got stdout %q stderr %q", want, stdout, stderr)
		}
	})

	t.Run("passes API bytes through in json mode", func(t *testing.T) {
		for _, args := range [][]string{{"--json", "test-case", "checklist"}, {"test-case", "checklist", "--json"}} {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)

			stdout, stderr, err := runMarasi(binary, commandArgs...)

			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("wanted passthrough %q, empty stderr, nil error; got stdout %q stderr %q error %v", body, stdout, stderr, err)
			}
		}
	})

	t.Run("reports a missing instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "test-case", "checklist", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")
	})
}
