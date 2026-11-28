package service

import (
	"errors"
	"net/http"
	"os"

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
	// ProjectBytes is the open project's size on disk, or null when its
	// file cannot be read.
	ProjectBytes *int64 `json:"project_bytes"`
}

// projectFileSuffixes are the files one SQLite project takes on disk: the
// project file, then its write-ahead log and shared-memory index.
var projectFileSuffixes = []string{"", "-wal", "-shm"}

// projectBytes returns the size on disk of the project at path: its file
// plus its -wal and -shm files, each counted as 0 when missing. Recent
// writes can sit in the -wal until a checkpoint, so it is counted.
func projectBytes(path string) (int64, error) {
	if path == "" {
		return 0, errors.New("no project path")
	}
	var total int64
	for _, suffix := range projectFileSuffixes {
		info, err := os.Stat(path + suffix)
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

// addDashboardRoutes registers the dashboard count route. The counts are
// kept as pairs are written, so the read does not scan traffic
// (docs/adr/2026-10-16-dashboard-counts-are-kept-as-pairs-are-written.md).
// projectPath returns the open project's path, whose size is read on each
// request.
func addDashboardRoutes(mux routeMux, proxy *marasi.Proxy, projectPath func() string) {
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
		body := dashboardCounts{
			Pairs:            counts.Pairs,
			Notes:            counts.Notes,
			Launchpads:       counts.Launchpads,
			Intercepted:      counts.Intercepted,
			InterceptedCount: jobCompleteness{Complete: counts.Complete[domain.JobInterceptedCount]},
		}
		// The size is a nicety, so a failed read leaves it null rather than
		// failing the counts.
		if size, err := projectBytes(projectPath()); err == nil {
			body.ProjectBytes = &size
		}
		writeJSON(w, r, http.StatusOK, body)
	})
}
