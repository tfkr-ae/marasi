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

func TestReportTemplateRemoveCommand(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	body := "{\"items\":[{\"name\":\"z.md\",\"size\":2}]}\n"
	sent := startCannedControlAPI(t, configDir, "report-remove", http.StatusOK, body)

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "report-remove", "report", "template", "remove", "dropped.md")
	if err != nil || stdout != "" || stderr != "report template dropped.md removed\n" {
		t.Fatalf("unexpected human result: %q, %q, %v", stdout, stderr, err)
	}
	request := sent.snapshot()
	if request.Method != http.MethodDelete || request.Path != "/report/template/dropped.md" || request.Body != "" {
		t.Fatalf("wanted DELETE /report/template/dropped.md with empty body, got %s %s with %q", request.Method, request.Path, request.Body)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "report-remove", "--json", "report", "template", "remove", "dropped.md")
	if err != nil || stdout != body || stderr != "" {
		t.Fatalf("unexpected JSON result: %q, %q, %v", stdout, stderr, err)
	}
}

func TestReportTemplateRemoveCommandFailure(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	startCannedControlAPI(t, configDir, "report-remove-error", http.StatusNotFound, `{"error":"not_found"}`)
	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "report-remove-error", "--json", "report", "template", "remove", "missing.md")
	assertJSONCommandError(t, stdout, stderr, err, "removing report template: not_found")
}

func TestReportTemplateRestoreCommand(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	body := "{\"items\":[{\"name\":\"default_template.md\",\"size\":8433}]}\n"
	sent := startCannedControlAPI(t, configDir, "report-restore", http.StatusOK, body)

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "report-restore", "report", "template", "restore")
	if err != nil || stdout != "" || stderr != "report template default_template.md restored\n" {
		t.Fatalf("unexpected human result: %q, %q, %v", stdout, stderr, err)
	}
	request := sent.snapshot()
	if request.Method != http.MethodPost || request.Path != "/report/template/restore" || request.Body != "" {
		t.Fatalf("wanted POST /report/template/restore with empty body, got %s %s with %q", request.Method, request.Path, request.Body)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "report-restore", "--json", "report", "template", "restore")
	if err != nil || stdout != body || stderr != "" {
		t.Fatalf("unexpected JSON result: %q, %q, %v", stdout, stderr, err)
	}
}

func TestReportTemplateRestoreCommandFailure(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	startCannedControlAPI(t, configDir, "report-restore-error", http.StatusNotFound, `{"error":"not_found"}`)
	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "--instance", "report-restore-error", "--json", "report", "template", "restore")
	assertJSONCommandError(t, stdout, stderr, err, "restoring report template: not_found")
}

func TestReportExportCommand(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	dir := t.TempDir()
	sent := startCannedControlAPI(t, configDir, "report-export", http.StatusOK, "rendered report")

	stdout, stderr, err := runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export", "report", "export", "custom.md", "--title", "Assessment", "--client", "Acme", "--type", "Web", "--scope", "api.example", "--assessor", "Ada", "--start", "2026-01-02", "--end", "2026-01-16", "--draft=false", "--truncate", "40", "--include-test-cases=false", "--output", "out.md")
	if err != nil {
		t.Fatal(err)
	}
	request := sent.snapshot()
	wantBody := `{"template":"custom.md","title":"Assessment","client":"Acme","type":"Web","scope":"api.example","assessor":"Ada","start":"2026-01-02","end":"2026-01-16","is_draft":false,"truncate_length":40,"include_test_cases":false}`
	abs, err := filepath.Abs(filepath.Join(dir, "out.md"))
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.Path != "/report" || request.Body != wantBody {
		t.Fatalf("wanted POST /report %s, got %s %s %s", wantBody, request.Method, request.Path, request.Body)
	}
	if stdout != abs+"\n" || stderr != "" {
		t.Fatalf("unexpected human output: stdout %q stderr %q", stdout, stderr)
	}
	written, err := os.ReadFile(abs)
	if err != nil || string(written) != "rendered report" {
		t.Fatalf("output file: %q %v", written, err)
	}

	sent.reset()
	stdout, stderr, err = runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export", "--json", "report", "export", "custom.md", "--start", "2026-01-02", "--end", "2026-01-16", "--output", "out.md")
	if err != nil || stdout != "{\"path\":\""+abs+"\"}\n" || stderr != "" {
		t.Fatalf("unexpected JSON result: stdout %q stderr %q err %v", stdout, stderr, err)
	}
	request = sent.snapshot()
	if request.Body != `{"template":"custom.md","title":"","client":"","type":"","scope":"","assessor":"","start":"2026-01-02","end":"2026-01-16","is_draft":true,"truncate_length":0,"include_test_cases":true}` {
		t.Fatalf("default request body: %s", request.Body)
	}
}

func TestReportExportCommandNamesAndOverwrite(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	dir := t.TempDir()
	sent := startCannedControlAPI(t, configDir, "report-export-name", http.StatusOK, "new bytes")

	stdout, stderr, err := runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export-name", "report", "export", "custom.md", "--title", "Assessment", "--start", "2026-01-02", "--end", "2026-01-16")
	abs, absErr := filepath.Abs(filepath.Join(dir, "Assessment.md"))
	if err != nil || absErr != nil || stdout != abs+"\n" || stderr != "" {
		t.Fatalf("default name: stdout %q stderr %q err %v", stdout, stderr, err)
	}
	if data, readErr := os.ReadFile(abs); readErr != nil || string(data) != "new bytes" {
		t.Fatalf("default file: %q %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "Assessment.md.md")); !os.IsNotExist(statErr) {
		t.Fatal("default name doubled the extension")
	}

	sent.reset()
	stdout, stderr, err = runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export-name", "report", "export", "custom.md", "--title", "Report.md", "--start", "2026-01-02", "--end", "2026-01-16")
	doubled := filepath.Join(dir, "Report.md.md")
	kept := filepath.Join(dir, "Report.md")
	if err != nil || stderr != "" {
		t.Fatalf("extension already present: %v %q", err, stderr)
	}
	if _, statErr := os.Stat(doubled); !os.IsNotExist(statErr) {
		t.Fatal("title that already ends with the extension gained another")
	}
	if data, readErr := os.ReadFile(kept); readErr != nil || string(data) != "new bytes" || stdout != mustAbs(t, kept)+"\n" {
		t.Fatalf("kept name stdout %q data %q err %v", stdout, data, readErr)
	}

	sent.reset()
	stdout, stderr, err = runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export-name", "report", "export", "custom.md", "--start", "2026-01-02", "--end", "2026-01-16")
	base := mustAbs(t, filepath.Join(dir, "custom.md"))
	if err != nil || stdout != base+"\n" || stderr != "" {
		t.Fatalf("empty title: stdout %q stderr %q err %v", stdout, stderr, err)
	}

	sent.reset()
	stdout, stderr, err = runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export-name", "report", "export", "plain", "--title", "Notes", "--start", "2026-01-02", "--end", "2026-01-16")
	plain := mustAbs(t, filepath.Join(dir, "Notes"))
	if err != nil || stdout != plain+"\n" || stderr != "" {
		t.Fatalf("template without an extension: stdout %q stderr %q err %v", stdout, stderr, err)
	}

	if err := os.Mkdir(filepath.Join(dir, "out"), 0700); err != nil {
		t.Fatal(err)
	}
	sent.reset()
	stdout, stderr, err = runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export-name", "report", "export", "custom.md", "--start", "2026-01-02", "--end", "2026-01-16", "--output", "out/saved.txt")
	saved := mustAbs(t, filepath.Join(dir, "out", "saved.txt"))
	if err != nil || stdout != saved+"\n" || stderr != "" {
		t.Fatalf("relative --output: stdout %q stderr %q err %v", stdout, stderr, err)
	}
	if data, readErr := os.ReadFile(saved); readErr != nil || string(data) != "new bytes" {
		t.Fatalf("relative output file: %q %v", data, readErr)
	}

	existing := filepath.Join(dir, "replace.md")
	if err := os.WriteFile(existing, []byte("old bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	sent.reset()
	stdout, stderr, err = runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export-name", "report", "export", "plain", "--title", "Notes", "--start", "2026-01-02", "--end", "2026-01-16", "--output", existing)
	if err != nil || stderr != "" || stdout != mustAbs(t, existing)+"\n" {
		t.Fatalf("overwrite: stdout %q stderr %q err %v", stdout, stderr, err)
	}
	if data, readErr := os.ReadFile(existing); readErr != nil || string(data) != "new bytes" {
		t.Fatalf("overwrite left %q: %v", data, readErr)
	}
	if sent.snapshot().Body != `{"template":"plain","title":"Notes","client":"","type":"","scope":"","assessor":"","start":"2026-01-02","end":"2026-01-16","is_draft":true,"truncate_length":0,"include_test_cases":true}` {
		t.Fatalf("no-extension template body: %s", sent.snapshot().Body)
	}
}

func TestReportExportCommandFailures(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, "keep.md")
	if err := os.WriteFile(existing, []byte("old bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	sent := startCannedControlAPI(t, configDir, "report-export-fail", http.StatusBadRequest, `{"error":"invalid_report_request","message":"template: boom"}`)

	stdout, stderr, err := runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export-fail", "--json", "report", "export", "custom.md", "--start", "2026-01-02", "--end", "2026-01-16", "--output", existing)
	if stdout != "{\"error\":\"exporting report: invalid_report_request: template: boom\"}\n" || stderr != "" {
		t.Fatalf("render failure JSON: stdout %q stderr %q err %v", stdout, stderr, err)
	}
	if data, readErr := os.ReadFile(existing); readErr != nil || string(data) != "old bytes" {
		t.Fatalf("failed render changed the output file: %q %v", data, readErr)
	}

	sent.reset()
	startCannedControlAPI(t, configDir, "report-export-missing", http.StatusNotFound, `{"error":"not_found"}`)
	stdout, stderr, err = runMarasiInDir(binary, dir, "--config-dir", configDir, "--instance", "report-export-missing", "--json", "report", "export", "custom.md", "--start", "2026-01-02", "--end", "2026-01-16", "--output", existing)
	if stdout != "{\"error\":\"exporting report: not_found\"}\n" || stderr != "" {
		t.Fatalf("other export failure JSON: stdout %q stderr %q err %v", stdout, stderr, err)
	}
	if data, readErr := os.ReadFile(existing); readErr != nil || string(data) != "old bytes" {
		t.Fatalf("failed export changed the output file: %q %v", data, readErr)
	}

	for _, args := range [][]string{
		{"report", "export", "custom.md", "--end", "2026-01-16"},
		{"report", "export", "custom.md", "--start", "2026-01-02"},
		{"report", "export", "custom.md", "--start", "nope", "--end", "2026-01-16"},
		{"report", "export", "custom.md", "--start", "2026-01-02", "--end", "2026-13-01"},
		{"report", "export", "custom.md", "--start", "2026-01-02", "--end", "2026-01-16", "--truncate", "-1"},
		{"report", "export", "custom.md", "--title", "nested/name", "--start", "2026-01-02", "--end", "2026-01-16"},
	} {
		sent.reset()
		command := append([]string{"--config-dir", configDir, "--instance", "report-export-fail"}, args...)
		stdout, stderr, err = runMarasiInDir(binary, dir, command...)
		if err == nil || len(sent.requests()) != 0 {
			t.Fatalf("args %q dialed or succeeded: stdout %q stderr %q requests %d err %v", args, stdout, stderr, len(sent.requests()), err)
		}
	}
	if data, readErr := os.ReadFile(existing); readErr != nil || string(data) != "old bytes" {
		t.Fatalf("rejected export changed the output file: %q %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "nested", "name.md")); !os.IsNotExist(statErr) {
		t.Fatal("invalid default name was written")
	}

	lockedDir := t.TempDir()
	locked := filepath.Join(lockedDir, "keep.md")
	if err := os.WriteFile(locked, []byte("old bytes"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(lockedDir, 0700) })
	startCannedControlAPI(t, configDir, "report-export-write", http.StatusOK, "new bytes")
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "report-export-write", "report", "export", "custom.md", "--start", "2026-01-02", "--end", "2026-01-16", "--output", locked)
	if err == nil || stdout != "" {
		t.Fatalf("failed write printed success: stdout %q stderr %q err %v", stdout, stderr, err)
	}
	if data, readErr := os.ReadFile(locked); readErr != nil || string(data) != "old bytes" {
		t.Fatalf("failed write changed the output file: %q %v", data, readErr)
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
