package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// stubStats returns fixed dashboard counts, so a test can report an
// intercepted_count job that is not complete.
type stubStats struct {
	domain.StatsRepository
	counts domain.DashboardCounts
}

func (s stubStats) DashboardCounts() (domain.DashboardCounts, error) { return s.counts, nil }

func TestDashboard(t *testing.T) {
	t.Run("should return the stored counts with the intercepted_count job's completeness", func(t *testing.T) {
		p := setupBulkProject(t)
		a := p.insertPair(t, "GET")
		p.insertPair(t, "GET")
		if err := p.repo.UpdateMetadata(map[string]any{"intercepted": true}, a); err != nil {
			t.Fatal(err)
		}
		if err := p.repo.UpdateNote(a, "a note"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.repo.CreateLaunchpad("dashboard", ""); err != nil {
			t.Fatal(err)
		}
		server := newTestServer(&marasi.Proxy{StatsRepo: p.repo}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/dashboard", "")
		want := `{"pairs":2,"notes":1,"launchpads":1,"intercepted":1,"intercepted_count":{"complete":true},"project_bytes":null}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})

	t.Run("should report an intercepted_count job that is not complete", func(t *testing.T) {
		stats := stubStats{counts: domain.DashboardCounts{
			Pairs: 9, Intercepted: 4,
			Complete: map[domain.JobName]bool{domain.JobInterceptedCount: false},
		}}
		server := newTestServer(&marasi.Proxy{StatsRepo: stats}, func() {})

		response := requestControlAPI(server, http.MethodGet, "/dashboard", "")
		want := `{"pairs":9,"notes":0,"launchpads":0,"intercepted":4,"intercepted_count":{"complete":false},"project_bytes":null}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})

	t.Run("should fail with not_found when no project is open", func(t *testing.T) {
		response := requestControlAPI(newTestServer(&marasi.Proxy{}, func() {}), http.MethodGet, "/dashboard", "")
		want := `{"error":"not_found"}` + "\n"
		if response.Code != http.StatusNotFound || response.Body.String() != want {
			t.Fatalf("\nwanted:\n404 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})
}

func TestDashboardProjectBytes(t *testing.T) {
	stats := stubStats{counts: domain.DashboardCounts{
		Pairs:    3,
		Complete: map[domain.JobName]bool{domain.JobInterceptedCount: true},
	}}
	read := func(t *testing.T, path string) string {
		t.Helper()
		mux := http.NewServeMux()
		addDashboardRoutes(mux, &marasi.Proxy{StatsRepo: stats}, func() string { return path })
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("wanted 200, got %d %s", response.Code, response.Body)
		}
		return response.Body.String()
	}
	write := func(t *testing.T, path string, size int) {
		t.Helper()
		if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want := func(size string) string {
		return fmt.Sprintf(`{"pairs":3,"notes":0,"launchpads":0,"intercepted":0,"intercepted_count":{"complete":true},"project_bytes":%s}`+"\n", size)
	}

	t.Run("should count the project file and its -wal and -shm files", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "p.marasi")
		write(t, path, 4096)
		write(t, path+"-wal", 1000)
		write(t, path+"-shm", 32768)
		if got := read(t, path); got != want("37864") {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want("37864"), got)
		}
	})

	t.Run("should count a missing -wal or -shm file as 0", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "p.marasi")
		write(t, path, 4096)
		if got := read(t, path); got != want("4096") {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want("4096"), got)
		}
		write(t, path+"-wal", 512)
		if got := read(t, path); got != want("4608") {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want("4608"), got)
		}
	})

	t.Run("should keep the counts and send null when the project file cannot be read", func(t *testing.T) {
		for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.marasi")} {
			if got := read(t, path); got != want("null") {
				t.Fatalf("path %q:\nwanted:\n%s\ngot:\n%s", path, want("null"), got)
			}
		}
	})
}
