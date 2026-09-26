package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestLogsCommand(t *testing.T) {
	const (
		newID       = "01938032-1b17-7243-b035-e6a9f4645904"
		oldID       = "0193802f-f0e7-73d9-a764-06d21e367809"
		requestID   = "01938030-0000-7000-8000-000000000001"
		extensionID = "01938031-0000-7000-8000-000000000001"
		body        = `{"items":[{"id":"` + newID + `","timestamp":"2026-01-02T03:04:06Z","level":"FATAL","message":"new proxy failure","context":{"secret":"hidden"},"request_id":null,"extension_id":null},{"id":"` + oldID + `","timestamp":"2026-01-02T03:04:05Z","level":"DEBUG","message":"older request","context":{},"request_id":"` + requestID + `","extension_id":"` + extensionID + `"}],"next_cursor":"` + oldID + `"}` + "\n"
	)
	binary := buildMarasi(t)

	t.Run("should print the page oldest first with optional ids and next cursor", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "logs")
		if err != nil {
			t.Fatalf("wanted no error, got %v", err)
		}
		want := "2026-01-02T03:04:05Z DEBUG older request request=" + requestID + " extension=" + extensionID + "\n" +
			"2026-01-02T03:04:06Z FATAL new proxy failure\n"
		if stdout != want || stderr != "next_cursor="+oldID+"\n" {
			t.Fatalf("wanted stdout %q and stderr %q, got stdout %q and stderr %q", want, "next_cursor="+oldID+"\n", stdout, stderr)
		}
		if strings.Contains(stdout, "hidden") {
			t.Fatalf("human output included context: %q", stdout)
		}
	})

	t.Run("should default to 200 and print nothing for an empty page", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "logs")
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("wanted empty output and no error, got stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/logs" || got.RawQuery != "limit=200" {
			t.Fatalf("wanted GET /logs?limit=200, got %s %s?%s", got.Method, got.Path, got.RawQuery)
		}
	})

	t.Run("should forward limit and cursor", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)
		_, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "logs", "--limit", "12", "--cursor", oldID)
		if err != nil {
			t.Fatalf("wanted no error, got %v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/logs" || got.RawQuery != "cursor="+oldID+"&limit=12" {
			t.Fatalf("wanted GET /logs?cursor=%s&limit=12, got %s %s?%s", oldID, got.Method, got.Path, got.RawQuery)
		}
	})

	for _, position := range []string{"before", "after"} {
		t.Run("should pass the API body through with --json "+position+" logs", func(t *testing.T) {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			args := []string{"--config-dir", configDir, "--instance", "work"}
			if position == "before" {
				args = append(args, "--json")
			}
			args = append(args, "logs")
			if position == "after" {
				args = append(args, "--json")
			}
			stdout, stderr, err := runMarasi(binary, args...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("wanted unchanged API body and no stderr, got stdout %q, stderr %q, error %v", stdout, stderr, err)
			}
		})
	}

	t.Run("should name the missing service instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "logs")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("wanted the missing instance error, got stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("should write a JSON error when the service instance is missing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "logs")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")
	})

	t.Run("should format JSON control API errors by operation", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"bad_request"}`)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "logs", "--json", "--limit", "0")
		assertJSONCommandError(t, stdout, stderr, err, "listing logs: bad_request")
	})

	t.Run("should reject extra arguments and short flags before dialing", func(t *testing.T) {
		for _, args := range [][]string{{"extra"}, {"-l", "2"}} {
			configDir := serviceConfigDir(t)
			sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work", "logs"}, args...)
			_, _, err := runMarasi(binary, commandArgs...)
			if err == nil || len(sent.requests()) != 0 {
				t.Fatalf("wanted argument error before dialing for %v, got error %v and requests %+v", args, err, sent.requests())
			}
		}
	})

	t.Run("should keep help human-readable in JSON mode", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--json", "--config-dir", configDir, "logs", "--help")
		if err != nil || stderr != "" || !strings.Contains(stdout, "List proxy logs") || strings.HasPrefix(stdout, "{") {
			t.Fatalf("wanted human-readable help, got stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})
}
