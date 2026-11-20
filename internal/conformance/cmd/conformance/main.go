// Command conformance runs the conformance suites through Marasi and scores
// each against its committed direct baseline and deviations file. See
// `make conformance`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/autobahn"
	"github.com/tfkr-ae/marasi/internal/conformance/h1client"
	"github.com/tfkr-ae/marasi/internal/conformance/h1origin"
	"github.com/tfkr-ae/marasi/internal/conformance/h2client"
	"github.com/tfkr-ae/marasi/internal/conformance/h2origin"
	"github.com/tfkr-ae/marasi/internal/conformance/streamorigin"
)

// narrowing are the flags that select less than every case. With each of
// them at its default, an entry for a conformance run and target that ran
// is stale when it matched nothing, whether or not it covers a case. -runs,
// -modes and -peers do not count: the stale check covers only the runs and
// targets that ran.
var narrowing = []string{"cases", "exclude", "approaches"}

func main() {
	var (
		settings                 conformance.Settings
		binary                   string
		suites, runs, approaches string
		cases, exclude           string
		autobahnSuite            autobahn.Suite
		modes, peers             string
		verbose                  bool
		container                conformance.ContainerSettings
	)
	flag.StringVar(&binary, "marasi", "dist/marasi", "marasi binary")
	flag.StringVar(&container.Arch, "linux-arch", runtime.GOARCH, "architecture of Marasi's container")
	flag.StringVar(&container.Binary, "marasi-linux", "", "marasi binary for the container; empty means dist/marasi-linux-<linux-arch>")
	flag.StringVar(&settings.OutDir, "out", "dist/conformance", "reports and tester working directories")
	flag.StringVar(&settings.BaselineDir, "baselines", "internal/conformance/baselines", "committed baselines, one directory per suite")
	flag.StringVar(&suites, "suites", h1origin.Name+","+h1client.Name, "comma-separated suites")
	flag.StringVar(&runs, "runs", "proxy,checkpoint-forward,checkpoint-edit,extension", "comma-separated conformance runs; direct rewrites the baselines")
	flag.StringVar(&cases, "cases", "*", "comma-separated case ID patterns, for example 3.*,7.9.*")
	flag.StringVar(&exclude, "exclude", "", "comma-separated case ID patterns to skip")
	flag.StringVar(&approaches, "approaches", "", "comma-separated client approaches (absolute, connect-plain, connect-tls); empty means all")
	flag.BoolVar(&settings.UpdateKnown, "update-known", false, "rewrite the known entries of deviations.json and score.json from this run")
	flag.IntVar(&settings.Phase, "phase", 0, "phase recorded on the known entries -update-known writes")
	flag.BoolVar(&verbose, "verbose", false, "stream tester output")
	flag.StringVar(&autobahnSuite.Image, "image", autobahn.Image, "autobahn: testsuite image")
	flag.StringVar(&modes, "modes", "fuzzingclient,fuzzingserver", "autobahn: suite modes")
	flag.StringVar(&peers, "peers", "strict,lenient", "autobahn: upstream peers")
	// The limit only catches a hung suite. A strict suite through
	// checkpoint-edit takes 43 to 45 minutes on a GitHub runner, holding and
	// editing every message of the 12.x and 13.x compression cases.
	flag.DurationVar(&autobahnSuite.SuiteLimit, "suite-limit", 75*time.Minute, "autobahn: time limit per mode and peer")
	flag.Parse()

	if settings.UpdateKnown && settings.Phase <= 0 {
		fail(errors.New("-update-known needs -phase, the phase expected to remove the new known entries"))
	}
	// Make passes every flag, so a flag narrows only when its value differs
	// from the default.
	settings.AllCases = true
	for _, name := range narrowing {
		f := flag.Lookup(name)
		if strings.Join(split(f.Value.String()), ",") != strings.Join(split(f.DefValue), ",") {
			settings.AllCases = false
		}
	}
	autobahnSuite.Modes = split(modes)
	autobahnSuite.Peers = split(peers)
	settings.Cases = split(cases)
	settings.Exclude = split(exclude)
	for _, run := range split(runs) {
		settings.Runs = append(settings.Runs, conformance.RunName(run))
	}
	for _, approach := range split(approaches) {
		settings.Approaches = append(settings.Approaches, conformance.Approach(approach))
	}
	for _, name := range split(suites) {
		switch name {
		case autobahn.Name:
			settings.Suites = append(settings.Suites, autobahnSuite)
		case h1client.Name:
			settings.Suites = append(settings.Suites, h1client.Suite{})
		case h1origin.Name:
			settings.Suites = append(settings.Suites, h1origin.Suite{})
		case h2client.Name:
			settings.Suites = append(settings.Suites, h2client.Suite{})
		case h2origin.Name:
			settings.Suites = append(settings.Suites, h2origin.Suite{})
		case streamorigin.Name:
			settings.Suites = append(settings.Suites, streamorigin.Suite{})
		default:
			fail(fmt.Errorf("unknown suite %q", name))
		}
	}
	if container.Binary == "" {
		container.Binary = "dist/marasi-linux-" + container.Arch
	}
	for _, dir := range []*string{&binary, &container.Binary, &settings.OutDir, &settings.BaselineDir} {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			fail(err)
		}
		*dir = abs
	}

	logFile, err := openLog(settings.OutDir)
	if err != nil {
		fail(err)
	}
	defer logFile.Close()
	settings.Log = logFile
	if verbose {
		settings.Log = io.MultiWriter(logFile, os.Stderr)
	}
	fmt.Fprintf(os.Stderr, "conformance: log at %s\n", logFile.Name())
	if file := os.Getenv("GITHUB_STEP_SUMMARY"); file != "" {
		summary, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fail(err)
		}
		defer summary.Close()
		settings.Summary = summary
	}
	// One harness CA per run: the suites' TLS targets and Marasi's
	// container both use it.
	if settings.CA, err = conformance.NewCA(); err != nil {
		fail(err)
	}
	container.CA = settings.CA
	settings.Launch = conformance.Launchers(conformance.MarasiLauncher(binary, settings.Log),
		conformance.ContainerLauncher(container, settings.Log))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	outcome, err := conformance.Run(ctx, settings)
	conformance.WriteTable(os.Stdout, outcome)
	fmt.Printf("\nfinished in %s\n", time.Since(started).Round(time.Second))
	if err != nil {
		fail(err)
	}
	if settings.UpdateKnown {
		// New and went-away entries were just written; only what the
		// update cannot fix still fails.
		for _, result := range outcome.Results {
			if result.Err != nil {
				os.Exit(1)
			}
		}
		if len(outcome.Stale) > 0 {
			os.Exit(1)
		}
		return
	}
	if outcome.Failed() {
		os.Exit(1)
	}
}

func split(value string) []string {
	var parts []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func openLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.Create(filepath.Join(dir, "conformance.log"))
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
	os.Exit(2)
}
