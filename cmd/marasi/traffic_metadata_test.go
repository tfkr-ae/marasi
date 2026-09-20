package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTrafficMetadataCommands(t *testing.T) {
	binary := buildMarasi(t)
	id := "0193802f-f0e7-73d9-a764-06d21e367809"
	getBody := `{"highlight":"red"}` + "\n"
	updateBody := `{"highlight":"red"}` + "\n"

	t.Run("should print metadata on stdout", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "get", http.StatusOK, getBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "get", "traffic", "metadata", "get", id)
		got := sent.snapshot()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.Method != http.MethodGet || got.Path != "/traffic/"+id+"/metadata" || got.Body != "" {
			t.Fatalf("\nwanted:\nGET /traffic/%s/metadata\ngot:\n%s %s body %q", id, got.Method, got.Path, got.Body)
		}
		if stdout != getBody || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", getBody, stdout, stderr)
		}
	})

	t.Run("should update metadata from --file", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		file := filepath.Join(t.TempDir(), "metadata.json")
		if err := os.WriteFile(file, []byte(`{"highlight":"red"}`), 0o600); err != nil {
			t.Fatalf("writing metadata file: %v", err)
		}
		sent := startCannedControlAPI(t, configDir, "file", http.StatusOK, updateBody)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "file", "traffic", "metadata", "update", id, "--file", file)
		got := sent.snapshot()
		if err != nil || stdout != "" || stderr != "metadata "+id+" updated\n" {
			t.Fatalf("\nwanted:\nsuccess on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		if got.Method != http.MethodPut || got.Path != "/traffic/"+id+"/metadata" || got.Body != `{"highlight":"red"}` || got.ContentType != "application/json" {
			t.Fatalf("\nwanted:\nPUT body from file\ngot:\n%s %s body %q type %q", got.Method, got.Path, got.Body, got.ContentType)
		}
	})

	t.Run("should update metadata from stdin", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		sent := startCannedControlAPI(t, configDir, "stdin", http.StatusOK, updateBody)
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "stdin", "traffic", "metadata", "update", id)
		command.Stdin = bytes.NewReader([]byte(`{"highlight":"red"}`))
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		got := sent.snapshot()
		if err != nil || stdout.String() != "" || stderr.String() != "metadata "+id+" updated\n" {
			t.Fatalf("\nwanted:\nsuccess on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout.String(), stderr.String(), err)
		}
		if got.Method != http.MethodPut || got.Path != "/traffic/"+id+"/metadata" || got.Body != `{"highlight":"red"}` || got.ContentType != "application/json" {
			t.Fatalf("\nwanted:\nPUT body from stdin\ngot:\n%s %s body %q type %q", got.Method, got.Path, got.Body, got.ContentType)
		}
	})
}

func TestTrafficMetadataCommandContract(t *testing.T) {
	binary := buildMarasi(t)
	id := "0193802f-f0e7-73d9-a764-06d21e367809"

	t.Run("should pass successful responses through byte for byte in JSON mode", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "metadata.json")
		if err := os.WriteFile(file, []byte(`{"highlight":"red"}`), 0o600); err != nil {
			t.Fatalf("writing metadata file: %v", err)
		}
		for index, args := range [][]string{
			{"--json", "traffic", "metadata", "get", id},
			{"traffic", "metadata", "get", id, "--json"},
			{"--json", "traffic", "metadata", "update", id, "--file", file},
			{"traffic", "metadata", "update", id, "--file", file, "--json"},
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
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "traffic", "metadata", "get", id, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

		missingFile := filepath.Join(t.TempDir(), "metadata.json")
		if writeErr := os.WriteFile(missingFile, []byte(`{"highlight":"red"}`), 0o600); writeErr != nil {
			t.Fatalf("writing metadata file: %v", writeErr)
		}
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "traffic", "metadata", "update", id, "--file", missingFile, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

		startCannedControlAPI(t, configDir, "api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "api", "traffic", "metadata", "get", id, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "getting metadata: not_found")

		file := filepath.Join(t.TempDir(), "metadata.json")
		if err := os.WriteFile(file, []byte(`{"highlight":"red"}`), 0o600); err != nil {
			t.Fatalf("writing metadata file: %v", err)
		}
		startCannedControlAPI(t, configDir, "update-api", http.StatusBadRequest, `{"error":"invalid_metadata_request"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "update-api", "traffic", "metadata", "update", id, "--file", file, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "updating metadata: invalid_metadata_request")
	})

	t.Run("should reject mixed empty and tty sources before dialing", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		file := filepath.Join(t.TempDir(), "metadata.json")
		if err := os.WriteFile(file, []byte(`{"highlight":"red"}`), 0o600); err != nil {
			t.Fatalf("writing metadata file: %v", err)
		}
		emptyFile := filepath.Join(t.TempDir(), "empty.json")
		if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
			t.Fatalf("writing empty file: %v", err)
		}
		sent := startCannedControlAPI(t, configDir, "missing", http.StatusOK, "{}\n")

		for _, args := range [][]string{
			{"traffic", "metadata", "update", id},
			{"traffic", "metadata", "update"},
			{"traffic", "metadata", "update", id, "extra"},
			{"traffic", "metadata", "update", id, "--file", emptyFile},
			{"traffic", "metadata", "get"},
			{"traffic", "metadata", "get", id, "extra"},
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
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "missing", "traffic", "metadata", "update", id, "--file", file)
		command.Stdin = bytes.NewReader([]byte(`{"highlight":"blue"}`))
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err == nil {
			t.Fatalf("\nwanted:\nmixed --file and stdin rejected\ngot:\naccepted")
		}
		got := sent.snapshot()
		if got.Method != "" || got.Path != "" {
			t.Fatalf("\nwanted:\nno dial for --file and stdin\ngot:\n%s %s", got.Method, got.Path)
		}

		command = exec.Command(binary, "--config-dir", configDir, "--instance", "missing", "traffic", "metadata", "update", id)
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

	t.Run("should print help for traffic metadata without a subcommand", func(t *testing.T) {
		stdout, stderr, err := runMarasi(binary, "traffic", "metadata")
		if err != nil || stderr != "" || stdout == "" {
			t.Fatalf("\nwanted:\nhelp on stdout\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})
}
