package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tfkr-ae/marasi/db"
)

// indexBuildBatchBytes caps the stored text one build transaction indexes.
// Batches share the project's single database connection with capture and
// reads, so they stay small (ADR-0026). The cap is in bytes, not pairs, so
// large bodies do not hold the connection for seconds; a pair larger than the
// cap is indexed in a batch of its own.
const indexBuildBatchBytes = 1 << 20

// indexBuildRetryDelay is how long the build waits before retrying a failed
// batch.
const indexBuildRetryDelay = time.Second

// trafficIndexBuilder is the repository side of the background traffic index
// build.
type trafficIndexBuilder interface {
	IndexMissingTraffic(maxBytes int) (indexed int, remaining bool, err error)
}

// indexMissingTraffic runs one build batch for the project at path.
func indexMissingTraffic(_ context.Context, _ string, builder trafficIndexBuilder) (int, bool, error) {
	return builder.IndexMissingTraffic(indexBuildBatchBytes)
}

// indexCompletion announces finished index builds. A build can finish before
// the server installs its publisher, because the first project opens before
// the server exists; that announcement is held until install.
type indexCompletion struct {
	mu      sync.Mutex
	publish func(path string)
	pending []string
}

func (completion *indexCompletion) install(publish func(path string)) {
	completion.mu.Lock()
	defer completion.mu.Unlock()
	completion.publish = publish
	for _, path := range completion.pending {
		publish(path)
	}
	completion.pending = nil
}

// announce publishes completion for path unless ctx was cancelled first.
func (completion *indexCompletion) announce(ctx context.Context, path string) {
	completion.mu.Lock()
	defer completion.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if completion.publish == nil {
		completion.pending = append(completion.pending, path)
		return
	}
	completion.publish(path)
}

// startIndexBuild indexes the project's pairs missing from the traffic index
// in the background. It runs outside the project gate, so a switch does not
// wait for the whole build: closing the project cancels it between batches and
// waits only for the batch in flight to commit or roll back. A failed batch is
// retried after a short delay, and a pair that keeps failing is logged and
// skipped.
func (lifecycle *ProjectLifecycle) startIndexBuild(project *openProject) {
	repository, ok := project.resources.Repository.(*eventLogRepository)
	if !ok {
		return
	}
	builder, ok := repository.RepositoryProvider.(trafficIndexBuilder)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(lifecycle.executionContext)
	done := make(chan struct{})
	project.cancelIndexBuild = cancel
	project.indexBuildDone = done
	indexBatch := lifecycle.indexBatch
	go func() {
		defer close(done)
		defer cancel()
		built := false
		for ctx.Err() == nil {
			indexed, remaining, err := indexBatch(ctx, project.path, builder)
			var skipped *db.SkippedPairError
			if errors.As(err, &skipped) {
				// The build left the pair out after retrying it; carry on
				// with the rest. The skip changed index.complete, so it
				// counts toward announcing completion.
				if lifecycle.logger != nil {
					lifecycle.logger.Warn("Skipped a pair the traffic index could not index", "path", project.path, "id", skipped.ID, "error", skipped.Err)
				}
				built = true
				continue
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if lifecycle.logger != nil {
					lifecycle.logger.Error("Failed to build the traffic index; retrying", "path", project.path, "error", err)
				}
				// A failed batch rolled back. Retry it until the project
				// closes, so one failure does not leave the index partial.
				select {
				case <-ctx.Done():
					return
				case <-time.After(indexBuildRetryDelay):
				}
				continue
			}
			built = built || indexed > 0
			if !remaining {
				if built {
					lifecycle.indexEvents.announce(ctx, project.path)
				}
				return
			}
		}
	}()
}
