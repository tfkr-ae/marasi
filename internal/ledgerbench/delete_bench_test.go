//go:build bench1m

package ledgerbench_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/ledgerbench"
	"github.com/tfkr-ae/marasi/service"
)

// evenlySpread returns count pair ids spread evenly over the project at
// path, oldest first.
func evenlySpread(b *testing.B, path string, count int) []uuid.UUID {
	b.Helper()
	conn, err := db.New(path, quiet())
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	var ids []uuid.UUID
	if err := conn.Select(&ids, `SELECT id FROM request ORDER BY id`); err != nil {
		b.Fatal(err)
	}
	step := max(1, len(ids)/count)
	out := make([]uuid.UUID, 0, count)
	for i := 0; i < len(ids) && len(out) < count; i += step {
		out = append(out, ids[i])
	}
	return out
}

// BenchmarkDelete is case 25: deleting 200 and 10k pairs spread over the
// project, through the Repository and POST /traffic/bulk/delete. The
// fixture's child tables (notes, launchpad, finding, test case and Armory
// links, WebSocket connections and messages, proxy logs, highlights,
// prettified bodies), both search indexes and the sitemap are populated.
// Every run deletes from a fresh copy of the fixture, cloned and opened
// untimed.
func BenchmarkDelete(b *testing.B) {
	p := benchProject(b)
	cases := []struct {
		name   string
		ids    []uuid.UUID
		target ledgerbench.Target
	}{
		{"25-ids-200", evenlySpread(b, p.path, 200), ledgerbench.Target{Max: ms(1000)}},
		{"25-ids-10k", evenlySpread(b, p.path, min(10_000, p.manifest.Pairs/2)), ledgerbench.Target{Max: time.Second}},
	}
	for _, c := range cases {
		sel := domain.Selection{IDs: c.ids}
		body, err := json.Marshal(map[string]any{"selection": map[string]any{"ids": c.ids}})
		if err != nil {
			b.Fatal(err)
		}
		run := map[string]func(repo *db.Repository, server http.Handler) error{
			"repo": func(repo *db.Repository, _ http.Handler) error {
				count, err := repo.DeleteTraffic(sel)
				if err == nil && count != len(c.ids) {
					err = fmt.Errorf("deleted %d pairs, want %d", count, len(c.ids))
				}
				return err
			},
			"service": func(_ *db.Repository, server http.Handler) error {
				response := httptest.NewRecorder()
				server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/traffic/bulk/delete", bytes.NewReader(body)))
				if response.Code != http.StatusOK {
					return fmt.Errorf("POST /traffic/bulk/delete: %d %s", response.Code, response.Body)
				}
				return nil
			},
		}
		for _, via := range []string{"repo", "service"} {
			b.Run(c.name+"/"+via, func(b *testing.B) {
				dir := b.TempDir()
				var repo *db.Repository
				var server http.Handler
				var clone string
				prepare := func() error {
					var err error
					if clone, err = ledgerbench.CloneProject(p.path, dir); err != nil {
						return err
					}
					conn, err := db.New(clone, quiet())
					if err != nil {
						return err
					}
					repo = db.NewProxyRepo(conn)
					server = service.NewServer(&marasi.Proxy{TrafficRepo: repo}, nil, nil, func() {}, "bench", "bench", clone)
					return nil
				}
				ledgerbench.MeasurePrepared(b, c.target, prepare, func() (func(), error) {
					err := run[via](repo, server)
					return func() {
						repo.Close()
						for _, suffix := range []string{"", "-wal", "-shm"} {
							os.Remove(clone + suffix)
						}
					}, err
				})
			})
		}
	}
}
