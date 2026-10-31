package service

import (
	"net/http"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// dashboardCounts is the body of GET /dashboard.
type dashboardCounts struct {
	Pairs       int `json:"pairs"`
	Notes       int `json:"notes"`
	Launchpads  int `json:"launchpads"`
	Intercepted int `json:"intercepted"`
	// InterceptedCount reports whether Intercepted includes every pair
	// (docs/adr/2026-10-14-background-jobs-save-their-state.md).
	InterceptedCount jobCompleteness `json:"intercepted_count"`
}

// addDashboardRoutes registers the dashboard count route. The counts are
// kept as pairs are written, so the read does not scan traffic
// (docs/adr/2026-10-16-dashboard-counts-are-kept-as-pairs-are-written.md).
func addDashboardRoutes(mux routeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		repo, err := proxy.GetStatsRepo()
		if err != nil {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		counts, err := repo.DashboardCounts()
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		writeJSON(w, r, http.StatusOK, dashboardCounts{
			Pairs:            counts.Pairs,
			Notes:            counts.Notes,
			Launchpads:       counts.Launchpads,
			Intercepted:      counts.Intercepted,
			InterceptedCount: jobCompleteness{Complete: counts.Complete[domain.JobInterceptedCount]},
		})
	})
}
