// Package connectclient is the conformance harness's connect-client
// suite. The connectrpc/conformance runner, a third-party tester, scores
// its own reference client against its reference servers, gRPC, gRPC-Web
// and Connect over HTTP/1.1 and h2c, with every stream type. The runner
// has no proxy option, so the client under test it runs is our wrapper
// (Wrap, cmd/connectclient-wrapper): it runs the reference client and
// sends each case through a shim, straight to the reference server in the
// direct run and with CONNECT through Marasi in a proxied run. A case's
// recording is the runner's verdict.
//
// The runner kills the client when a case gives no result within its
// fixed 20 seconds, and reports every later case as not run, so the
// wrapper answers a case the reference client leaves past its own,
// shorter deadline with an error, which the runner reports as that case
// failing. A case that deadlocks when Marasi buffers, such as a
// full-duplex bidirectional stream, is then timed-out and the rest of the
// run goes on.
//
// Only the cleartext configurations run. The runner signs the reference
// server's certificate itself and the reference client trusts only that,
// so a TLS case cannot pass through an intercepting proxy. See
// docs/adr/2026-11-20-conformance-testers-for-grpc-and-streams.md.
//
// The suite is a required check on all four conformance runs.
package connectclient

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// Name is the suite's name and its baselines directory.
const Name = "connect-client"

// Target is the suite's one target and baseline file: the runner's
// reference servers, both implementations it starts.
const Target = "reference"

// Runner is the connectrpc/conformance module the harness installs its
// runner and reference client from with go install, so it never enters
// go.mod: connectrpc.com/conformance v1.0.7 (Apache-2.0).
const Runner = "connectrpc.com/conformance"

// RunnerVersion is Runner's pinned version.
const RunnerVersion = "v1.0.7"

// wrapperPackage is the wrapper command, built from this module.
const wrapperPackage = "github.com/tfkr-ae/marasi/internal/conformance/connectclient/cmd/connectclient-wrapper"

// DefaultDeadline is how long the wrapper waits for a case's result: under
// the runner's 20 seconds, with room for the Checkpoint runs, where every
// head and body waits its turn at the resolver.
const DefaultDeadline = 15 * time.Second

// clientParallel is the reference client's limit on RPCs in flight, more
// than the run has cases. A case the wrapper answered at its deadline is
// still in flight in the client, and at the limit the client would stop
// reading new cases until the runner times out.
const clientParallel = 10000

// Parallel is how many unanswered cases the wrapper lets the reference
// client have at once, in place of clientParallel, so the load on the
// proxy, which shows in the Deadline Propagation cases, is the same on
// every machine.
const Parallel = 16

// config lists the client features the runner selects cases by: HTTP/1.1
// and HTTP/2 with h2c, the three protocols, every codec and compression,
// and no TLS.
//
//go:embed config.yaml
var config []byte

// Suite is the connect-client conformance suite.
type Suite struct {
	// Deadline is the wrapper's deadline per case; zero means DefaultDeadline.
	Deadline time.Duration
}

var _ conformance.Suite = Suite{}

// Name is "connect-client".
func (s Suite) Name() string { return Name }

// Runs is all four proxied runs.
func (s Suite) Runs() []conformance.RunName { return conformance.ProxiedRuns }

// Approaches is plaintext inside CONNECT: HTTP/1.1, or h2c by prior knowledge.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachConnectPlain}
}

// Targets is the reference servers.
func (s Suite) Targets() []string { return []string{Target} }

// Deterministic asks the direct run to record every case twice and leave
// out any case whose verdict changes.
func (s Suite) Deterministic() {}

// Setup runs Marasi on the host, which reaches the reference servers on
// loopback, and holds and edits HTTP as an HTTP/1.1 suite does. The
// Checkpoint runs also hold every stream message and trailer block.
func (s Suite) Setup() conformance.Setup {
	setup := conformance.HTTPSetup()
	setup.Checkpoint = append(setup.Checkpoint, conformance.CheckpointStream)
	return setup
}

// Recording is a case's recording: the runner's verdict.
type Recording struct {
	Verdict Verdict `json:"verdict"`
}

// caseID turns the runner's name for a case into a case ID: dots for its
// slashes, since a case pattern's * does not match a slash, and the
// approach. The runner's names have no dots.
func caseID(name string) string {
	return conformance.CaseID(strings.ReplaceAll(name, "/", "."), conformance.ApproachConnectPlain)
}

// Execute lists the runner's cases, then runs the selected ones once
// through the wrapper.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	if len(job.Approaches) > 0 && !slices.Contains(job.Approaches, conformance.ApproachConnectPlain) {
		return conformance.Recordings{}, nil
	}
	log := job.Log
	if log == nil {
		log = io.Discard
	}
	dir, err := filepath.Abs(job.Dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	bin, err := install(ctx, filepath.Join(dir, "bin"), log)
	if err != nil {
		return nil, err
	}
	conf := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(conf, config, 0o644); err != nil {
		return nil, err
	}

	// The listing: every case reaches the wrapper, which answers it at once.
	listEvents := filepath.Join(dir, "list-events.jsonl")
	if _, err := s.runner(ctx, bin, conf, listEvents, nil, "", true, io.Discard); err != nil {
		return nil, err
	}
	listed, err := readEvents(listEvents)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range listed {
		if e.Kind == EventSent && conformance.Selected(caseID(e.Case), job.Cases, job.Exclude, job.Approaches) {
			names = append(names, e.Case)
		}
	}
	if len(listed) == 0 {
		return nil, errors.New("the runner listed no cases")
	}
	if len(names) == 0 {
		return conformance.Recordings{}, nil
	}
	slices.Sort(names)
	var run []string
	if len(names) < countSent(listed) {
		run = names
	}

	events := filepath.Join(dir, "events.jsonl")
	fmt.Fprintf(log, "connect-plain: connectconformance, %d cases\n", len(names))
	output, err := s.runner(ctx, bin, conf, events, run, job.Proxy, false, log)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "connectconformance.log"), output, 0o644); err != nil {
		return nil, err
	}
	happened, err := readEvents(events)
	if err != nil {
		return nil, err
	}
	recordings := conformance.Recordings{}
	for name, verdict := range Verdicts(names, happened, output) {
		data, err := json.Marshal(Recording{Verdict: verdict})
		if err != nil {
			return nil, err
		}
		recordings[caseID(name)] = data
	}
	fmt.Fprintf(log, "connect-plain: recorded %d cases\n", len(recordings))
	return recordings, nil
}

// runner runs connectconformance in client mode with the wrapper as the
// client under test and returns its output; the wrapper writes its events
// to events. When list is set, the wrapper runs no reference client and
// answers every case at once, so its events list the cases. run, when
// set, names the only cases to run. The runner exits non-zero when a case
// fails, so its exit status is not an error.
func (s Suite) runner(ctx context.Context, bin, conf, events string, run []string, proxy string, list bool, log io.Writer) ([]byte, error) {
	if err := os.Remove(events); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	args := []string{"--mode", "client", "--conf", conf}
	if run != nil {
		file := events + ".run"
		if err := os.WriteFile(file, []byte(strings.Join(run, "\n")+"\n"), 0o644); err != nil {
			return nil, err
		}
		args = append(args, "--run", "@"+file)
	}
	// The wrapper passes its arguments on to the reference client.
	args = append(args, "--", filepath.Join(bin, "connectclient-wrapper"), "-p", strconv.Itoa(clientParallel))
	cmd := exec.CommandContext(ctx, filepath.Join(bin, "connectconformance"), args...)
	client := filepath.Join(bin, "referenceclient")
	if list {
		client = ""
	}
	cmd.Env = append(os.Environ(), "MARASI_CC_EVENTS="+events, "MARASI_CC_PROXY="+proxy,
		"MARASI_CC_CLIENT="+client, "MARASI_CC_DEADLINE="+s.deadline().String(), "MARASI_CC_PARALLEL="+strconv.Itoa(Parallel))
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = io.MultiWriter(&output, log)
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return output.Bytes(), fmt.Errorf("running connectconformance: %w", err)
	}
	if ctx.Err() != nil {
		return output.Bytes(), ctx.Err()
	}
	return output.Bytes(), nil
}

// install builds the runner, the reference client and the wrapper into
// bin. Go's build cache makes it quick after the first time.
func install(ctx context.Context, bin string, log io.Writer) (string, error) {
	for _, args := range [][]string{
		{"install", Runner + "/cmd/connectconformance@" + RunnerVersion},
		{"install", Runner + "/cmd/referenceclient@" + RunnerVersion},
		{"build", "-o", filepath.Join(bin, "connectclient-wrapper"), wrapperPackage},
	} {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Env = append(os.Environ(), "GOBIN="+bin, "GOFLAGS=")
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
		}
	}
	return bin, nil
}

func readEvents(file string) ([]Event, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", file, err)
		}
		events = append(events, e)
	}
	return events, nil
}

func countSent(events []Event) int {
	n := 0
	for _, e := range events {
		if e.Kind == EventSent {
			n++
		}
	}
	return n
}

// Compare compares each case's verdict with the baseline, in ID order.
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	return conformance.CompareCases(baseline, got, conformance.CaseComparison{Diff: compareCase})
}

func compareCase(caseID string, wantData, gotData json.RawMessage) ([]conformance.Difference, error) {
	var want, got Recording
	if err := json.Unmarshal(wantData, &want); err != nil {
		return nil, fmt.Errorf("decoding the baseline: %w", err)
	}
	if err := json.Unmarshal(gotData, &got); err != nil {
		return nil, err
	}
	if want.Verdict == got.Verdict {
		return nil, nil
	}
	return []conformance.Difference{{Case: caseID, Field: "verdict", Want: string(want.Verdict), Got: string(got.Verdict)}}, nil
}

func (s Suite) deadline() time.Duration {
	if s.Deadline == 0 {
		return DefaultDeadline
	}
	return s.Deadline
}
