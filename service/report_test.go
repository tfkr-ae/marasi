package service

import (
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
