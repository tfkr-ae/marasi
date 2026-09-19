package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

	t.Run("should update lua from a file and write success on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		lua := "print(1)\n"
		luaFile := filepath.Join(t.TempDir(), "workshop.lua")
		if err := os.WriteFile(luaFile, []byte(lua), 0o600); err != nil {
			t.Fatalf("writing lua file: %v", err)
		}
		response := `{"id":"` + workshopID + `","name":"workshop","lua_content":"print(1)\n"}` + "\n"
		sent := startCannedControlAPI(t, configDir, "update", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "update", "extension", "update", workshopID, "--file", luaFile)
		got := sent.snapshot()
		wantBody := `{"lua_content":"print(1)\n"}`
		if err != nil || stdout != "" || stderr != "extension "+workshopID+" updated\n" || got.Method != http.MethodPost || got.Path != "/extension/"+workshopID || got.Body != wantBody {
			t.Fatalf("\nwanted:\nPOST %s body %q, empty stdout, success on stderr\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", "/extension/"+workshopID, wantBody, got.Method, got.Path, got.Body, stdout, stderr, err)
		}
	})

	t.Run("should update lua from piped stdin", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		lua := []byte("print(2)")
		body := `{"id":"` + workshopID + `","lua_content":"print(2)"}` + "\n"
		sent := startCannedControlAPI(t, configDir, "stdin", http.StatusOK, body)
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "stdin", "extension", "update", workshopID)
		command.Stdin = bytes.NewReader(lua)
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		got := sent.snapshot()
		wantBody := `{"lua_content":"print(2)"}`
		if err != nil || stdout.String() != "" || stderr.String() != "extension "+workshopID+" updated\n" || got.Body != wantBody {
			t.Fatalf("\nwanted:\nbody %q and success on stderr\ngot:\nbody %q, stdout %q, stderr %q, error %v", wantBody, got.Body, stdout.String(), stderr.String(), err)
		}
	})

	t.Run("should print logs as RFC3339 text lines", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		response := `{"items":[{"time":"2026-09-19T10:00:00Z","text":"first"},{"time":"2026-09-19T10:00:01Z","text":"second"}]}` + "\n"
		sent := startCannedControlAPI(t, configDir, "logs", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "logs", "extension", "logs", workshopID)
		got := sent.snapshot()
		wantStdout := "2026-09-19T10:00:00Z first\n2026-09-19T10:00:01Z second\n"
		if err != nil || stdout != wantStdout || stderr != "" || got.Method != http.MethodGet || got.Path != "/extension/"+workshopID+"/logs" || got.Body != "" {
			t.Fatalf("\nwanted:\nGET logs, stdout %q\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", wantStdout, got.Method, got.Path, got.Body, stdout, stderr, err)
		}
	})

	t.Run("should print settings get as the API body", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		response := `{"settings":{"theme":"dark"}}` + "\n"
		sent := startCannedControlAPI(t, configDir, "settings-get", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "settings-get", "extension", "settings", "get", workshopID)
		got := sent.snapshot()
		if err != nil || stdout != response || stderr != "" || got.Method != http.MethodGet || got.Path != "/extension/"+workshopID+"/settings" || got.Body != "" {
			t.Fatalf("\nwanted:\nGET settings body on stdout\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", got.Method, got.Path, got.Body, stdout, stderr, err)
		}
	})

	t.Run("should set settings from a file and write success on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		settingsFile := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(settingsFile, []byte(`{"theme":"dark"}`), 0o600); err != nil {
			t.Fatalf("writing settings file: %v", err)
		}
		response := `{"settings":{"theme":"dark"}}` + "\n"
		sent := startCannedControlAPI(t, configDir, "settings-set", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "settings-set", "extension", "settings", "set", workshopID, "--file", settingsFile)
		got := sent.snapshot()
		wantBody := `{"settings":{"theme":"dark"}}`
		if err != nil || stdout != "" || stderr != "extension "+workshopID+" settings updated\n" || got.Method != http.MethodPost || got.Path != "/extension/"+workshopID+"/settings" || got.Body != wantBody {
			t.Fatalf("\nwanted:\nPOST %s body %q, empty stdout, success on stderr\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", "/extension/"+workshopID+"/settings", wantBody, got.Method, got.Path, got.Body, stdout, stderr, err)
		}
	})

	t.Run("should set settings from piped stdin", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		settings := []byte(`{"theme":"light"}`)
		body := `{"settings":{"theme":"light"}}` + "\n"
		sent := startCannedControlAPI(t, configDir, "settings-stdin", http.StatusOK, body)
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, "--config-dir", configDir, "--instance", "settings-stdin", "extension", "settings", "set", workshopID)
		command.Stdin = bytes.NewReader(settings)
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		got := sent.snapshot()
		wantBody := `{"settings":{"theme":"light"}}`
		if err != nil || stdout.String() != "" || stderr.String() != "extension "+workshopID+" settings updated\n" || got.Body != wantBody {
			t.Fatalf("\nwanted:\nbody %q and success on stderr\ngot:\nbody %q, stdout %q, stderr %q, error %v", wantBody, got.Body, stdout.String(), stderr.String(), err)
		}
	})

	t.Run("should call a function and write success on stderr", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		response := `{"status":"called"}` + "\n"
		sent := startCannedControlAPI(t, configDir, "call", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "call", "extension", "call", workshopID, "poke")
		got := sent.snapshot()
		wantBody := `{"function":"poke"}`
		if err != nil || stdout != "" || stderr != "extension "+workshopID+" called poke\n" || got.Method != http.MethodPost || got.Path != "/extension/"+workshopID+"/call" || got.Body != wantBody {
			t.Fatalf("\nwanted:\nPOST %s body %q, empty stdout, success on stderr\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", "/extension/"+workshopID+"/call", wantBody, got.Method, got.Path, got.Body, stdout, stderr, err)
		}
	})

	t.Run("should send --args as a JSON array", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		response := `{"status":"called"}` + "\n"
		sent := startCannedControlAPI(t, configDir, "call-args", http.StatusOK, response)

		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "call-args", "extension", "call", workshopID, "poke", "--args", `["hello",1,{"key":"val"}]`)
		got := sent.snapshot()
		wantBody := `{"function":"poke","args":["hello",1,{"key":"val"}]}`
		if err != nil || stdout != "" || stderr != "extension "+workshopID+" called poke\n" || got.Method != http.MethodPost || got.Path != "/extension/"+workshopID+"/call" || got.Body != wantBody {
			t.Fatalf("\nwanted:\nPOST %s body %q, empty stdout, success on stderr\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", "/extension/"+workshopID+"/call", wantBody, got.Method, got.Path, got.Body, stdout, stderr, err)
		}
	})

	t.Run("should reject invalid --args before the request", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		for i, raw := range []string{"{", `{"key":"val"}`, `"hello"`, "1", "null"} {
			name := "call-bad-args-" + string(rune('a'+i))
			sent := startCannedControlAPI(t, configDir, name, http.StatusOK, `{"status":"called"}`+"\n")
			stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", name, "extension", "call", workshopID, "poke", "--args", raw)
			got := sent.snapshot()
			if err == nil || stdout != "" || stderr == "" || got.Method != "" || got.Path != "" || got.Body != "" {
				t.Fatalf("\nwanted:\nCLI error before request for --args %q\ngot:\n%s %s body %q, stdout %q, stderr %q, error %v", raw, got.Method, got.Path, got.Body, stdout, stderr, err)
			}
		}
	})

	t.Run("should leave empty logs silent", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "empty-logs", http.StatusOK, `{"items":[]}`+"\n")
		stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "empty-logs", "extension", "logs", workshopID)
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("\nwanted:\nempty streams, nil error\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("should pass successful responses through byte for byte in JSON mode", func(t *testing.T) {
		luaFile := filepath.Join(t.TempDir(), "workshop.lua")
		if err := os.WriteFile(luaFile, []byte("print(1)"), 0o600); err != nil {
			t.Fatalf("writing lua file: %v", err)
		}
		settingsFile := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(settingsFile, []byte(`{"theme":"dark"}`), 0o600); err != nil {
			t.Fatalf("writing settings file: %v", err)
		}
		for _, args := range [][]string{
			{"--json", "extension", "list"},
			{"extension", "list", "--json"},
			{"--json", "extension", "get", workshopID},
			{"extension", "get", workshopID, "--json"},
			{"--json", "extension", "update", workshopID, "--file", luaFile},
			{"extension", "update", workshopID, "--file", luaFile, "--json"},
			{"--json", "extension", "logs", workshopID},
			{"extension", "logs", workshopID, "--json"},
			{"--json", "extension", "settings", "get", workshopID},
			{"extension", "settings", "get", workshopID, "--json"},
			{"--json", "extension", "settings", "set", workshopID, "--file", settingsFile},
			{"extension", "settings", "set", workshopID, "--file", settingsFile, "--json"},
			{"--json", "extension", "call", workshopID, "poke"},
			{"extension", "call", workshopID, "poke", "--json"},
			{"--json", "extension", "call", workshopID, "poke", "--args", `["hello"]`},
			{"extension", "call", workshopID, "poke", "--args", `["hello"]`, "--json"},
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
			{"extension", "update"},
			{"extension", "update", workshopID},
			{"extension", "update", workshopID, "extra"},
			{"extension", "logs"},
			{"extension", "logs", workshopID, "extra"},
			{"extension", "settings", "get"},
			{"extension", "settings", "get", workshopID, "extra"},
			{"extension", "settings", "set"},
			{"extension", "settings", "set", workshopID},
			{"extension", "settings", "set", workshopID, "extra"},
			{"extension", "call"},
			{"extension", "call", workshopID},
			{"extension", "call", workshopID, "poke", "extra"},
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

		startCannedControlAPI(t, configDir, "update-api", http.StatusNotFound, `{"error":"not_found"}`)
		luaFile := filepath.Join(t.TempDir(), "workshop.lua")
		if err := os.WriteFile(luaFile, []byte("print(1)"), 0o600); err != nil {
			t.Fatalf("writing lua file: %v", err)
		}
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "update-api", "extension", "update", workshopID, "--file", luaFile, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "updating extension: not_found")

		startCannedControlAPI(t, configDir, "logs-api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "logs-api", "extension", "logs", workshopID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "listing extension logs: not_found")

		startCannedControlAPI(t, configDir, "settings-get-api", http.StatusNotFound, `{"error":"not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "settings-get-api", "extension", "settings", "get", workshopID, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "getting extension settings: not_found")

		startCannedControlAPI(t, configDir, "settings-set-api", http.StatusNotFound, `{"error":"not_found"}`)
		settingsFile := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(settingsFile, []byte(`{"theme":"dark"}`), 0o600); err != nil {
			t.Fatalf("writing settings file: %v", err)
		}
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "settings-set-api", "extension", "settings", "set", workshopID, "--file", settingsFile, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "setting extension settings: not_found")

		startCannedControlAPI(t, configDir, "call-api", http.StatusNotFound, `{"error":"function_not_found"}`)
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "call-api", "extension", "call", workshopID, "poke", "--json")
		assertJSONCommandError(t, stdout, stderr, err, "calling extension: function_not_found")
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
	originalLua := stdout

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

	luaFile := filepath.Join(t.TempDir(), "workshop.lua")
	if err := os.WriteFile(luaFile, []byte(originalLua), 0o600); err != nil {
		t.Fatalf("writing workshop lua: %v", err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "update", workshopID, "--file", luaFile)
	if err != nil || stdout != "" || stderr != "extension "+workshopID+" updated\n" {
		t.Fatalf("round-tripping workshop lua: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "get", workshopID)
	if err != nil || stderr != "" || stdout != originalLua {
		t.Fatalf("wanted get after update to match original lua, got stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	updatedLua := `print("lifecycle")`
	if err := os.WriteFile(luaFile, []byte(updatedLua), 0o600); err != nil {
		t.Fatalf("writing updated lua: %v", err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "update", workshopID, "--file", luaFile, "--json")
	if err != nil || stderr != "" {
		t.Fatalf("updating workshop as JSON: stderr %q, error %v", stderr, err)
	}
	if err := json.Unmarshal([]byte(stdout), &detail); err != nil {
		t.Fatalf("decoding extension update: %v", err)
	}
	if detail.LuaContent != updatedLua {
		t.Fatalf("wanted updated lua %q, got %q", updatedLua, detail.LuaContent)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "logs", workshopID)
	if err != nil || stderr != "" || !strings.Contains(stdout, "lifecycle") {
		t.Fatalf("listing workshop logs: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "settings", "get", workshopID)
	if err != nil || stderr != "" || stdout != `{"settings":{}}`+"\n" {
		t.Fatalf("getting empty workshop settings: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	settingsFile := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settingsFile, []byte(`{"theme":"dark","count":1}`), 0o600); err != nil {
		t.Fatalf("writing settings file: %v", err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "settings", "set", workshopID, "--file", settingsFile)
	if err != nil || stdout != "" || stderr != "extension "+workshopID+" settings updated\n" {
		t.Fatalf("setting workshop settings: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "settings", "get", workshopID)
	if err != nil || stderr != "" || stdout != `{"settings":{"count":1,"theme":"dark"}}`+"\n" {
		t.Fatalf("getting workshop settings: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	if err := os.WriteFile(settingsFile, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("writing empty settings: %v", err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "settings", "set", workshopID, "--file", settingsFile, "--json")
	if err != nil || stderr != "" || stdout != `{"settings":{}}`+"\n" {
		t.Fatalf("clearing workshop settings as JSON: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	callLua := `function poke(msg)
  print(msg)
end`
	if err := os.WriteFile(luaFile, []byte(callLua), 0o600); err != nil {
		t.Fatalf("writing call lua: %v", err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "update", workshopID, "--file", luaFile)
	if err != nil || stdout != "" || stderr != "extension "+workshopID+" updated\n" {
		t.Fatalf("updating workshop for call: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "call", workshopID, "poke", "--args", `["from-cli"]`)
	if err != nil || stdout != "" || stderr != "extension "+workshopID+" called poke\n" {
		t.Fatalf("calling workshop poke: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "logs", workshopID)
	if err != nil || stderr != "" || !strings.Contains(stdout, "from-cli") {
		t.Fatalf("listing workshop logs after call: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "call", workshopID, "poke", "--json")
	if err != nil || stderr != "" || stdout != `{"status":"called"}`+"\n" {
		t.Fatalf("calling workshop as JSON: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "extension", "call", workshopID, "missing", "--json")
	assertJSONCommandError(t, stdout, stderr, err, "calling extension: function_not_found")
}
