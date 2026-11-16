package conformance

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"
)

// newestWait is how long a proxied run waits for the proxy's newest item to
// change after a target before it decides no traffic went through.
const newestWait = 2 * time.Second

// Settings selects what Run runs and where it keeps its files.
type Settings struct {
	Suites      []Suite
	Runs        []RunName  // conformance runs; a suite skips the proxied runs it does not support
	Cases       []string   // case ID patterns
	Exclude     []string   // case ID patterns to skip
	Approaches  []Approach // client approaches; empty means all
	Full        bool       // nothing was narrowed, so an entry that matched nothing is stale
	BaselineDir string     // committed baselines: <dir>/<suite>/<target>.json, deviations.json, score.json
	OutDir      string     // reports: <dir>/<suite>/<run>/
	Launch      Launcher   // starts the proxy for each proxied run
	UpdateKnown bool       // rewrite the known entries and score.json in BaselineDir
	Phase       int        // phase given to the known entries UpdateKnown writes
	Log         io.Writer  // progress output
	Summary     io.Writer  // receives every Markdown summary too, such as $GITHUB_STEP_SUMMARY; may be nil
}

// Result is one target of a suite under one conformance run.
type Result struct {
	Suite  string
	Run    RunName
	Target string
	Cases  []string // scored case IDs; in the direct run, the cases recorded
	Sorted          // the differences, sorted against the deviations file
	Notes  []string // suite notes, such as timing
	Err    error    // harness failure; the target has no result
}

// Matched counts the scored cases with no difference at all.
func (r Result) Matched() int {
	return len(r.Cases) - len(r.differing())
}

// differing returns the case IDs with any difference, listed or not.
func (r Result) differing() map[string]bool {
	differing := map[string]bool{}
	for _, d := range r.New {
		differing[d.Case] = true
	}
	for _, m := range r.Known {
		differing[m.Case] = true
	}
	for _, m := range r.Allowed {
		differing[m.Case] = true
	}
	return differing
}

// WentAway is a known entry that matched nothing in a run that covered its cases.
type WentAway struct {
	Suite string
	Run   RunName
	Entry Entry
	index int // the entry's position in the suite's deviations file
}

// Stale is an entry that matched nothing in a full run.
type Stale struct {
	Suite string
	Entry Entry
}

// Outcome is everything one Run produced.
type Outcome struct {
	Results  []Result
	WentAway []WentAway
	Stale    []Stale
}

// Failed reports whether the gate fails: a harness error, a new deviation,
// a known deviation that went away, or a stale entry.
func (o Outcome) Failed() bool {
	for _, result := range o.Results {
		if result.Err != nil || len(result.New) > 0 {
			return true
		}
	}
	return len(o.WentAway) > 0 || len(o.Stale) > 0
}

// DeviationsFile is a suite's committed deviations file.
func (s Settings) DeviationsFile(suite string) string {
	return filepath.Join(s.BaselineDir, suite, "deviations.json")
}

// ScoreFile is a suite's committed score.
func (s Settings) ScoreFile(suite string) string {
	return filepath.Join(s.BaselineDir, suite, "score.json")
}

// BaselineFile is the committed direct baseline of a suite's target.
func (s Settings) BaselineFile(suite, target string) string {
	return filepath.Join(s.BaselineDir, suite, target+".json")
}

// Run runs every selected suite under every selected conformance run. A
// direct run merges its recordings into the baselines; any other run is
// compared to them and sorted against the suite's deviations file. Reports
// go to OutDir. The error is a failure that stopped the run; a failure of
// one target is in its Result.
func Run(ctx context.Context, settings Settings) (Outcome, error) {
	var outcome Outcome
	for _, suite := range settings.Suites {
		if len(settings.Approaches) > 0 && !slices.ContainsFunc(suite.Approaches(), func(a Approach) bool {
			return slices.Contains(settings.Approaches, a)
		}) {
			fmt.Fprintf(settings.Log, "== %s: no selected client approach, skipped\n", suite.Name())
			continue
		}
		suiteOutcome, err := settings.runSuite(ctx, suite)
		outcome.Results = append(outcome.Results, suiteOutcome.Results...)
		outcome.WentAway = append(outcome.WentAway, suiteOutcome.WentAway...)
		outcome.Stale = append(outcome.Stale, suiteOutcome.Stale...)
		if err != nil {
			return outcome, fmt.Errorf("%s suite: %w", suite.Name(), err)
		}
	}
	return outcome, nil
}

func (s Settings) runSuite(ctx context.Context, suite Suite) (Outcome, error) {
	var outcome Outcome
	deviations, err := ReadDeviations(s.DeviationsFile(suite.Name()))
	if err != nil {
		return outcome, err
	}
	matchedAny := make([]bool, len(deviations))
	ranProxied := false
	for _, run := range s.Runs {
		if run != RunDirect && !slices.Contains(ProxiedRuns, run) {
			return outcome, fmt.Errorf("unknown conformance run %q", run)
		}
		if run != RunDirect && !slices.Contains(suite.Runs(), run) {
			continue
		}
		dir := filepath.Join(s.OutDir, suite.Name(), string(run))
		var results []Result
		if run == RunDirect {
			results = s.direct(ctx, suite, dir)
		} else {
			ranProxied = true
			var matched []bool
			if results, matched, err = s.proxied(ctx, suite, run, deviations, dir); err != nil {
				outcome.Results = append(outcome.Results, results...)
				return outcome, fmt.Errorf("%s run: %w", run, err)
			}
			wentAway := s.wentAway(suite.Name(), run, deviations, results, matched)
			outcome.WentAway = append(outcome.WentAway, wentAway...)
			for i := range matched {
				matchedAny[i] = matchedAny[i] || matched[i]
			}
			if err := s.writeReports(suite.Name(), run, dir, results, wentAway); err != nil {
				return outcome, err
			}
		}
		outcome.Results = append(outcome.Results, results...)
		if ctx.Err() != nil {
			return outcome, ctx.Err()
		}
	}
	if s.Full && ranProxied {
		for i, entry := range deviations {
			if !matchedAny[i] {
				outcome.Stale = append(outcome.Stale, Stale{Suite: suite.Name(), Entry: entry})
			}
		}
	}
	if err := s.writeScores(suite.Name(), outcome); err != nil {
		return outcome, err
	}
	if s.UpdateKnown {
		updated := updateKnown(deviations, outcome, s.Phase)
		if err := WriteDeviations(s.DeviationsFile(suite.Name()), updated); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

func (s Settings) job(run RunName, target, proxy, dir string) Job {
	return Job{
		Run:        run,
		Target:     target,
		Proxy:      proxy,
		Cases:      s.Cases,
		Exclude:    s.Exclude,
		Approaches: s.Approaches,
		Dir:        filepath.Join(dir, target),
		Log:        s.Log,
	}
}

// direct runs every target with no proxy and merges it into the baselines.
func (s Settings) direct(ctx context.Context, suite Suite, dir string) []Result {
	var results []Result
	for _, target := range suite.Targets() {
		fmt.Fprintf(s.Log, "== %s %s %s\n", suite.Name(), RunDirect, target)
		result := Result{Suite: suite.Name(), Run: RunDirect, Target: target}
		job := s.job(RunDirect, target, "", dir)
		got, err := suite.Execute(ctx, job)
		if err == nil {
			err = mergeBaseline(s.BaselineFile(suite.Name(), target), job, got)
		}
		result.Err = err
		for id := range got {
			result.Cases = append(result.Cases, id)
		}
		slices.Sort(result.Cases)
		results = append(results, result)
		if ctx.Err() != nil {
			break
		}
	}
	return results
}

// proxied runs every target through a proxy launched for run, compares each
// with its baseline, and sorts the differences. matched says which entries
// matched a difference.
func (s Settings) proxied(ctx context.Context, suite Suite, run RunName, deviations Deviations, dir string) ([]Result, []bool, error) {
	matched := make([]bool, len(deviations))
	proxy, err := s.Launch(ctx, suite, run, dir)
	if err != nil {
		return nil, matched, err
	}
	defer proxy.Stop()
	kind := suite.Setup().Traffic
	var results []Result
	for _, target := range suite.Targets() {
		fmt.Fprintf(s.Log, "== %s %s %s\n", suite.Name(), run, target)
		result := Result{Suite: suite.Name(), Run: run, Target: target}
		result.Err = s.compareTarget(ctx, suite, run, target, dir, proxy, kind, deviations, matched, &result)
		results = append(results, result)
		if ctx.Err() != nil {
			return results, matched, ctx.Err()
		}
	}
	return results, matched, nil
}

func (s Settings) compareTarget(ctx context.Context, suite Suite, run RunName, target, dir string, proxy Proxy,
	kind TrafficKind, deviations Deviations, matched []bool, result *Result) error {
	before, err := proxy.Newest(ctx, kind)
	if err != nil {
		return err
	}
	holder, holds := proxy.(Holder)
	if isCheckpointRun(run) && !holds {
		return fmt.Errorf("%w: the proxy cannot report held items", ErrNotHeld)
	}
	var heldBefore int64
	if isCheckpointRun(run) {
		if heldBefore, err = holder.Held(ctx); err != nil {
			return err
		}
	}
	job := s.job(run, target, proxy.Address(), dir)
	got, err := suite.Execute(ctx, job)
	if err != nil {
		return err
	}
	if isCheckpointRun(run) {
		heldAfter, err := holder.Held(ctx)
		if err != nil {
			return err
		}
		if heldAfter == heldBefore {
			return ErrNotHeld
		}
	}
	after, err := proxy.Newest(ctx, kind)
	// Marasi stores what it captured in the background, so the newest
	// item can lag the traffic by a moment.
	for wait := time.Now().Add(newestWait); err == nil && checkProxied(before, after) != nil && time.Now().Before(wait); {
		time.Sleep(50 * time.Millisecond)
		after, err = proxy.Newest(ctx, kind)
	}
	if err != nil {
		return err
	}
	if err := checkProxied(before, after); err != nil {
		return err
	}
	baseline, err := ReadBaseline(s.BaselineFile(suite.Name(), target))
	if err != nil {
		return err
	}
	baseline = baseline.Select(s.Cases, s.Exclude, s.Approaches)
	comparison, err := suite.Compare(target, baseline, got)
	if err != nil {
		return err
	}
	sorted, targetMatched := deviations.sort(run, comparison.Differences)
	for i := range targetMatched {
		matched[i] = matched[i] || targetMatched[i]
	}
	result.Cases, result.Sorted, result.Notes = comparison.Cases, sorted, comparison.Notes
	return writeCaseDiffs(filepath.Join(dir, "diffs", target), result, baseline, got)
}

// wentAway lists the known entries that matched nothing in run although
// run scored at least one case they cover.
func (s Settings) wentAway(suite string, run RunName, deviations Deviations, results []Result, matched []bool) []WentAway {
	var gone []WentAway
	for i, entry := range deviations {
		if entry.Kind != KindKnown || matched[i] {
			continue
		}
		if covered(entry, run, results) {
			gone = append(gone, WentAway{Suite: suite, Run: run, Entry: entry, index: i})
		}
	}
	return gone
}

func covered(entry Entry, run RunName, results []Result) bool {
	for _, result := range results {
		for _, id := range result.Cases {
			if entry.covers(run, id) {
				return true
			}
		}
	}
	return false
}
