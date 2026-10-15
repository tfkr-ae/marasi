package ledgerbench_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/ledgerbench/fixture"
)

// TestCaptureCostDoesNotGrowWithProjectSize captures into a new project the
// way the proxy does, InsertRequest then InsertResponse with 50 pairs in
// flight, and checks that the last pairs cost about what the first did.
// Case 22 cannot see this: it captures a few pairs into a project that is
// already full size. A new project that slows down as it fills ran at 80
// pairs/s at the start and 2 to 4 pairs/s by 20k pairs.
func TestCaptureCostDoesNotGrowWithProjectSize(t *testing.T) {
	if testing.Short() {
		t.Skip("captures 1500 pairs")
	}
	const (
		pairs    = 1500
		window   = 300
		inFlight = 50
	)
	conn, err := db.New(filepath.Join(t.TempDir(), "capture.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	repo := db.NewProxyRepo(conn)
	t.Cleanup(func() { repo.Close() })

	captured := fixture.NewPairs(1, pairs, fixture.End, 16<<10)
	var waiting []int
	respond := func() {
		i := waiting[0]
		waiting = waiting[1:]
		if r := captured[i].Response; r != nil {
			if err := repo.InsertResponse(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Capture leaves its pairs queued for the search index (ADR-0026). A text
	// search indexes everything queued first, so each window ends with one
	// and pays for indexing its own pairs.
	indexed := func() {
		if _, err := repo.ListTraffic(domain.TrafficQuery{Text: `"no-such-text"`, Limit: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// The first window starts after a warm-up window: indexing into an
	// empty index merges nothing, so the very first pairs cost about half
	// of what every later window does, at any project size.
	var first, last time.Duration
	var started time.Time
	for i, pair := range captured {
		if err := repo.InsertRequest(pair.Request); err != nil {
			t.Fatal(err)
		}
		waiting = append(waiting, i)
		if len(waiting) >= inFlight {
			respond()
		}
		switch i + 1 {
		case window:
			indexed()
			started = time.Now()
		case 2 * window:
			indexed()
			first = time.Since(started)
		case pairs - window:
			indexed()
			started = time.Now()
		}
	}
	indexed()
	last = time.Since(started)
	for len(waiting) > 0 {
		respond()
	}

	t.Logf("first %d pairs: %s, last %d pairs: %s", window, first, window, last)
	if last > 2*first {
		t.Errorf("the last %d pairs took %s, more than twice the first %d (%s): capture cost grows with project size", window, last, window, first)
	}
}
