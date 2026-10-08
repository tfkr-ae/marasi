package service

import (
	"context"
	"sync"
	"time"
)

// indexBuildBatchSize is how many pairs one build transaction indexes. Batches
// share the project's single database connection with capture and reads, so
// they stay small (ADR-0026).
const indexBuildBatchSize = 100

// indexBuildRetryDelay is how long the build waits before retrying a failed
// batch.
const indexBuildRetryDelay = time.Second

// trafficIndexBuilder is the repository side of the background traffic index
// build.
type trafficIndexBuilder interface {
	IndexMissingTraffic(limit int) (indexed int, remaining bool, err error)
}

// indexMissingTraffic runs one build batch for the project at path.
func indexMissingTraffic(_ context.Context, _ string, builder trafficIndexBuilder) (int, bool, error) {
	return builder.IndexMissingTraffic(indexBuildBatchSize)
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
// wait for it. A failed batch is retried after a short delay. Closing the
// project cancels it between batches; the batch in flight commits or rolls
// back on its own.
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
	project.cancelIndexBuild = cancel
	indexBatch := lifecycle.indexBatch
	go func() {
		defer cancel()
		built := false
		for ctx.Err() == nil {
			indexed, remaining, err := indexBatch(ctx, project.path, builder)
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
