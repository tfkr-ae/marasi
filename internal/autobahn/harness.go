package autobahn

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

// Runs. direct has no proxy and writes the baseline; the others go through a
// Marasi instance and are compared to it.
const (
	RunDirect            = "direct"
	RunProxy             = "proxy"
	RunCheckpointForward = "checkpoint-forward"
	RunCheckpointEdit    = "checkpoint-edit"
	RunExtension         = "extension"
)

// Config selects what the harness runs.
type Config struct {
	Binary      string        // marasi binary
	Image       string        // Autobahn testsuite image
	OutDir      string        // per-run working directories and reports
	BaselineDir string        // committed direct baseline, one file per mode and peer
	Cases       []string      // Autobahn case patterns
	Exclude     []string      // Autobahn case patterns to skip
	Modes       []string      // fuzzingclient, fuzzingserver
	Peers       []string      // strict, lenient
	Runs        []string      // direct, proxy, checkpoint-forward, checkpoint-edit, extension
	SuiteLimit  time.Duration // limit per mode and peer
	Log         io.Writer     // progress output
}

// BaselineFile is the committed baseline for mode and peer.
func (c Config) BaselineFile(mode, peer string) string {
	return filepath.Join(c.BaselineDir, mode+"-"+peer+".json")
}

// Run executes every selected run, mode, and peer. A direct run merges its
// cases into the baseline; any other run is compared to the baseline.
func Run(ctx context.Context, config Config) ([]SuiteResult, error) {
	var results []SuiteResult
	for _, run := range config.Runs {
		runResults, err := config.run(ctx, run)
		results = append(results, runResults...)
		if err != nil {
			return results, fmt.Errorf("%s run: %w", run, err)
		}
	}
	return results, nil
}

func (c Config) run(ctx context.Context, run string) ([]SuiteResult, error) {
	proxy := ""
	if run != RunDirect {
		instance, stop, err := c.startInstance(ctx, run)
		if err != nil {
			return nil, err
		}
		defer stop()
		proxy = instance.Proxy
	}

	var results []SuiteResult
	for _, mode := range c.Modes {
		for _, peer := range c.Peers {
			fmt.Fprintf(c.Log, "== %s %s/%s\n", run, mode, peer)
			result := SuiteResult{Run: run, Mode: mode, Peer: peer}
			report, err := c.suite(ctx, run, mode, peer, proxy)
			if err != nil {
				result.Err = err
				results = append(results, result)
				if ctx.Err() != nil {
					return results, ctx.Err()
				}
				continue
			}
			result.Cases = len(report)
			if run == RunDirect {
				result.Err = c.mergeBaseline(mode, peer, report)
			} else {
				baseline, err := ReadBaseline(c.BaselineFile(mode, peer))
				if err != nil {
					result.Err = err
				} else {
					selected := baseline.Select(c.Cases, c.Exclude)
					result.Deviations = Compare(selected, report)
					result.Timing = PerformanceTiming(selected, report)
				}
			}
			results = append(results, result)
		}
	}
	return results, nil
}

func (c Config) suite(ctx context.Context, run, mode, peer, proxy string) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, c.SuiteLimit)
	defer cancel()
	suite := Suite{
		Image:   c.Image,
		Dir:     filepath.Join(c.OutDir, run, mode+"-"+peer),
		Cases:   c.Cases,
		Exclude: c.Exclude,
		Log:     c.Log,
	}
	return suite.Run(ctx, mode, peer, proxy)
}

// startInstance launches a fresh Marasi instance and sets it up for run.
func (c Config) startInstance(ctx context.Context, run string) (*Instance, func(), error) {
	instance, err := StartInstance(ctx, c.Binary, "autobahn-"+run)
	if err != nil {
		return nil, nil, err
	}
	stops := []func(){instance.Stop}
	stop := func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
	}
	switch run {
	case RunProxy:
	case RunCheckpointForward, RunCheckpointEdit:
		if err := instance.EnableWebSocketCheckpoint(ctx); err != nil {
			stop()
			return nil, nil, err
		}
		resolver := &Resolver{Edit: run == RunCheckpointEdit}
		resolveCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			resolver.Run(resolveCtx, instance)
			close(done)
		}()
		stops = append(stops, func() {
			cancel()
			<-done
			fmt.Fprintf(c.Log, "resolver forwarded %d items, %d rejected\n", resolver.Forwarded.Load(), resolver.Failed.Load())
		})
	case RunExtension:
		if err := instance.InstallEchoExtension(ctx); err != nil {
			stop()
			return nil, nil, err
		}
	default:
		stop()
		return nil, nil, fmt.Errorf("unknown run %q", run)
	}
	return instance, stop, nil
}

// mergeBaseline replaces the selected cases in the committed baseline with report.
func (c Config) mergeBaseline(mode, peer string, report Report) error {
	file := c.BaselineFile(mode, peer)
	baseline, err := ReadBaseline(file)
	if err != nil {
		return err
	}
	for id := range baseline.Select(c.Cases, c.Exclude) {
		delete(baseline, id)
	}
	for id, result := range report {
		baseline[id] = result
	}
	return WriteBaseline(file, baseline)
}
