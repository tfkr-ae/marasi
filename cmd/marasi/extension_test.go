package main

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

func TestExtensionCommands(t *testing.T) {
	binary := buildMarasi(t)
	compassID := "01937d13-9632-72aa-83b9-c10ea1abbdd6"
	checkpointID := "01937d13-9632-75b1-9e73-c5129b06fa8c"
	workshopID := "01937d13-9632-7f84-add5-14ec2c2c7f43"

	t.Run("should list extensions as id name enabled lines", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		response := `{"items":[{"id":"` + compassID + `","name":"compass","enabled":true},{"id":"` + checkpointID + `","name":"checkpoint","enabled":false}]}` + "\n"
		sent := startCannedControlAPI(t, configDir, "list", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "list", "extension", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/extension" || got.Body != "" {
			t.Fatalf("\nwanted:\nGET /extension empty body\ngot:\n%s %s body %q", got.Method, got.Path, got.Body)
		}
		wantStdout := compassID + " compass enabled\n" + checkpointID + " checkpoint disabled\n"
		if stdout != wantStdout || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", wantStdout, stdout, stderr)
		}
	})

	t.Run("should leave an empty list silent", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "empty", http.StatusOK, `{"items":[]}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "empty", "extension", "list")
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nempty streams, nil error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("should write lua content only for human get", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		lua := "print(\"workshop\")\n"
		response := `{"id":"` + workshopID + `","name":"workshop","enabled":true,"lua_content":"print(\"workshop\")\n","settings":{}}` + "\n"
		sent := startCannedControlAPI(t, configDir, "get", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "get", "extension", "get", workshopID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got := sent.snapshot()
		if got.Method != http.MethodGet || got.Path != "/extension/"+workshopID || got.Body != "" {
			t.Fatalf("\nwanted:\nGET /extension/%s empty body\ngot:\n%s %s body %q", workshopID, got.Method, got.Path, got.Body)
		}
		if stdout != lua || stderr != "" {
			t.Fatalf("\nwanted:\nstdout %q, empty stderr\ngot:\nstdout %q, stderr %q", lua, stdout, stderr)
		}
	})

	t.Run("should pass successful responses through byte for byte in JSON mode", func(t *testing.T) {
		for _, args := range [][]string{
			{"--json", "extension", "list"},
			{"extension", "list", "--json"},
			{"--json", "extension", "get", workshopID},
			{"extension", "get", workshopID, "--json"},
		} {
			configDir := serviceConfigDir(t)
			body := " {\n  \"unexpected\": true\n} "
			startCannedControlAPI(t, configDir, "json", http.StatusOK, body)
			commandArgs := append([]string{"--config-dir", configDir, "--instance", "json"}, args...)
			stdout, stderr, err := runMarasi(binary, commandArgs...)
			if err != nil || stdout != body || stderr != "" {
				t.Fatalf("\n%v wanted:\nstdout %q, empty stderr, nil error\ngot:\nstdout %q, stderr %q, error %v", args, body, stdout, stderr, err)
			}
		}
	})

	t.Run("should enforce arguments", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for _, args := range [][]string{
			{"extension", "list", "extra"},
			{"extension", "get"},
			{"extension", "get", workshopID, "extra"},
		} {
			commandArgs := append([]string{"--config-dir", configDir}, args...)
			if _, _, err := runMarasi(binary, commandArgs...); err == nil {
				t.Fatalf("\nwanted:\ninvalid invocation\ngot:\naccepted %v", args)
			}
		}
	})

	t.Run("should report missing instances and json API errors", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "extension", "list")
		if err == nil || stdout != "" || !strings.Contains(stderr, "instance missing is not running") {
			t.Fatalf("\nwanted:\nmissing instance on stderr\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}

		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "extension", "list", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

		startCannedControlAPI(t, configDir, "api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "api", "extension", "get", workshopID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "getting extension: not_found")
	})
}

func TestExtensionCommandLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("service process fixture uses Unix control sockets")
	}
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	t.Cleanup(func() { runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop") })

	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "start", "--project-name", "extension-cli", "--port", "0"); err != nil {
		t.Fatalf("starting service: %v", err)
	}

	compassID := "01937d13-9632-72aa-83b9-c10ea1abbdd6"
	checkpointID := "01937d13-9632-75b1-9e73-c5129b06fa8c"
	workshopID := "01937d13-9632-7f84-add5-14ec2c2c7f43"

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "list")
	wantList := compassID + " compass enabled\n" + checkpointID + " checkpoint enabled\n" + workshopID + " workshop enabled\n"
	if err != nil || stdout != wantList || stderr != "" {
		t.Fatalf("listing extensions: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "list", "--json")
	if err != nil || stderr != "" {
		t.Fatalf("listing extensions as JSON: stderr %q, error %v", stderr, err)
	}
	var listed struct {
		Items []struct {
			ID          string          `json:"id"`
			Name        string          `json:"name"`
			LuaContent  json.RawMessage `json:"lua_content"`
			Settings    json.RawMessage `json:"settings"`
			Logs        json.RawMessage `json:"logs"`
			Core        json.RawMessage `json:"core"`
			Enabled     *bool           `json:"enabled"`
			Author      string          `json:"author"`
			Description string          `json:"description"`
			SourceURL   string          `json:"source_url"`
			UpdatedAt   string          `json:"updated_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatalf("decoding extension list: %v", err)
	}
	if len(listed.Items) != 3 ||
		listed.Items[0].ID != compassID || listed.Items[0].Name != "compass" ||
		listed.Items[1].ID != checkpointID || listed.Items[1].Name != "checkpoint" ||
		listed.Items[2].ID != workshopID || listed.Items[2].Name != "workshop" {
		t.Fatalf("wanted seeded compass, checkpoint, workshop in id order, got %#v", listed.Items)
	}
	for _, item := range listed.Items {
		if item.LuaContent != nil || item.Settings != nil || item.Logs != nil || item.Core != nil {
			t.Fatalf("list item included omitted fields: %#v", item)
		}
		if item.Enabled == nil || !*item.Enabled || item.Author == "" || item.Description == "" || item.SourceURL == "" || item.UpdatedAt == "" {
			t.Fatalf("list item missing metadata: %#v", item)
		}
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "get", workshopID)
	if err != nil || stderr != "" || !strings.Contains(stdout, "Welcome to the Marasi Workshop") {
		t.Fatalf("getting workshop lua: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "get", workshopID, "--json")
	if err != nil || stderr != "" {
		t.Fatalf("getting workshop as JSON: stderr %q, error %v", stderr, err)
	}
	var detail struct {
		ID         string          `json:"id"`
		Name       string          `json:"name"`
		LuaContent string          `json:"lua_content"`
		Settings   map[string]any  `json:"settings"`
		Logs       json.RawMessage `json:"logs"`
		Core       json.RawMessage `json:"core"`
	}
	if err := json.Unmarshal([]byte(stdout), &detail); err != nil {
		t.Fatalf("decoding extension get: %v", err)
	}
	if detail.ID != workshopID || detail.Name != "workshop" || !strings.Contains(detail.LuaContent, "Welcome to the Marasi Workshop") || detail.Settings == nil || detail.Logs != nil || detail.Core != nil {
		t.Fatalf("wanted workshop detail with lua and settings, got %#v", detail)
	}
}
