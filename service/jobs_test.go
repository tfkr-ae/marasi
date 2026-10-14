package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
)

type listedJob struct {
	Name     string `json:"name"`
	Complete bool   `json:"complete"`
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
}

func listJobs(t *testing.T, server *Server) []listedJob {
	t.Helper()
	response := requestListener(t, server, http.MethodGet, "/jobs", "")
	if response.Code != http.StatusOK {
		t.Fatalf("listing jobs: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Items []listedJob `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding jobs %s: %v", response.Body.String(), err)
	}
	return body.Items
}

// eventNamed waits for the next event called name, skipping others, and
// returns its data, or "" when none arrives within wait.
func eventNamed(subscriber *eventSubscriber, name string, wait time.Duration) string {
	deadline := time.After(wait)
	for {
		select {
		case event := <-subscriber.events:
			if event.name == name {
				return string(event.data)
			}
		case <-deadline:
			return ""
		}
	}
}

// eventsUntil collects the names of events published until one called last,
// or until wait passes.
func eventsUntil(subscriber *eventSubscriber, last string, wait time.Duration) []string {
	deadline := time.After(wait)
	var names []string
	for {
		select {
		case event := <-subscriber.events:
			names = append(names, event.name+" "+string(event.data))
			if event.name == last {
				return names
			}
		case <-deadline:
			return names
		}
	}
}

// refusingRebuild is a project repository whose jobs cannot be rebuilt.
type refusingRebuild struct {
	*db.Repository
}

func (refusingRebuild) RebuildJob(name string) error {
	return fmt.Errorf("%w: %s", db.ErrJobNotRebuildable, name)
}

func TestJobs(t *testing.T) {
	t.Run("should list the search index jobs of a new project as complete", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "fresh.marasi"))
		server, lifecycle, _ := newIndexServer(t)
		openProjectPath(t, lifecycle, path)

		got := listJobs(t, server)
		want := []listedJob{{Name: db.TrafficIndexJob, Complete: true}, {Name: db.AnnotationIndexJob, Complete: true}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should announce progress and completion of a build", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "existing.marasi"))
		seedUnindexedProject(t, path, 20)
		server, lifecycle, subscriber := newIndexServer(t)
		pacer := paceIndexBuild(lifecycle, path)
		// Every pair is larger than one byte, so each batch indexes one.
		pacer.batchBytes = 1
		pacer.release()

		openProjectPath(t, lifecycle, path)

		got := eventsUntil(subscriber, "traffic.index_complete", 30*time.Second)
		// The traffic index build writes both indexes, so the annotation
		// index backfill that follows it finds nothing left to do.
		want := []string{
			`job.progress {"name":"traffic_index","done":1,"total":20}`,
			`job.complete {"name":"traffic_index"}`,
			`job.complete {"name":"annotation_index"}`,
			fmt.Sprintf("traffic.index_complete {\"project\":%q}", path),
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
		jobs := listJobs(t, server)
		wantJobs := []listedJob{
			{Name: db.TrafficIndexJob, Complete: true, Done: 20, Total: 20},
			{Name: db.AnnotationIndexJob, Complete: true, Done: 20, Total: 20},
		}
		if fmt.Sprint(jobs) != fmt.Sprint(wantJobs) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", wantJobs, jobs)
		}
	})

	t.Run("should keep progress when a build resumes after reopening", func(t *testing.T) {
		dir := t.TempDir()
		path := canonicalProjectPath(t, filepath.Join(dir, "large.marasi"))
		other := canonicalProjectPath(t, filepath.Join(dir, "other.marasi"))
		seedUnindexedProject(t, path, 30)
		_, lifecycle, subscriber := newIndexServer(t)
		pacer := paceIndexBuild(lifecycle, path)
		pacer.batchBytes = 1

		openProjectPath(t, lifecycle, path)
		// Two batches run, and the third is held when the project closes.
		pacer.waitBatch(t)
		pacer.step <- struct{}{}
		pacer.waitBatch(t)
		pacer.step <- struct{}{}
		pacer.waitBatch(t)
		openProjectPath(t, lifecycle, other)

		pacer.release()
		openProjectPath(t, lifecycle, path)
		// Skip the events of the first open, up to the reopen.
		for range 2 {
			eventNamed(subscriber, "project.opened", time.Second)
		}
		got := eventNamed(subscriber, "job.progress", 30*time.Second)
		if want := `{"name":"traffic_index","done":3,"total":30}`; got != want {
			t.Fatalf("\nwanted:\nprogress resumed from the saved state %s\ngot:\n%s", want, got)
		}
	})

	t.Run("should rebuild the traffic index on request", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "rebuild.marasi"))
		seedUnindexedProject(t, path, 5)
		server, lifecycle, subscriber := newIndexServer(t)
		openProjectPath(t, lifecycle, path)
		if got := eventNamed(subscriber, "traffic.index_complete", 30*time.Second); got == "" {
			t.Fatal("the first build did not complete")
		}
		pacer := paceIndexBuild(lifecycle, path)

		response := requestListener(t, server, http.MethodPost, "/jobs/traffic_index/rebuild", "")
		if response.Code != http.StatusAccepted {
			t.Fatalf("\nwanted:\n202\ngot:\n%d %s", response.Code, response.Body.String())
		}
		pacer.waitBatch(t)
		if listIndexComplete(t, server) {
			t.Fatalf("\nwanted:\nindex.complete false while the rebuild runs\ngot:\ntrue")
		}
		pacer.release()
		if got := eventNamed(subscriber, "job.complete", 30*time.Second); got != `{"name":"traffic_index"}` {
			t.Fatalf("\nwanted:\njob.complete for the rebuild\ngot:\n%q", got)
		}
		if got := pacer.indexedPairs(path); got != 5 {
			t.Fatalf("\nwanted:\nevery pair indexed again\ngot:\n%d", got)
		}
		if !listIndexComplete(t, server) {
			t.Fatalf("\nwanted:\nindex.complete true after the rebuild\ngot:\nfalse")
		}
	})

	t.Run("should not find a job it does not know", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "fresh.marasi"))
		server, lifecycle, _ := newIndexServer(t)
		openProjectPath(t, lifecycle, path)

		response := requestListener(t, server, http.MethodPost, "/jobs/no_such_job/rebuild", "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("\nwanted:\n404\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("should refuse to rebuild a job that cannot be rebuilt", func(t *testing.T) {
		path := canonicalProjectPath(t, filepath.Join(t.TempDir(), "fresh.marasi"))
		server, lifecycle, _ := newIndexServer(t)
		prepare := lifecycle.prepare
		lifecycle.prepare = func(ctx context.Context, path string) (marasi.ProjectResources, error) {
			resources, err := prepare(ctx, path)
			if repository, ok := resources.Repository.(*eventLogRepository); ok {
				repository.RepositoryProvider = refusingRebuild{repository.RepositoryProvider.(*db.Repository)}
			}
			return resources, err
		}
		openProjectPath(t, lifecycle, path)

		response := requestListener(t, server, http.MethodPost, "/jobs/traffic_index/rebuild", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("\nwanted:\n400\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})
}
