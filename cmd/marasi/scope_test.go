package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestScopeCheckCommand(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should post the positional URL unchanged to the selected instance", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"in_scope":false,"tested_url":"https://example.com/path?q=1","rule":{"pattern":"^example\\.com$","match_type":"host"},"compass_enabled":false}` + "\n"
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
		input := "  example.com/path?q=1  "

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "scope", "check", input)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		request := sent.snapshot()
		if request.Method != http.MethodPost || request.Path != "/scope/check" || request.Body != `{"url":"  example.com/path?q=1  "}` || request.ContentType != "application/json" {
			t.Fatalf("\nwanted:\nPOST /scope/check with the unmodified URL and application/json\ngot:\n%s %s with body %q and content type %q", request.Method, request.Path, request.Body, request.ContentType)
		}
		want := "tested_url: https://example.com/path?q=1\nin_scope: false\nrule: ^example\\.com$\nmatch_type: host\ncompass_enabled: false\n"
		if stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", want, stdout, stderr)
		}
	})

	t.Run("should omit rule lines when the rule is null", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "default", http.StatusOK, `{"in_scope":true,"tested_url":"https://example.com","rule":null,"compass_enabled":true}`+"\n")

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "scope", "check", "https://example.com")
		want := "tested_url: https://example.com\nin_scope: true\ncompass_enabled: true\n"
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", want, stdout, stderr, err)
		}
	})

	t.Run("should pass the response through unchanged in JSON mode before or after the subcommand", func(t *testing.T) {
		body := " {\n  \"in_scope\": false, \"tested_url\": \"https://example.com\",\n  \"rule\": null, \"compass_enabled\": false\n} \n"
		for index, args := range [][]string{
			{"--json", "scope", "check", "https://example.com"},
			{"scope", "check", "https://example.com", "--json"},
		} {
			configDir := serviceConfigDir(t)
			name := []string{"json-before", "json-after"}[index]
			startCannedControlAPI(t, configDir, name, http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", name}, args...)

			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\n%v wanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", args, body, stdout, stderr, err)
			}
		}
	})

	t.Run("should fail without dialing when scope has no subcommand", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "scope")
		if err == nil || stderr != "" || !strings.Contains(stdout, "Available Commands:") || !strings.Contains(stdout, "marasi scope [command]") || len(sent.requests()) != 0 {
			t.Fatalf("\nwanted:\nhelp before dialing\ngot:\nstdout %q, stderr %q, error %v, requests %#v", stdout, stderr, err, sent.requests())
		}
		stdout, stderr, err = runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "scope")
		assertJSONCommandError(t, stdout, stderr, err, "marasi scope: choose check")
		if len(sent.requests()) != 0 {
			t.Fatalf("\nwanted:\nno requests\ngot:\n%#v", sent.requests())
		}
	})

	t.Run("should reject wrong positional counts and --url before dialing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{}`)
		for _, args := range [][]string{
			{"scope", "check"},
			{"scope", "check", "https://example.com", "extra"},
			{"scope", "check", "--url", "https://example.com"},
		} {
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
			stdout, _, err := runMarasi(binary, commandArgs...)
			if err == nil || stdout != "" || len(sent.requests()) != 0 {
				t.Fatalf("\nwanted:\ninvalid invocation before dialing\ngot:\nargs %v, stdout %q, error %v, requests %#v", args, stdout, err, sent.requests())
			}
		}
	})

	t.Run("should report API errors in human and JSON modes", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"bad_request"}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "scope", "check", "invalid")
		if err == nil || stdout != "" || !strings.Contains(stderr, "checking scope: bad_request") {
			t.Fatalf("\nwanted:\nAPI error on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		stdout, stderr, err = runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "work", "scope", "check", "invalid")
		assertJSONCommandError(t, stdout, stderr, err, "checking scope: bad_request")
	})

	t.Run("should report a missing instance without printing a result", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "scope", "check", "https://example.com")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance missing is not running") {
			t.Fatalf("\nwanted:\nmissing instance error on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		stdout, stderr, err = runMarasi(binary, "--json", "--config-dir", configDir, "--instance", "missing", "scope", "check", "https://example.com")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")
	})

	t.Run("should print readable command help", func(t *testing.T) {
		stdout, stderr, err := runMarasi(binary, "scope", "check", "--help")
		if err != nil || stderr != "" || !strings.Contains(stdout, "Check whether a URL is in scope") || !strings.Contains(stdout, "Usage:") {
			t.Fatalf("\nwanted:\nreadable scope check help\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})
}
