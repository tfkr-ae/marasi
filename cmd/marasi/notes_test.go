package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
