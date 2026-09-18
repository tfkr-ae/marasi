package main

import (
	"net/http"
	"testing"
)

func TestWordlistListCommand(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	sent := startCannedControlAPI(t, configDir, "list", http.StatusOK, `{"items":[{"name":"passwords.txt","size":15},{"name":"users.txt","size":5}]}`+"\n")

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "list", "wordlist", "list")
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	request := sent.snapshot()
	if request.Method != http.MethodGet || request.Path != "/wordlist" || request.Body != "" {
		t.Fatalf("\nwanted:\nGET /wordlist with empty body\ngot:\n%s %s with body %q", request.Method, request.Path, request.Body)
	}
	wantStdout := "name: passwords.txt\nsize: 15\n\nname: users.txt\nsize: 5\n"
	if stdout != wantStdout || stderr != "" {
		t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", wantStdout, stdout, stderr)
	}
}

func TestWordlistPreviewCommand(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	sent := startCannedControlAPI(t, configDir, "preview", http.StatusOK, `{"name":"passwords.txt","items":["one","two"]}`+"\n")

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "preview", "wordlist", "preview", "passwords.txt")
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	request := sent.snapshot()
	if request.Method != http.MethodGet || request.Path != "/wordlist/passwords.txt" || request.Body != "" {
		t.Fatalf("\nwanted:\nGET /wordlist/passwords.txt with empty body\ngot:\n%s %s with body %q", request.Method, request.Path, request.Body)
	}
	if stdout != "one\ntwo\n" || stderr != "" {
		t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", "one\ntwo\n", stdout, stderr)
	}
}

func TestWordlistPreviewCommandPassesLimit(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	sent := startCannedControlAPI(t, configDir, "preview-limit", http.StatusOK, `{"name":"passwords.txt","items":["one"]}`+"\n")

	_, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "preview-limit", "wordlist", "preview", "passwords.txt", "--limit", "7")
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	request := sent.snapshot()
	if request.Method != http.MethodGet || request.Path != "/wordlist/passwords.txt" || request.RawQuery != "limit=7" {
		t.Fatalf("\nwanted:\nGET /wordlist/passwords.txt?limit=7\ngot:\n%s %s?%s", request.Method, request.Path, request.RawQuery)
	}
}

func TestWordlistCommandContract(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should pass successful responses through byte for byte in JSON mode", func(t *testing.T) {
		for index, args := range [][]string{{"wordlist", "list"}, {"wordlist", "preview", "passwords.txt"}} {
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

	t.Run("should report control API errors for invalid names and limits", func(t *testing.T) {
		for index, args := range [][]string{{"wordlist", "preview", "../passwords.txt"}, {"wordlist", "preview", "passwords.txt", "--limit", "0"}, {"wordlist", "preview", "passwords.txt", "--limit", "101"}} {
			configDir := serviceConfigDir(t)
			instanceName := "invalid-" + string(rune('a'+index))
			startCannedControlAPI(t, configDir, instanceName, http.StatusBadRequest, `{"error":"invalid_wordlist_request"}`)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", instanceName, "--json"}, args...)

			stdout, stderr, err := runMarasi(binary, commandArgs...)
			assertJSONCommandError(t, stdout, stderr, err, "previewing wordlist: invalid_wordlist_request")
		}
	})

	t.Run("should enforce command arguments", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{{"wordlist", "list", "extra"}, {"wordlist", "preview"}, {"wordlist", "preview", "one", "two"}, {"wordlist", "preview", "one", "--limit", "nope"}} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})
}
