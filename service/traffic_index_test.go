package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/db"
)

// seedUnindexedProject creates a project file holding count pairs written the
// way an older Marasi binary writes them: without traffic index rows. Pair i
// has path /item/i and is newer than pair i-1.
func seedUnindexedProject(t *testing.T, path string, count int) {
	t.Helper()
	connection, err := db.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("creating project: %v", err)
	}
	defer connection.Close()
	tx, err := connection.Beginx()
	if err != nil {
		t.Fatalf("starting seed: %v", err)
	}
	defer tx.Rollback()
	for i := range count {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}
		_, err = tx.Exec(`INSERT INTO request
			(id, scheme, method, host, path, request_raw, response_raw, status, status_code, requested_at, responded_at)
			VALUES (?, 'https', 'GET', 'example.com', ?, ?, ?, '200 OK', 200, ?, ?)`,
			id, fmt.Sprintf("/item/%d", i),
			fmt.Sprintf("GET /item/%d HTTP/1.1\r\nHost: example.com\r\n\r\n", i),
			fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nlegacy body %d.", i),
			time.Now(), time.Now())
		if err != nil {
			t.Fatalf("seeding pair: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing seed: %v", err)
	}
}

// missingFromIndex counts the pairs in the project file without an index row.
func missingFromIndex(t *testing.T, path string) int {
	t.Helper()
	connection, err := sqlx.Connect("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("opening project: %v", err)
	}
	defer connection.Close()
	var missing int
	err = connection.Get(&missing, `SELECT count(*) FROM request r
		WHERE NOT EXISTS (SELECT 1 FROM traffic_fts_docsize d WHERE d.id = r.rowid)`)
	if err != nil {
		t.Fatalf("counting pairs missing from the index: %v", err)
	}
	return missing
}

// breakPairMetadata makes the metadata of the pair at path unreadable as JSON,
// so the pair cannot be indexed, and returns the pair's id.
func breakPairMetadata(t *testing.T, project, path string) uuid.UUID {
	t.Helper()
	connection, err := sqlx.Connect("sqlite", "file:"+project)
	if err != nil {
		t.Fatalf("opening project: %v", err)
	}
	defer connection.Close()
	var id uuid.UUID
	if err := connection.Get(&id, `UPDATE request SET metadata = 'not json' WHERE path = ? RETURNING id`, path); err != nil {
		t.Fatalf("breaking metadata: %v", err)
	}
	return id
}

// lockedBuffer collects log output written from other goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// indexPacer runs the real build batches but holds each batch of the paced
// project until the test steps it or releases the build.
type indexPacer struct {
	paced      string
	batchBytes int
	mu         sync.Mutex
	indexed    map[string]int
	calls      chan indexCall
	step       chan struct{}
	free       chan struct{}
}

type indexCall struct {
	path string
	ctx  context.Context
}

func paceIndexBuild(lifecycle *ProjectLifecycle, paced string) *indexPacer {
	pacer := &indexPacer{
		paced:      paced,
		batchBytes: indexBuildBatchBytes,
		indexed:    map[string]int{},
		calls:      make(chan indexCall, 100),
		step:       make(chan struct{}),
		free:       make(chan struct{}),
	}
	lifecycle.indexBatch = pacer.batch
	return pacer
}

func (pacer *indexPacer) batch(ctx context.Context, path string, builder trafficIndexBuilder) (int, bool, error) {
	if path == pacer.paced && !pacer.released() {
		pacer.calls <- indexCall{path: path, ctx: ctx}
		select {
		case <-pacer.step:
		case <-pacer.free:
		case <-ctx.Done():
			return 0, false, ctx.Err()
		}
	}
	indexed, remaining, err := builder.IndexMissingTraffic(pacer.batchBytes)
	pacer.mu.Lock()
	pacer.indexed[path] += indexed
	pacer.mu.Unlock()
	return indexed, remaining, err
}

// waitBatch waits for the next paced batch to be ready.
func (pacer *indexPacer) waitBatch(t *testing.T) indexCall {
	t.Helper()
	select {
	case call := <-pacer.calls:
		return call
	case <-time.After(10 * time.Second):
		t.Fatal("no index build batch started")
		return indexCall{}
	}
}

// release lets the paced project's build run without pausing.
func (pacer *indexPacer) release() {
	close(pacer.free)
}

func (pacer *indexPacer) released() bool {
	select {
	case <-pacer.free:
		return true
	default:
		return false
	}
}

func (pacer *indexPacer) indexedPairs(path string) int {
	pacer.mu.Lock()
	defer pacer.mu.Unlock()
	return pacer.indexed[path]
}

func listIndexComplete(t *testing.T, server *Server) bool {
	t.Helper()
	response := requestListener(t, server, http.MethodGet, "/traffic?limit=1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("listing traffic: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Index *struct {
			Complete bool `json:"complete"`
		} `json:"index"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Index == nil {
		t.Fatalf("decoding traffic list %s: %v", response.Body.String(), err)
	}
	return body.Index.Complete
}

// nextEvent returns the next event published to subscriber, or "" when none
// arrives within wait.
func nextEvent(subscriber *eventSubscriber, wait time.Duration) string {
	select {
	case event := <-subscriber.events:
		return fmt.Sprintf("%s %s", event.name, event.data)
	default:
	}
	select {
	case event := <-subscriber.events:
		return fmt.Sprintf("%s %s", event.name, event.data)
	case <-time.After(wait):
		return ""
	}
}

// newIndexServer creates a service with no open project whose events are
// already subscribed, so no index event can be missed.
func newIndexServer(t *testing.T) (*Server, *ProjectLifecycle, *eventSubscriber) {
	t.Helper()
	lifecycle, _, _ := newTestProjectLifecycle(t)
	server := NewServer(lifecycle.proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "dev", "default", "")
	subscriber := server.events.subscribe()
	t.Cleanup(func() { server.events.unsubscribe(subscriber) })
	return server, lifecycle, subscriber
}

func openProjectPath(t *testing.T, lifecycle *ProjectLifecycle, path string) {
	t.Helper()
	if err := lifecycle.Open(context.Background(), path); err != nil {
		t.Fatalf("opening project %s: %v", path, err)
	}
}

func TestTrafficIndexBuild(t *testing.T) {
	t.Run("should index an existing project in the background and announce completion once", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "existing.marasi"))
		seedUnindexedProject(t, path, 250)
		server, lifecycle, subscriber := newIndexServer(t)
		pacer := paceIndexBuild(lifecycle, path)

		openProjectPath(t, lifecycle, path)
		pacer.waitBatch(t)

		if listIndexComplete(t, server) {
			t.Fatalf("\nwanted:\nindex.complete false while the build runs\ngot:\ntrue")
		}
		pacer.release()
		want := fmt.Sprintf("traffic.index_complete {\"project\":%q}", path)
		if got := nextEvent(subscriber, 30*time.Second); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, got)
		}
		if !listIndexComplete(t, server) {
			t.Fatalf("\nwanted:\nindex.complete true after the build\ngot:\nfalse")
		}
		if got := nextEvent(subscriber, 200*time.Millisecond); got != "" {
			t.Fatalf("\nwanted:\nno second event\ngot:\n%s", got)
		}
		response := requestListener(t, server, http.MethodGet, "/traffic?q=%22legacy+body+7.%22", "")
		if got := listedPaths(t, response.Body.Bytes()); got != "/item/7" {
			t.Fatalf("\nwanted:\nthe legacy pair found by its text\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("should not announce completion when the index is complete on open", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "fresh.marasi"))
		server, lifecycle, subscriber := newIndexServer(t)

		openProjectPath(t, lifecycle, path)

		if !listIndexComplete(t, server) {
			t.Fatalf("\nwanted:\nindex.complete true for a new project\ngot:\nfalse")
		}
		if got := nextEvent(subscriber, 300*time.Millisecond); got != "" {
			t.Fatalf("\nwanted:\nno event\ngot:\n%s", got)
		}
	})

	t.Run("should retry a failed batch and still announce completion once", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "flaky.marasi"))
		seedUnindexedProject(t, path, 150)
		server, lifecycle, subscriber := newIndexServer(t)
		failures := 2
		lifecycle.indexBatch = func(ctx context.Context, path string, builder trafficIndexBuilder) (int, bool, error) {
			if failures > 0 {
				failures--
				return 0, false, errors.New("database is locked")
			}
			return builder.IndexMissingTraffic(indexBuildBatchBytes)
		}

		openProjectPath(t, lifecycle, path)

		want := fmt.Sprintf("traffic.index_complete {\"project\":%q}", path)
		if got := nextEvent(subscriber, 30*time.Second); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, got)
		}
		if !listIndexComplete(t, server) {
			t.Fatalf("\nwanted:\nindex.complete true after the retried build\ngot:\nfalse")
		}
		if got := missingFromIndex(t, path); got != 0 {
			t.Fatalf("\nwanted:\nno pairs missing\ngot:\n%d", got)
		}
		if got := nextEvent(subscriber, 200*time.Millisecond); got != "" {
			t.Fatalf("\nwanted:\nno second event\ngot:\n%s", got)
		}
	})

	t.Run("should skip a pair that keeps failing to index, log it, and still announce completion", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "broken.marasi"))
		seedUnindexedProject(t, path, 20)
		broken := breakPairMetadata(t, path, "/item/7")
		_, lifecycle, subscriber := newIndexServer(t)
		logs := &lockedBuffer{}
		lifecycle.logger = slog.New(slog.NewTextHandler(logs, nil))

		openProjectPath(t, lifecycle, path)

		want := fmt.Sprintf("traffic.index_complete {\"project\":%q}", path)
		if got := nextEvent(subscriber, 30*time.Second); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, got)
		}
		if got := missingFromIndex(t, path); got != 1 {
			t.Fatalf("\nwanted:\nonly the broken pair missing\ngot:\n%d", got)
		}
		skips := strings.Count(logs.String(), "Skipped a pair the traffic index could not index")
		if skips != 1 || !strings.Contains(logs.String(), "id="+broken.String()) {
			t.Fatalf("\nwanted:\none skip logged for %s\ngot:\n%s", broken, logs.String())
		}
	})

	t.Run("should announce completion when the only missing pair is skipped", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "only-broken.marasi"))
		seedUnindexedProject(t, path, 1)
		breakPairMetadata(t, path, "/item/0")
		_, lifecycle, subscriber := newIndexServer(t)

		openProjectPath(t, lifecycle, path)

		want := fmt.Sprintf("traffic.index_complete {\"project\":%q}", path)
		if got := nextEvent(subscriber, 30*time.Second); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, got)
		}
	})

	t.Run("should finish the batch in flight before a switch closes the project", func(t *testing.T) {
		dir := t.TempDir()
		path := canonicalProjectPath(t, filepath.Join(dir, "busy.marasi"))
		other := canonicalProjectPath(t, filepath.Join(dir, "other.marasi"))
		seedUnindexedProject(t, path, 10)
		_, lifecycle, _ := newIndexServer(t)
		started := make(chan struct{})
		var finished atomic.Bool
		lifecycle.indexBatch = func(ctx context.Context, batchPath string, builder trafficIndexBuilder) (int, bool, error) {
			if batchPath != path {
				return 0, false, nil
			}
			close(started)
			// A batch transaction does not stop when the build is
			// cancelled; it commits or rolls back first.
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				return 0, false, errors.New("the switch did not cancel the build")
			}
			time.Sleep(100 * time.Millisecond)
			finished.Store(true)
			return 0, false, ctx.Err()
		}

		openProjectPath(t, lifecycle, path)
		<-started
		openProjectPath(t, lifecycle, other)

		if !finished.Load() {
			t.Fatalf("\nwanted:\nthe switch to wait for the batch in flight\ngot:\nthe project closed while the batch was still running")
		}
	})

	t.Run("should cancel the build on a project switch and resume on reopen", func(t *testing.T) {
		dir := t.TempDir()
		path := canonicalProjectPath(t, filepath.Join(dir, "large.marasi"))
		other := canonicalProjectPath(t, filepath.Join(dir, "other.marasi"))
		seedUnindexedProject(t, path, 250)
		server, lifecycle, subscriber := newIndexServer(t)
		pacer := paceIndexBuild(lifecycle, path)
		// Every pair is larger than one byte, so each batch indexes one.
		pacer.batchBytes = 1

		openProjectPath(t, lifecycle, path)
		pacer.waitBatch(t)
		pacer.step <- struct{}{}
		held := pacer.waitBatch(t)

		// The build is held mid-way; a switch that waited for it would hang.
		openProjectPath(t, lifecycle, other)
		select {
		case <-held.ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the switch did not cancel the build")
		}
		if got := missingFromIndex(t, path); got != 249 {
			t.Fatalf("\nwanted:\n249 pairs left after one batch\ngot:\n%d", got)
		}
		if got := nextEvent(subscriber, 0); got != fmt.Sprintf("project.opened {\"project\":%q}", other) {
			t.Fatalf("\nwanted:\nproject.opened only\ngot:\n%q", got)
		}
		if got := nextEvent(subscriber, 0); got != "" {
			t.Fatalf("\nwanted:\nno event for the cancelled build\ngot:\n%s", got)
		}

		pacer.release()
		openProjectPath(t, lifecycle, path)
		if got := nextEvent(subscriber, 0); got != fmt.Sprintf("project.opened {\"project\":%q}", path) {
			t.Fatalf("\nwanted:\nproject.opened\ngot:\n%q", got)
		}
		want := fmt.Sprintf("traffic.index_complete {\"project\":%q}", path)
		if got := nextEvent(subscriber, 30*time.Second); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%q", want, got)
		}
		if !listIndexComplete(t, server) {
			t.Fatalf("\nwanted:\nindex.complete true after the build\ngot:\nfalse")
		}
		if got := pacer.indexedPairs(path); got != 250 {
			t.Fatalf("\nwanted:\n250 pairs indexed across both opens, none twice\ngot:\n%d", got)
		}
		if got := missingFromIndex(t, path); got != 0 {
			t.Fatalf("\nwanted:\nno pairs missing\ngot:\n%d", got)
		}
	})
}

func listedPaths(t *testing.T, body []byte) string {
	t.Helper()
	var list struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decoding traffic list %s: %v", body, err)
	}
	paths := ""
	for i, item := range list.Items {
		if i > 0 {
			paths += ","
		}
		paths += item.Path
	}
	return paths
}
