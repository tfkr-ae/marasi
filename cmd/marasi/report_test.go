package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportTemplateListCommand(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	body := "{\"items\":[{\"name\":\"a.md\",\"size\":5},{\"name\":\"default_template.md\",\"size\":7}]}\n"
	sent := startCannedControlAPI(t, configDir, "report-list", http.StatusOK, body)

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "report-list", "report", "template", "list")
	if err != nil {
		t.Fatal(err)
	}
	request := sent.snapshot()
	if request.Method != http.MethodGet || request.Path != "/report/template" || request.Body != "" {
		t.Fatalf("wanted GET /report/template with no body, got %s %s with %q", request.Method, request.Path, request.Body)
	}
	if stdout != "name: a.md\nsize: 5\n\nname: default_template.md\nsize: 7\n" || stderr != "" {
		t.Fatalf("unexpected human output: stdout %q, stderr %q", stdout, stderr)
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "report-list", "report", "template", "list", "--json")
	if err != nil || stdout != body || stderr != "" {
		t.Fatalf("wanted unchanged JSON body and no stderr, got stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestReportTemplateListCommandFailure(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	startCannedControlAPI(t, configDir, "report-list-error", http.StatusBadRequest, `{"error":"invalid_report_template_request"}`)

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "report-list-error", "--json", "report", "template", "list")
	assertJSONCommandError(t, stdout, stderr, err, "listing report templates: invalid_report_template_request")
}

func TestReportTemplateAddCommand(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	source := filepath.Join(t.TempDir(), "custom.md")
	if err := os.WriteFile(source, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	body := "{\"items\":[{\"name\":\"custom.md\",\"size\":7}]}\n"
	sent := startCannedControlAPI(t, configDir, "report-add", http.StatusOK, body)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, source)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "report-add", "report", "template", "add", relative)
	if err != nil || stdout != "" || stderr != "report template custom.md added\n" {
		t.Fatalf("unexpected human result: %q, %q, %v", stdout, stderr, err)
	}
	request := sent.snapshot()
	var data struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(request.Body), &data); err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.Path != "/report/template" || data.Path != source {
		t.Fatalf("wanted POST /report/template with absolute source %q, got %s %s %s", source, request.Method, request.Path, request.Body)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "report-add", "--json", "report", "template", "add", source)
	if err != nil || stdout != body || stderr != "" {
		t.Fatalf("unexpected JSON result: %q, %q, %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "report", "template", "add", "--help")
	if err != nil || !strings.Contains(stdout, "source file is moved into the templates directory") || stderr != "" {
		t.Fatalf("help does not describe the move: %q, %q, %v", stdout, stderr, err)
	}
}

func TestReportTemplateAddCommandFailure(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	startCannedControlAPI(t, configDir, "report-add-error", http.StatusConflict, `{"error":"report_template_already_exists"}`)
	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "report-add-error", "--json", "report", "template", "add", "custom.md")
	assertJSONCommandError(t, stdout, stderr, err, "adding report template: report_template_already_exists")
}
