package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestReadCommands(t *testing.T) {
	binary := buildMarasi(t)
	id := "01938032-1b17-7243-b035-e6a9f4645904"
	for _, test := range []struct {
		name      string
		args      []string
		operation string
		empty     string
	}{
		{"finding requests", []string{"finding", "list-requests", id}, "listing finding requests", `{"items":[]}`},
		{"test case requests", []string{"test-case", "list-requests", id}, "listing test-case requests", `{"items":[]}`},
		{"notes get", []string{"notes", "get", id}, "getting note", ""},
		{"artifact list", []string{"artifact", "list"}, "listing artifacts", `{"items":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := serviceConfigDir(t)
			prefix := []string{"--config-dir", configDir, "--instance", "work"}
			args := append(prefix, test.args...)
			stdout, stderr, err := runMarasi(binary, args...)
			if err == nil || stdout != "" || !strings.Contains(stderr, "instance work is not running") {
				t.Fatalf("wanted missing instance, got %q %q %v", stdout, stderr, err)
			}
			startCannedControlAPI(t, configDir, "work", http.StatusNotFound, `{"error":"not_found"}`)
			stdout, stderr, err = runMarasi(binary, append(args, "--json")...)
			assertJSONCommandError(t, stdout, stderr, err, test.operation+": not_found")
			if test.empty != "" {
				emptyDir := serviceConfigDir(t)
				startCannedControlAPI(t, emptyDir, "work", http.StatusOK, test.empty)
				emptyArgs := append([]string{"--config-dir", emptyDir, "--instance", "work"}, test.args...)
				stdout, stderr, err = runMarasi(binary, emptyArgs...)
				if err != nil || stdout != "" || stderr != "" {
					t.Fatalf("wanted empty human list, got %q %q %v", stdout, stderr, err)
				}
				stdout, stderr, err = runMarasi(binary, append(emptyArgs, "--json")...)
				if err != nil || strings.TrimSpace(stdout) != test.empty || stderr != "" {
					t.Fatalf("wanted empty JSON list %q, got %q %q %v", test.empty, stdout, stderr, err)
				}
			}
		})
	}
	for _, args := range [][]string{
		{"finding", "list-requests"}, {"finding", "list-requests", "junk"}, {"finding", "list-requests", id, id},
		{"test-case", "list-requests"}, {"test-case", "list-requests", "junk"}, {"test-case", "list-requests", id, id},
		{"notes", "get"}, {"notes", "get", "junk"}, {"notes", "get", id, id}, {"artifact", "list", id},
	} {
		stdout, stderr, err := runMarasi(binary, append([]string{"--config-dir", serviceConfigDir(t)}, args...)...)
		if err == nil || stdout != "" || strings.Contains(stderr, "not running") {
			t.Fatalf("wanted validation before dialing for %v, got %q %q %v", args, stdout, stderr, err)
		}
	}
}
