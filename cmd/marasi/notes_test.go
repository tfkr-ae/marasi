package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNotesCommands(t *testing.T) {
	binary := buildMarasi(t)
	id := "0193802f-f0e7-73d9-a764-06d21e367809"
	setBody := `{"id":"` + id + `","note":"needs review"}` + "\n"
	clearBody := `{"id":"` + id + `"}` + "\n"

	t.Run("should set a note from a positional string", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "set", http.StatusOK, setBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "set", "notes", "set", id, "needs review")
		got := sent.snapshot()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.Method != http.MethodPut || got.Path != "/notes/"+id || got.Body != `{"note":"needs review"}` || got.ContentType != "application/json" {
			t.Fatalf("\nwanted:\nPUT /notes/%s body %q\ngot:\n%s %s body %q type %q", id, `{"note":"needs review"}`, got.Method, got.Path, got.Body, got.ContentType)
		}
		if stdout != "" || stderr != "note "+id+" set\n" {
			t.Fatalf("\nwanted:\nempty stdout, stderr %q\ngot:\nstdout %q, stderr %q", "note "+id+" set\n", stdout, stderr)
		}
	})

	t.Run("should set a note from --file", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		file := filepath.Join(t.TempDir(), "note.txt")
		if err := os.WriteFile(file, []byte("needs review"), 0o600); err != nil {
			t.Fatalf("writing note file: %v", err)
		}
		sent := startCannedControlAPI(t, configDir, "file", http.StatusOK, setBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "file", "notes", "set", id, "--file", file)
		got := sent.snapshot()
		if err != nil || stdout != "" || stderr != "note "+id+" set\n" {
			t.Fatalf("\nwanted:\nsuccess on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		if got.Method != http.MethodPut || got.Path != "/notes/"+id || got.Body != `{"note":"needs review"}` || got.ContentType != "application/json" {
			t.Fatalf("\nwanted:\nPUT body from file\ngot:\n%s %s body %q type %q", got.Method, got.Path, got.Body, got.ContentType)
		}
	})

	t.Run("should set a note from stdin", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "stdin", http.StatusOK, setBody)
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "stdin", "notes", "set", id)
		command.Stdin = bytes.NewReader([]byte("needs review"))
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		got := sent.snapshot()
		if err != nil || stdout.String() != "" || stderr.String() != "note "+id+" set\n" {
			t.Fatalf("\nwanted:\nsuccess on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout.String(), stderr.String(), err)
		}
		if got.Method != http.MethodPut || got.Path != "/notes/"+id || got.Body != `{"note":"needs review"}` || got.ContentType != "application/json" {
			t.Fatalf("\nwanted:\nPUT body from stdin\ngot:\n%s %s body %q type %q", got.Method, got.Path, got.Body, got.ContentType)
		}
	})

	t.Run("should clear a note", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "clear", http.StatusOK, clearBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "clear", "notes", "clear", id)
		got := sent.snapshot()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.Method != http.MethodDelete || got.Path != "/notes/"+id || got.Body != "" || got.ContentType != "" {
			t.Fatalf("\nwanted:\nDELETE /notes/%s with no body\ngot:\n%s %s body %q type %q", id, got.Method, got.Path, got.Body, got.ContentType)
		}
		if stdout != "" || stderr != "note "+id+" cleared\n" {
			t.Fatalf("\nwanted:\nempty stdout, stderr %q\ngot:\nstdout %q, stderr %q", "note "+id+" cleared\n", stdout, stderr)
		}
	})
}

func TestNotesCommandContract(t *testing.T) {
	binary := buildMarasi(t)
	id := "0193802f-f0e7-73d9-a764-06d21e367809"

	t.Run("should pass successful responses through byte for byte in JSON mode", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "note.txt")
		if err := os.WriteFile(file, []byte("needs review"), 0o600); err != nil {
			t.Fatalf("writing note file: %v", err)
		}
		for index, args := range [][]string{
			{"--json", "notes", "set", id, "needs review"},
			{"notes", "set", id, "needs review", "--json"},
			{"--json", "notes", "set", id, "--file", file},
			{"notes", "clear", id, "--json"},
		} {
			configDir := serviceConfigDir(t)
			instanceName := "json-" + string(rune('a'+index))
			body := " {\n  \"unexpected\": true\n} "
			startCannedControlAPI(t, configDir, instanceName, http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", instanceName}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\n%v wanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", args, body, stdout, stderr, err)
			}
		}
	})

	t.Run("should normalize unavailable instances and API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "notes", "set", id, "needs review", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "notes", "clear", id, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

		startCannedControlAPI(t, configDir, "api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "api", "notes", "set", id, "needs review", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "setting note: not_found")

		startCannedControlAPI(t, configDir, "clear-api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "clear-api", "notes", "clear", id, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "clearing note: not_found")
	})

	t.Run("should reject mixed empty and tty sources before dialing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		file := filepath.Join(t.TempDir(), "note.txt")
		if err := os.WriteFile(file, []byte("needs review"), 0o600); err != nil {
			t.Fatalf("writing note file: %v", err)
		}
		emptyFile := filepath.Join(t.TempDir(), "empty.txt")
		if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
			t.Fatalf("writing empty file: %v", err)
		}
		sent := startCannedControlAPI(t, configDir, "missing", http.StatusOK, `{"id":"`+id+`"}`+"\n")

		for _, args := range [][]string{
			{"notes", "set", id, "needs review", "--file", file},
			{"notes", "set", id},
			{"notes", "set", id, ""},
			{"notes", "set", id, "--file", emptyFile},
			{"notes", "clear"},
			{"notes", "clear", id, "extra"},
			{"notes", "set"},
			{"notes", "set", id, "one", "two"},
		} {
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "missing"}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
			got := sent.snapshot()
			if got.Method != "" || got.Path != "" {
				t.Fatalf("\nwanted:\nno dial for %v\ngot:\n%s %s", args, got.Method, got.Path)
			}
		}

		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "missing", "notes", "set", id, "needs review")
		command.Stdin = bytes.NewReader([]byte("piped"))
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err == nil {
			t.Fatalf("\nwanted:\nmixed positional and stdin rejected\ngot:\naccepted")
		}
		got := sent.snapshot()
		if got.Method != "" || got.Path != "" {
			t.Fatalf("\nwanted:\nno dial for positional and stdin\ngot:\n%s %s", got.Method, got.Path)
		}

		command = exec.Command(binary, "--config-dir", configDir, "--instance", "missing", "notes", "set", id, "--file", file)
		command.Stdin = bytes.NewReader([]byte("piped"))
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err == nil {
			t.Fatalf("\nwanted:\nmixed --file and stdin rejected\ngot:\naccepted")
		}
		got = sent.snapshot()
		if got.Method != "" || got.Path != "" {
			t.Fatalf("\nwanted:\nno dial for --file and stdin\ngot:\n%s %s", got.Method, got.Path)
		}

		command = exec.Command(binary, "--config-dir", configDir, "--instance", "missing", "notes", "set", id)
		command.Stdin = bytes.NewReader(nil)
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err == nil {
			t.Fatalf("\nwanted:\nempty stdin rejected\ngot:\naccepted")
		}
		got = sent.snapshot()
		if got.Method != "" || got.Path != "" {
			t.Fatalf("\nwanted:\nno dial for empty stdin\ngot:\n%s %s", got.Method, got.Path)
		}
	})

	t.Run("should print help for notes without a subcommand", func(t *testing.T) {
		stdout, stderr, err := runMarasi(binary, "notes")
		if err != nil || stderr != "" || stdout == "" {
			t.Fatalf("\nwanted:\nhelp on stdout\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})
}

func TestNotesListCommand(t *testing.T) {
	binary := buildMarasi(t)

	t.Run("should print the newest page in JSON item order", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","scheme":"https","method":"POST","host":"example.com","path":"/login","status":"401 Unauthorized","status_code":401,"content_type":"text/plain","length":"45","metadata":{},"requested_at":"2026-01-02T03:04:04Z","responded_at":"2026-01-02T03:04:05Z","note":"newer note"},{"id":"0193802f-f0e7-73d9-a764-06d21e367809","scheme":"https","method":"GET","host":"example.com","path":"/a","status":"200 OK","status_code":200,"content_type":"application/json","length":"12","metadata":{"foo":"bar"},"requested_at":"2026-01-02T03:04:05Z","responded_at":"2026-01-02T03:04:06Z","note":"older note"}],"next_cursor":null}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/notes" || got.RawQuery != "limit=200" {
			t.Fatalf("\nwanted:\nGET /notes?limit=200\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
		want := "01938032-1b17-7243-b035-e6a9f4645904  POST  example.com  /login  401  45  newer note\n0193802f-f0e7-73d9-a764-06d21e367809  GET   example.com  /a      200  12  older note\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should truncate long paths and notes before aligning the row", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"0193802f-f0e7-73d9-a764-06d21e367809","method":"GET","host":"example.com","path":"/12345678901234567890123456789012345678901234567890","status_code":200,"length":"12","note":"01234567890123456789012345678901234567890123456789"},{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"POST","host":"example.com","path":"/short","status_code":404,"length":"45","note":"short"}],"next_cursor":null}`)

		stdout, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "0193802f-f0e7-73d9-a764-06d21e367809  GET   example.com  /123456789012345678901234567890123456...  200  12  0123456789012345678901234567890123456...\n01938032-1b17-7243-b035-e6a9f4645904  POST  example.com  /short                                    404  45  short\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should truncate long notes without splitting utf-8 characters", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"0193802f-f0e7-73d9-a764-06d21e367809","method":"GET","host":"example.com","path":"/a","status_code":200,"length":"12","note":"012345678901234567890123456789012345😀XYZQ"}],"next_cursor":null}`)

		stdout, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "0193802f-f0e7-73d9-a764-06d21e367809  GET  example.com  /a  200  12  012345678901234567890123456789012345😀...\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if !utf8.ValidString(stdout) {
			t.Fatalf("\nwanted:\nvalid utf-8\ngot:\n%s", stdout)
		}
	})

	t.Run("should write next_cursor to stderr when another page exists", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"GET","host":"example.com","path":"/a","status_code":200,"length":"12","note":"keep"}],"next_cursor":"0193802f-f0e7-73d9-a764-06d21e367809"}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "01938032-1b17-7243-b035-e6a9f4645904  GET  example.com  /a  200  12  keep\n"
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
		if stderr != "next_cursor=0193802f-f0e7-73d9-a764-06d21e367809\n" {
			t.Fatalf("\nwanted:\nnext_cursor=0193802f-f0e7-73d9-a764-06d21e367809\ngot:\n%s", stderr)
		}
	})

	t.Run("should print the control API list body with --json", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		body := `{"items":[{"id":"01938032-1b17-7243-b035-e6a9f4645904","method":"GET","host":"example.com","path":"/a","status_code":200,"length":"12","note":"keep"}],"next_cursor":"0193802f-f0e7-73d9-a764-06d21e367809"}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		for _, args := range [][]string{
			{"--json", "notes", "list"},
			{"notes", "list", "--json"},
		} {
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "work"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\n%v wanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", args, body, stdout, stderr, err)
			}
		}
	})

	t.Run("should print no rows for an empty page", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno rows\ngot:\n%s", stdout)
		}
		if stderr != "" {
			t.Fatalf("\nwanted:\nempty stderr\ngot:\n%s", stderr)
		}
	})

	t.Run("should send --limit and --cursor as query parameters", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[],"next_cursor":null}`)

		_, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list", "--limit", "10", "--cursor", "0193802f-f0e7-73d9-a764-06d21e367809")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/notes" || got.RawQuery != "cursor=0193802f-f0e7-73d9-a764-06d21e367809&limit=10" {
			t.Fatalf("\nwanted:\nGET /notes?cursor=...&limit=10\ngot:\n%s %s?%s", got.Method, got.Path, got.RawQuery)
		}
	})

	t.Run("should fail and name the instance when the control listener is missing", func(t *testing.T) {
		configDir := serviceConfigDir(t)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if stdout != "" {
			t.Fatalf("\nwanted:\nno stdout\ngot:\n%s", stdout)
		}
		if !strings.Contains(stderr, "instance work is not running") {
			t.Fatalf("\nwanted:\nstderr naming work\ngot:\n%s", stderr)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance work is not running")
	})

	t.Run("should normalize a JSON control API error", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "work", http.StatusBadRequest, `{"error":"bad_request"}`)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "notes", "list", "--json", "--limit", "0")
		got := sent.snapshot()
		if got.RawQuery != "limit=0" {
			t.Fatalf("\nwanted:\nlimit=0\ngot:\n%s", got.RawQuery)
		}
		assertJSONCommandError(t, stdout, stderr, err, "listing notes: bad_request")
	})
}
