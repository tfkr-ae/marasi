package main

import (
	"net/http"
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
