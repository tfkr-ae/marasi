package ledgerbench_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/ledgerbench"
	"github.com/tfkr-ae/marasi/internal/ledgerbench/fixture"
)

// testMaxBody caps the bodies of the projects these tests build, so they
// build in seconds under -race. The bench1m tag builds full-size bodies.
var testMaxBody = 1 << 10

func buildSmall(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "small.marasi")
	if _, err := fixture.Build(path, fixture.Config{Seed: 1, Pairs: 100, MaxBody: testMaxBody}); err != nil {
		t.Fatalf("building: %v", err)
	}
	return path
}

func open(t *testing.T, path string) *db.Repository {
	t.Helper()
	conn, err := db.New(path, nil)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	repo := db.NewProxyRepo(conn)
	t.Cleanup(func() { repo.Close() })
	return repo
}

func TestProbeEnvironment(t *testing.T) {
	t.Parallel()
	path := buildSmall(t)

	env, err := ledgerbench.ProbeEnvironment(path)

	t.Run("should record the toolchain and the database settings", func(t *testing.T) {
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if env.GoVersion != runtime.Version() {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", runtime.Version(), env.GoVersion)
		}
		if !strings.HasPrefix(env.ModerncSQLite, "v1.") {
			t.Fatalf("\nwanted:\nthe modernc.org/sqlite version\ngot:\n%q", env.ModerncSQLite)
		}
		if env.PageSize != 4096 || env.CacheSize == 0 {
			t.Fatalf("\nwanted:\npage_size 4096 and a cache_size\ngot:\n%+v", env)
		}
		if env.Hardware == "" || env.CPUs == 0 {
			t.Fatalf("\nwanted:\nthe hardware\ngot:\n%+v", env)
		}
	})

	t.Run("should report the state of each background job", func(t *testing.T) {
		if env.Jobs[domain.JobTrafficIndex] != "complete" {
			t.Fatalf("\nwanted:\ntraffic index complete\ngot:\n%v", env.Jobs)
		}
	})

	t.Run("should print every field", func(t *testing.T) {
		text := env.String()
		for _, want := range []string{"go:", "modernc.org/sqlite:", "page_size:", "cache_size:", "mmap_size:", "wal_bytes:", "job traffic_index: complete"} {
			if !strings.Contains(text, want) {
				t.Fatalf("\nwanted:\n%q in\n%s", want, text)
			}
		}
	})
}

func TestCloneProject(t *testing.T) {
	t.Parallel()
	t.Run("should give a copy whose writes leave the original alone", func(t *testing.T) {
		path := buildSmall(t)

		clone, err := ledgerbench.CloneProject(path, t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		cloned := open(t, clone)
		before, _ := cloned.CountRows()
		if err := cloned.InsertRequest(&domain.ProxyRequest{
			ID: uuid.Must(uuid.NewV7()), Scheme: "https", Method: "GET", Host: "clone.test", Path: "/", Raw: []byte("GET / HTTP/1.1\r\n\r\n"),
		}); err != nil {
			t.Fatalf("writing to the clone: %v", err)
		}

		original, _ := open(t, path).CountRows()
		after, _ := cloned.CountRows()
		if before != 100 || after != 101 || original != 100 {
			t.Fatalf("\nwanted:\n100 then 101 in the clone, 100 in the original\ngot:\n%d then %d, %d", before, after, original)
		}
	})
}
