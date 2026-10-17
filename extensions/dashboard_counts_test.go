package extensions

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// TestRepoSetMetadataKeepsInterceptedCount checks that marasi.repo's
// set_metadata keeps the dashboard's maintained intercepted count exact on a
// real project (ticket 23).
func TestRepoSetMetadataKeepsInterceptedCount(t *testing.T) {
	t.Run("should count and uncount a pair as set_metadata sets intercepted", func(t *testing.T) {
		conn, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("opening the project: %v", err)
		}
		repo := db.NewProxyRepo(conn)
		t.Cleanup(func() { repo.Close() })
		id := uuid.Must(uuid.NewV7())
		err = repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "GET", Host: "example.com", Path: "/",
			Raw: []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("InsertRequest() error = %v", err)
		}

		extension, mockProxy := setupTestExtension(t, "")
		mockProxy.GetTrafficRepoFunc = func() (domain.TrafficRepository, error) { return repo, nil }
		for _, step := range []struct {
			lua  string
			want int
		}{
			{`marasi.repo:set_metadata("` + id.String() + `", {intercepted = true})`, 1},
			{`marasi.repo:set_metadata("` + id.String() + `", {intercepted = true, note = "again"})`, 1},
			{`marasi.repo:set_metadata("` + id.String() + `", {intercepted = false})`, 0},
		} {
			if err := extension.ExecuteLua(step.lua); err != nil {
				t.Fatalf("executing %s: %v", step.lua, err)
			}
			counts, err := repo.DashboardCounts()
			if err != nil {
				t.Fatalf("DashboardCounts() error = %v", err)
			}
			if counts.Intercepted != step.want || !counts.Complete[domain.JobInterceptedCount] {
				t.Fatalf("after %s: DashboardCounts() = %d intercepted, complete %v; want %d, true",
					step.lua, counts.Intercepted, counts.Complete[domain.JobInterceptedCount], step.want)
			}
		}
	})
}
