package service

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/report"
)

func TestReportTemplateListControlAPI(t *testing.T) {
	configDir := t.TempDir()
	generator, err := report.NewGenerator(nil, report.WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "templates")
	for name, content := range map[string]string{
		"a.md": "alpha", "default_template.md": "default", "z.md": "", ".hidden.md": "hidden",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "a.md"), filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(&marasi.Proxy{ReportGenerator: generator}, func() {})
	subscriber := server.events.subscribe()
	defer server.events.unsubscribe(subscriber)

	assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/report/template", ""), http.StatusOK,
		"{\"items\":[{\"name\":\"a.md\",\"size\":5},{\"name\":\"default_template.md\",\"size\":7},{\"name\":\"z.md\",\"size\":0}]}\n")
	select {
	case event := <-subscriber.events:
		t.Fatalf("list emitted %s", event.name)
	default:
	}
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/report/template?extra=true", ""), http.StatusBadRequest, "{\"error\":\"invalid_report_template_request\"}\n")
	assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodGet, "/report/template", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")

	for _, name := range []string{"a.md", "default_template.md", "z.md"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/report/template", ""), http.StatusOK, "{\"items\":[]}\n")
}

func TestReportTemplateAddControlAPI(t *testing.T) {
	configDir := t.TempDir()
	generator, err := report.NewGenerator(nil, report.WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "templates")
	if err := os.Remove(filepath.Join(dir, "default_template.md")); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(&marasi.Proxy{ReportGenerator: generator}, func() {})
	subscriber := server.events.subscribe()
	defer server.events.unsubscribe(subscriber)
	source := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(source, []byte("{{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"path": source})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"items\":[{\"name\":\"template.md\",\"size\":8}]}"
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report/template", string(body)), http.StatusOK, want+"\n")
	if _, err := os.Lstat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "template.md")); err != nil || string(data) != "{{broken" {
		t.Fatalf("template not moved intact: %q, %v", data, err)
	}
	if event := <-subscriber.events; event.name != "report.template.added" || string(event.data) != want {
		t.Fatalf("wrong event: %s %s", event.name, event.data)
	}
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/report/template", ""), http.StatusOK, want+"\n")
	select {
	case event := <-subscriber.events:
		t.Fatalf("list emitted %s", event.name)
	default:
	}

	other := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(other, []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	conflict, _ := json.Marshal(map[string]string{"path": other})
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report/template", string(conflict)), http.StatusConflict, "{\"error\":\"report_template_already_exists\"}\n")
	if data, err := os.ReadFile(other); err != nil || string(data) != "different" {
		t.Fatalf("conflicting source changed: %q, %v", data, err)
	}
	select {
	case event := <-subscriber.events:
		t.Fatalf("conflict emitted %s", event.name)
	default:
	}
}

func TestReportTemplateAddRejectsInvalidRequests(t *testing.T) {
	configDir := t.TempDir()
	generator, err := report.NewGenerator(nil, report.WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(&marasi.Proxy{ReportGenerator: generator}, func() {})
	for _, input := range []struct{ path, body string }{
		{"/report/template?extra=true", `{"path":"/tmp/a.md"}`},
		{"/report/template?bad=%zz", `{"path":"/tmp/a.md"}`},
		{"/report/template", ""},
		{"/report/template", "null"},
		{"/report/template", `{}`},
		{"/report/template", `{"path":"relative.md"}`},
		{"/report/template", `{"path":"/tmp/a.md","extra":true}`},
		{"/report/template", `{"path":"/tmp/a.md"} {}`},
	} {
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, input.path, input.body), http.StatusBadRequest, "{\"error\":\"invalid_report_template_request\"}\n")
	}
	missing, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "missing.md")})
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report/template", string(missing)), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodPost, "/report/template", string(missing)), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
}

func TestReportTemplateRemoveControlAPI(t *testing.T) {
	configDir := t.TempDir()
	generator, err := report.NewGenerator(nil, report.WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "templates")
	for name, content := range map[string]string{
		"default_template.md": "default",
		"dropped.md":          "dropped",
		"z.md":                "zz",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(&marasi.Proxy{ReportGenerator: generator}, func() {})
	subscriber := server.events.subscribe()
	defer server.events.unsubscribe(subscriber)

	want := "{\"items\":[{\"name\":\"default_template.md\",\"size\":7},{\"name\":\"z.md\",\"size\":2}]}"
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/report/template/dropped.md", ""), http.StatusOK, want+"\n")
	if _, err := os.Lstat(filepath.Join(dir, "dropped.md")); !os.IsNotExist(err) {
		t.Fatalf("hand-dropped template still exists: %v", err)
	}
	if event := <-subscriber.events; event.name != "report.template.removed" || string(event.data) != want {
		t.Fatalf("wrong event: %s %s", event.name, event.data)
	}

	want = "{\"items\":[{\"name\":\"z.md\",\"size\":2}]}"
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/report/template/default_template.md", ""), http.StatusOK, want+"\n")
	if _, err := os.Lstat(filepath.Join(dir, "default_template.md")); !os.IsNotExist(err) {
		t.Fatalf("default template still exists: %v", err)
	}
	if event := <-subscriber.events; event.name != "report.template.removed" || string(event.data) != want {
		t.Fatalf("wrong event: %s %s", event.name, event.data)
	}

	assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/report/template/link.md", ""), http.StatusOK, want+"\n")
	if _, err := os.Lstat(filepath.Join(dir, "link.md")); !os.IsNotExist(err) {
		t.Fatalf("symlink still exists: %v", err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "target" {
		t.Fatalf("symlink target changed: %q, %v", content, err)
	}
	if event := <-subscriber.events; event.name != "report.template.removed" || string(event.data) != want {
		t.Fatalf("wrong event: %s %s", event.name, event.data)
	}

	assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, "/report/template", ""), http.StatusOK, want+"\n")
	select {
	case event := <-subscriber.events:
		t.Fatalf("list emitted %s", event.name)
	default:
	}

	assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/report/template/missing.md", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	for _, request := range []struct{ path, body string }{
		{"/report/template/..", ""},
		{"/report/template/foo/../bar", ""},
		{"/report/template/%2E%2E", ""},
		{"/report/template/.", ""},
		{"/report/template/z.md?extra=true", ""},
		{"/report/template/z.md?bad=%zz", ""},
		{"/report/template/z.md", "not empty"},
	} {
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, request.path, request.body), http.StatusBadRequest, "{\"error\":\"invalid_report_template_request\"}\n")
	}
	if content, err := os.ReadFile(filepath.Join(dir, "z.md")); err != nil || string(content) != "zz" {
		t.Fatalf("rejected remove changed a template: %q, %v", content, err)
	}
	select {
	case event := <-subscriber.events:
		t.Fatalf("rejected remove emitted %s", event.name)
	default:
	}

	want = "{\"items\":[]}"
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodDelete, "/report/template/z.md", ""), http.StatusOK, want+"\n")
	if event := <-subscriber.events; event.name != "report.template.removed" || string(event.data) != want {
		t.Fatalf("wrong event: %s %s", event.name, event.data)
	}
	assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodDelete, "/report/template/z.md", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
}
