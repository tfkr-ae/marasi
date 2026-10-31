package service

import (
	"net/http"
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
		want := `{"pairs":2,"notes":1,"launchpads":1,"intercepted":1,"intercepted_count":{"complete":true}}` + "\n"
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
		want := `{"pairs":9,"notes":0,"launchpads":0,"intercepted":4,"intercepted_count":{"complete":false}}` + "\n"
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
