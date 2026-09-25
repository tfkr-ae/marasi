package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/compass"
	"github.com/tfkr-ae/marasi/domain"
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

func TestReportTemplateRestoreControlAPI(t *testing.T) {
	configDir := t.TempDir()
	generator, err := report.NewGenerator(nil, report.WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "templates")
	embedded, err := os.ReadFile(filepath.Join(dir, "default_template.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "z.md"), []byte("zz"), 0600); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(&marasi.Proxy{ReportGenerator: generator}, func() {})
	subscriber := server.events.subscribe()
	defer server.events.unsubscribe(subscriber)

	want := fmt.Sprintf("{\"items\":[{\"name\":\"default_template.md\",\"size\":%d},{\"name\":\"z.md\",\"size\":2}]}", len(embedded))
	assertRestored := func() {
		t.Helper()
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report/template/restore", ""), http.StatusOK, want+"\n")
		got, err := os.ReadFile(filepath.Join(dir, "default_template.md"))
		if err != nil || string(got) != string(embedded) {
			t.Fatalf("default not restored: %q, %v", got, err)
		}
		if content, err := os.ReadFile(filepath.Join(dir, "z.md")); err != nil || string(content) != "zz" {
			t.Fatalf("other template changed: %q, %v", content, err)
		}
		if event := <-subscriber.events; event.name != "report.template.restored" || string(event.data) != want {
			t.Fatalf("wrong event: %s %s", event.name, event.data)
		}
		select {
		case event := <-subscriber.events:
			t.Fatalf("restore also emitted %s", event.name)
		default:
		}
	}

	assertRestored()

	if err := os.WriteFile(filepath.Join(dir, "default_template.md"), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	assertRestored()

	if err := os.Remove(filepath.Join(dir, "default_template.md")); err != nil {
		t.Fatal(err)
	}
	assertRestored()

	for _, request := range []struct{ path, body string }{
		{"/report/template/restore?extra=true", ""},
		{"/report/template/restore?bad=%zz", ""},
		{"/report/template/restore", "not empty"},
		{"/report/template/restore", "{}"},
	} {
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, request.path, request.body), http.StatusBadRequest, "{\"error\":\"invalid_report_template_request\"}\n")
	}
	got, err := os.ReadFile(filepath.Join(dir, "default_template.md"))
	if err != nil || string(got) != string(embedded) {
		t.Fatalf("rejected restore changed the default: %q, %v", got, err)
	}
	if content, err := os.ReadFile(filepath.Join(dir, "z.md")); err != nil || string(content) != "zz" {
		t.Fatalf("rejected restore changed another template: %q, %v", content, err)
	}
	select {
	case event := <-subscriber.events:
		t.Fatalf("rejected restore emitted %s", event.name)
	default:
	}

	assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodPost, "/report/template/restore", ""), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
}

const reportExportFixture = `title={{.Metadata.Title}}
client={{.Metadata.Client}}
type={{.Metadata.Type}}
draft={{.Metadata.IsDraft}}
scope={{.Metadata.Scope}}
assessor={{.Metadata.Assessor}}
start={{.Metadata.Start.Format "2006-01-02T15:04:05Z07:00"}}
end={{.Metadata.End.Format "2006-01-02T15:04:05Z07:00"}}
created={{.Metadata.CreatedAt.Format "2006-01-02T15:04:05Z07:00"}}
truncate={{.Metadata.TruncateLength}}
custom={{toJSON .Metadata.CustomProperties}}
findings={{range .Findings}}{{.Title}}:{{if .TestCaseID}}linked{{else}}unassigned{{end}};{{end}}
casetitles={{range .TestCases}}{{.Title}};{{end}}
cases={{printf "%#v" .TestCases}}
`

type stubExportReporting struct {
	domain.ReportingRepository
	findings     []*domain.Finding
	testCases    []*domain.TestCase
	findingsErr  error
	testCasesErr error
}

func (repo *stubExportReporting) ListFindings() ([]*domain.Finding, error) {
	if repo.findingsErr != nil {
		return nil, repo.findingsErr
	}
	return repo.findings, nil
}

func (repo *stubExportReporting) ListTestCases() ([]*domain.TestCase, error) {
	if repo.testCasesErr != nil {
		return nil, repo.testCasesErr
	}
	return repo.testCases, nil
}

func reportExportField(t *testing.T, rendered, name string) string {
	t.Helper()
	prefix := name + "="
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("rendered report missing %s:\n%s", name, rendered)
	return ""
}

func TestReportExportRendersOpenProject(t *testing.T) {
	configDir := t.TempDir()
	generator, err := report.NewGenerator(nil, report.WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "templates", "fixture.md"), []byte(reportExportFixture), 0600); err != nil {
		t.Fatal(err)
	}
	caseID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
	repo := &stubExportReporting{
		findings: []*domain.Finding{
			{Title: "XSS"},
			{Title: "SQLi", TestCaseID: &caseID},
		},
		testCases: []*domain.TestCase{{Title: "Auth"}},
	}
	server := newTestServer(&marasi.Proxy{
		ReportGenerator: generator,
		ReportingRepo:   repo,
		Scope:           compass.NewScope(true),
	}, func() {})
	subscriber := server.events.subscribe()
	defer server.events.unsubscribe(subscriber)

	before := time.Now().UTC().Add(-time.Second)
	response := requestControlAPI(server, http.MethodPost, "/report", `{
		"template":"fixture.md",
		"title":"Quarterly",
		"client":"Acme",
		"type":"Web",
		"scope":"api.acme.test",
		"assessor":"Ada",
		"start":"2026-01-02",
		"end":"2026-01-16",
		"is_draft":false,
		"truncate_length":50,
		"include_test_cases":true
	}`)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/octet-stream" || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("wanted 200 application/octet-stream and no content-disposition, got %d %s %q", response.Code, response.Header().Get("Content-Type"), response.Header().Get("Content-Disposition"))
	}
	got := response.Body.String()
	createdLine := reportExportField(t, got, "created")
	stamped, err := time.Parse("2006-01-02T15:04:05Z07:00", createdLine)
	if err != nil || stamped.Location() != time.UTC || stamped.Before(before) || stamped.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("created_at %q is not an export-time UTC stamp: %v", createdLine, err)
	}
	want := "title=Quarterly\nclient=Acme\ntype=Web\ndraft=false\nscope=api.acme.test\nassessor=Ada\nstart=2026-01-02T00:00:00Z\nend=2026-01-16T00:00:00Z\ncreated=" + createdLine + "\ntruncate=50\ncustom={}\nfindings=XSS:unassigned;SQLi:linked;\ncasetitles=Auth;\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("\nwanted prefix:\n%s\ngot:\n%s", want, got)
	}
	select {
	case event := <-subscriber.events:
		t.Fatalf("export emitted %s", event.name)
	default:
	}
}

func TestReportExportPayloadDefaultsAndEmptyLogbook(t *testing.T) {
	server, _, _ := newReportExportServer(t, &stubExportReporting{
		findings:  []*domain.Finding{{Title: "XSS"}},
		testCases: []*domain.TestCase{{Title: "Auth"}},
	})

	omitted := requestControlAPI(server, http.MethodPost, "/report", `{"template":"fixture.md","start":"2026-03-01","end":"2026-03-02"}`)
	if omitted.Code != http.StatusOK {
		t.Fatalf("omitted fields: %d %s", omitted.Code, omitted.Body.String())
	}
	got := omitted.Body.String()
	for _, field := range []string{"title=", "client=", "type=", "draft=true", "scope=", "assessor=", "truncate=0", "start=2026-03-01T00:00:00Z", "end=2026-03-02T00:00:00Z", "custom={}", "findings=XSS:unassigned;", "casetitles=Auth;"} {
		if !strings.Contains(got, field) {
			t.Fatalf("omitted export missing %q:\n%s", field, got)
		}
	}

	withoutCases := requestControlAPI(server, http.MethodPost, "/report", `{"template":"fixture.md","start":"2026-03-01","end":"2026-03-02","include_test_cases":false}`)
	if withoutCases.Code != http.StatusOK {
		t.Fatalf("include_test_cases false: %d %s", withoutCases.Code, withoutCases.Body.String())
	}
	body := withoutCases.Body.String()
	if !strings.Contains(body, "findings=XSS:unassigned;") || !strings.Contains(body, "casetitles=\n") || !strings.Contains(body, "cases=[]*domain.TestCase{}") {
		t.Fatalf("false include_test_cases did not keep findings and an empty test-case slice:\n%s", body)
	}

	short := requestControlAPI(server, http.MethodPost, "/report", `{"template":"fixture.md","start":"2026-03-01","end":"2026-03-02","truncate_length":1}`)
	if short.Code != http.StatusOK || !strings.Contains(short.Body.String(), "truncate=1") {
		t.Fatalf("truncate length 1 was rejected:\n%d %s", short.Code, short.Body.String())
	}

	emptyServer, _, _ := newReportExportServer(t, &stubExportReporting{
		findings:  []*domain.Finding{},
		testCases: []*domain.TestCase{},
	})
	empty := requestControlAPI(emptyServer, http.MethodPost, "/report", `{"template":"fixture.md","start":"2026-03-01","end":"2026-03-02"}`)
	if empty.Code != http.StatusOK || empty.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(empty.Body.String(), "findings=\n") || !strings.Contains(empty.Body.String(), "casetitles=\n") {
		t.Fatalf("empty logbook did not export: %d %s %s", empty.Code, empty.Header().Get("Content-Type"), empty.Body.String())
	}
}

func TestReportExportRejectsInvalidRequests(t *testing.T) {
	server, _, _ := newReportExportServer(t, &stubExportReporting{})
	for _, body := range []string{
		"",
		"{}",
		`{"start":"2026-01-02","end":"2026-01-16"}`,
		`{"template":"fixture.md","end":"2026-01-16"}`,
		`{"template":"fixture.md","start":"2026-01-16"}`,
		`{"template":"fixture.md","start":"2026-01-02T00:00:00Z","end":"2026-01-16"}`,
		`{"template":"fixture.md","start":"2026-02-31","end":"2026-01-16"}`,
		`{"template":"fixture.md","start":"01/02/2026","end":"2026-01-16"}`,
		`{"template":"","start":"2026-01-02","end":"2026-01-16"}`,
		`{"template":"../secret.md","start":"2026-01-02","end":"2026-01-16"}`,
		`{"template":"nested/a.md","start":"2026-01-02","end":"2026-01-16"}`,
		`{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16","truncate_length":-1}`,
		`{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16","created_at":"2026-01-02T00:00:00Z"}`,
		`{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16","custom_properties":{}}`,
		`{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16","test_case_id":"01938032-1b17-7243-b035-e6a9f4645904"}`,
		`{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16"}{}`,
		`{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16","is_draft":"yes"}`,
	} {
		assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report", body), http.StatusBadRequest, "{\"error\":\"invalid_report_request\"}\n")
	}
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report?is_draft=false", `{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16"}`), http.StatusBadRequest, "{\"error\":\"invalid_report_request\"}\n")
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report?bad=%zz", `{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16"}`), http.StatusBadRequest, "{\"error\":\"invalid_report_request\"}\n")
}

func TestReportExportErrors(t *testing.T) {
	server, templates, generator := newReportExportServer(t, &stubExportReporting{})
	subscriber := server.events.subscribe()
	defer server.events.unsubscribe(subscriber)
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report", `{"template":"missing.md","start":"2026-01-02","end":"2026-01-16"}`), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodPost, "/report", `{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16"}`), http.StatusNotFound, "{\"error\":\"not_found\"}\n")
	assertControlAPIResponse(t, requestControlAPI(newTestServer(&marasi.Proxy{ReportGenerator: generator}, func() {}), http.MethodPost, "/report", `{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16"}`), http.StatusNotFound, "{\"error\":\"not_found\"}\n")

	failServer, _, _ := newReportExportServer(t, &stubExportReporting{findingsErr: errors.New("db down")})
	assertControlAPIResponse(t, requestControlAPI(failServer, http.MethodPost, "/report", `{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16"}`), http.StatusInternalServerError, "{\"error\":\"internal_server_error\"}\n")
	caseFail, _, _ := newReportExportServer(t, &stubExportReporting{testCasesErr: errors.New("db down")})
	assertControlAPIResponse(t, requestControlAPI(caseFail, http.MethodPost, "/report", `{"template":"fixture.md","start":"2026-01-02","end":"2026-01-16"}`), http.StatusInternalServerError, "{\"error\":\"internal_server_error\"}\n")

	_, templateErr := generator.Execute([]byte("{{.Missing}}"), &domain.ReportPayload{})
	if templateErr == nil {
		t.Fatal("fixture template was expected to fail")
	}
	if err := os.WriteFile(filepath.Join(templates, "broken.md"), []byte("{{.Missing}}"), 0600); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}{Error: "invalid_report_request", Message: templateErr.Error()})
	if err != nil {
		t.Fatal(err)
	}
	assertControlAPIResponse(t, requestControlAPI(server, http.MethodPost, "/report", `{"template":"broken.md","start":"2026-01-02","end":"2026-01-16"}`), http.StatusBadRequest, string(want)+"\n")
	select {
	case event := <-subscriber.events:
		t.Fatalf("export error emitted %s", event.name)
	default:
	}
}

func newReportExportServer(t *testing.T, repo *stubExportReporting) (*Server, string, *report.Generator) {
	t.Helper()
	configDir := t.TempDir()
	generator, err := report.NewGenerator(nil, report.WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	templates := filepath.Join(configDir, "templates")
	if err := os.WriteFile(filepath.Join(templates, "fixture.md"), []byte(reportExportFixture), 0600); err != nil {
		t.Fatal(err)
	}
	return newTestServer(&marasi.Proxy{ReportGenerator: generator, ReportingRepo: repo, Scope: compass.NewScope(true)}, func() {}), templates, generator
}
