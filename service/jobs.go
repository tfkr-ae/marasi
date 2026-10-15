package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/tfkr-ae/marasi/db"
)

// ErrNoOpenProject means the service instance has no open project.
var ErrNoOpenProject = errors.New("no open project")

// jobBatchBytes caps the stored text one background job batch processes.
// Batches share the project's single database connection with capture and
// reads, so they stay small (ADR-0026). The cap is in bytes, not pairs, so
// large bodies do not hold the connection for seconds; a pair larger than the
// cap is processed in a batch of its own.
const jobBatchBytes = 1 << 20

// jobRetryDelay is how long a background job waits before retrying a failed
// batch.
const jobRetryDelay = time.Second

// jobProgressInterval is the shortest time between two job.progress events
// for one job.
const jobProgressInterval = time.Second

// jobRunner is the repository side of a project's background jobs
// (ADR-0039).
type jobRunner interface {
	ListJobs() ([]db.JobState, error)
	RunJobBatch(name string, maxBytes int) (processed int, remaining bool, err error)
	RebuildJob(name string) error
}

// runJobBatch runs one batch of the named job for the project at path.
func runJobBatch(_ context.Context, _ string, runner jobRunner, name string) (int, bool, error) {
	return runner.RunJobBatch(name, jobBatchBytes)
}

// jobPublisher turns background job reports into service events.
type jobPublisher struct {
	progress func(path string, job db.JobState)
	// complete reports that the named job finished. searchIndexComplete is
	// true when the job is one of db.SearchIndexJobs and all of them are now
	// complete.
	complete func(path, name string, searchIndexComplete bool)
}

// completedJob is a job completion held until the publisher is installed.
type completedJob struct {
	path, name          string
	searchIndexComplete bool
}

// jobEvents is the internal hook through which ProjectLifecycle reports
// background job progress to the service. A job can finish before the server
// installs its publisher, because the first project opens before the server
// exists; that completion is held until install. Progress reported before
// install is dropped.
type jobEvents struct {
	mu        sync.Mutex
	publisher *jobPublisher
	pending   []completedJob
}

func (events *jobEvents) install(publisher jobPublisher) {
	events.mu.Lock()
	defer events.mu.Unlock()
	events.publisher = &publisher
	for _, completed := range events.pending {
		publisher.complete(completed.path, completed.name, completed.searchIndexComplete)
	}
	events.pending = nil
}

// progress reports job's progress unless ctx was cancelled first.
func (events *jobEvents) progress(ctx context.Context, path string, job db.JobState) {
	events.mu.Lock()
	defer events.mu.Unlock()
	if ctx.Err() != nil || events.publisher == nil {
		return
	}
	events.publisher.progress(path, job)
}

// complete reports that the named job finished, unless ctx was cancelled
// first.
func (events *jobEvents) complete(ctx context.Context, path, name string, searchIndexComplete bool) {
	events.mu.Lock()
	defer events.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if events.publisher == nil {
		events.pending = append(events.pending, completedJob{path: path, name: name, searchIndexComplete: searchIndexComplete})
		return
	}
	events.publisher.complete(path, name, searchIndexComplete)
}

func projectJobRunner(project *openProject) (jobRunner, bool) {
	repository, ok := project.resources.Repository.(*eventLogRepository)
	if !ok {
		return nil, false
	}
	runner, ok := repository.RepositoryProvider.(jobRunner)
	return runner, ok
}

// startJobs runs the project's incomplete background jobs in the background,
// one after another in registry order. It runs outside the project gate, so a
// switch does not wait for a whole job: closing the project cancels the jobs
// between batches and waits only for the batch in flight to commit or roll
// back. A failed batch is retried after a short delay, and a pair that keeps
// failing is logged and skipped.
func (lifecycle *ProjectLifecycle) startJobs(project *openProject) {
	runner, ok := projectJobRunner(project)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(lifecycle.executionContext)
	done := make(chan struct{})
	project.cancelJobs = cancel
	project.jobsDone = done
	go func() {
		defer close(done)
		defer cancel()
		finished := map[string]bool{}
		for ctx.Err() == nil {
			jobs, err := runner.ListJobs()
			if err != nil {
				lifecycle.logError("Failed to read background jobs; retrying", project.path, err)
				if !sleepContext(ctx, jobRetryDelay) {
					return
				}
				continue
			}
			next := ""
			for _, job := range jobs {
				if !job.Complete && !finished[job.Name] {
					next = job.Name
					break
				}
			}
			if next == "" {
				return
			}
			lifecycle.runJob(ctx, project.path, runner, next)
			finished[next] = true
		}
	}()
}

// stopJobs cancels the project's background jobs and waits for the batch in
// flight. Closing the database under it would leave the project file locked.
func stopJobs(project *openProject) {
	if project.cancelJobs == nil {
		return
	}
	project.cancelJobs()
	<-project.jobsDone
	project.cancelJobs = nil
	project.jobsDone = nil
}

// runJob runs the named job until it completes or ctx is cancelled, reporting
// progress at most once per jobProgressInterval.
func (lifecycle *ProjectLifecycle) runJob(ctx context.Context, path string, runner jobRunner, name string) {
	batch := lifecycle.jobBatch
	var reported time.Time
	for ctx.Err() == nil {
		processed, remaining, err := batch(ctx, path, runner, name)
		var skipped *db.SkippedPairError
		if errors.As(err, &skipped) {
			// The job left the pair out after retrying it; carry on
			// with the rest.
			if lifecycle.logger != nil {
				lifecycle.logger.Warn("Skipped a pair the traffic index could not index", "path", path, "job", name, "id", skipped.ID, "error", skipped.Err)
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A failed batch rolled back. Retry it until the project
			// closes, so one failure does not leave the job partial.
			lifecycle.logError("Failed to run a background job batch; retrying", path, err, "job", name)
			if !sleepContext(ctx, jobRetryDelay) {
				return
			}
			continue
		}
		if !remaining {
			lifecycle.jobEvents.complete(ctx, path, name, completesSearchIndex(runner, name))
			return
		}
		if processed > 0 && time.Since(reported) >= lifecycle.jobProgressInterval {
			if job, ok := findJobState(runner, name); ok {
				lifecycle.jobEvents.progress(ctx, path, job)
				reported = time.Now()
			}
		}
	}
}

// completesSearchIndex reports whether the named job, which just finished, is
// one of db.SearchIndexJobs and all of them are now complete, so that traffic
// reads report index.complete true.
func completesSearchIndex(runner jobRunner, name string) bool {
	if !slices.Contains(db.SearchIndexJobs, name) {
		return false
	}
	for _, other := range db.SearchIndexJobs {
		if job, ok := findJobState(runner, other); !ok || !job.Complete {
			return false
		}
	}
	return true
}

func findJobState(runner jobRunner, name string) (db.JobState, bool) {
	jobs, err := runner.ListJobs()
	if err != nil {
		return db.JobState{}, false
	}
	for _, job := range jobs {
		if job.Name == name {
			return job, true
		}
	}
	return db.JobState{}, false
}

func (lifecycle *ProjectLifecycle) logError(message, path string, err error, args ...any) {
	if lifecycle.logger != nil {
		lifecycle.logger.Error(message, append([]any{"path", path, "error", err}, args...)...)
	}
}

// sleepContext waits for delay and reports whether ctx is still live.
func sleepContext(ctx context.Context, delay time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// ListJobs returns the saved state of each background job of the open
// project.
func (lifecycle *ProjectLifecycle) ListJobs() ([]db.JobState, error) {
	project := lifecycle.openProject()
	if project == nil {
		return nil, ErrNoOpenProject
	}
	runner, ok := projectJobRunner(project)
	if !ok {
		return nil, ErrNoOpenProject
	}
	return runner.ListJobs()
}

// RebuildJob resets the named background job of the open project and starts
// it again. It returns db.ErrUnknownJob for a name no job has, and
// db.ErrJobNotRebuildable for a job that cannot be rebuilt.
func (lifecycle *ProjectLifecycle) RebuildJob(name string) error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	project := lifecycle.openProject()
	if project == nil {
		return ErrNoOpenProject
	}
	runner, ok := projectJobRunner(project)
	if !ok {
		return ErrNoOpenProject
	}
	// Restart the jobs so that the rebuilt job runs even when the jobs had
	// all finished.
	stopJobs(project)
	defer lifecycle.startJobs(project)
	return runner.RebuildJob(name)
}

// job is a background job in the service API.
type job struct {
	Name     string `json:"name"`
	Complete bool   `json:"complete"`
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
}

func jobFromState(state db.JobState) job {
	return job{Name: state.Name, Complete: state.Complete, Done: state.Done, Total: state.Total}
}

// addJobRoutes registers the background job routes. Listing is project-bound
// work. A rebuild is not: it takes the lifecycle lock, which a project switch
// holds while it waits for admitted work.
func addJobRoutes(projectRoutes routeMux, mux *http.ServeMux, projects *ProjectLifecycle, events *eventBroadcaster) {
	if projects == nil {
		return
	}
	projectRoutes.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		states, err := projects.ListJobs()
		if errors.Is(err, ErrNoOpenProject) {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		items := make([]job, len(states))
		for i, state := range states {
			items[i] = jobFromState(state)
		}
		writeJSON(w, r, http.StatusOK, struct {
			Items []job `json:"items"`
		}{Items: items})
	})
	mux.HandleFunc("POST /jobs/{name}/rebuild", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		err := projects.RebuildJob(name)
		switch {
		case errors.Is(err, ErrNoOpenProject), errors.Is(err, db.ErrUnknownJob):
			writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		case errors.Is(err, db.ErrJobNotRebuildable):
			writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "not_rebuildable"})
			return
		case err != nil:
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		if name == db.SitemapJob {
			// The tree is empty until the job refills it, so clients
			// re-read the nodes they have expanded.
			events.publishSitemapChanged("rebuild")
		}
		states, err := projects.ListJobs()
		if err != nil {
			writeJSON(w, r, http.StatusInternalServerError, map[string]string{"error": "internal_server_error"})
			return
		}
		for _, state := range states {
			if state.Name == name {
				writeJSON(w, r, http.StatusAccepted, jobFromState(state))
				return
			}
		}
		writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
	})
}
