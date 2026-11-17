// Package h2client is the conformance harness's HTTP/2 client-leg suite.
// h2spec, a third-party tester, tests nginx's TLS h2 listener through the
// shim: straight to nginx in the direct run, and with CONNECT through
// Marasi in the proxy run. h2spec does its own TLS (-t -k), so the shim
// only tunnels. A case's recording is h2spec's verdict, as with Autobahn,
// and how the shim saw the case's connection end.
//
// The harness runs h2spec once per case, so a case h2spec cannot start
// (it stops the whole run at the first one) still gets a verdict, and the
// one connection a case opens is the one the shim accepted while it ran.
//
// nginx listens for Marasi at nginx.test, which Marasi's container
// resolves to 127.0.0.2, on the shim's own port, as for h1-client. h2spec
// dials the shim by address, so its :authority is 127.0.0.1 with the
// shim's port; a proxy that routes h2 requests by :authority rather than
// by the CONNECT target reaches the shim, not nginx.
//
// The suite is report only in Phase 0: run it apart from the h1 suites.
package h2client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/nginx"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

// Name is the suite's name and its baselines directory.
const Name = "h2-client"

// Target is the suite's one target and baseline file.
const Target = "nginx"

// TargetHost is nginx's name in CONNECT.
const TargetHost = "nginx.test"

// marasiAddress is where Marasi's container resolves TargetHost: a
// loopback address the shim does not listen on.
const marasiAddress = "127.0.0.2"

// Suite is the h2-client conformance suite.
type Suite struct {
	// Timeout is h2spec's timeout per case, in seconds; zero means 2,
	// h2spec's default.
	Timeout int
	// Settle is how long the shim watches nginx after h2spec leaves; zero
	// means the shim's default.
	Settle time.Duration
}

var _ conformance.Suite = Suite{}

// Name is "h2-client".
func (s Suite) Name() string { return Name }

// Runs is only proxy: the h2 Checkpoint and extension runs come with
// HTTP/2 itself (Phase 5).
func (s Suite) Runs() []conformance.RunName { return []conformance.RunName{conformance.RunProxy} }

// Approaches is TLS inside CONNECT, with ALPN h2.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachConnectTLS}
}

// Targets is nginx.
func (s Suite) Targets() []string { return []string{Target} }

// Deterministic asks the direct run to record every case twice and leave
// out any case whose verdict or end changes, such as one that passes only
// within h2spec's timeout.
func (s Suite) Deterministic() {}

// Setup runs Marasi in a container that resolves nginx's name to
// marasiAddress. The went-through check is the intercepted TLS, which a
// proxy that answers ALPN with http/1.1, and so stores no h2 traffic,
// still shows.
func (s Suite) Setup() conformance.Setup {
	return conformance.Setup{Traffic: conformance.TrafficTLS, Container: true, Hosts: []string{TargetHost + ":" + marasiAddress}}
}

// Execute runs h2spec once per selected case through one shim, against
// one nginx, after checkRoute has shown the shim's route reaches TLS from
// the run's side: the proxy's in a proxied run, nginx's in the direct run.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	if len(job.Approaches) > 0 && !slices.Contains(job.Approaches, conformance.ApproachConnectTLS) {
		return conformance.Recordings{}, nil
	}
	log := job.Log
	if log == nil {
		log = io.Discard
	}
	if err := os.MkdirAll(job.Dir, 0o755); err != nil {
		return nil, err
	}
	h2spec, err := installH2spec(ctx, job.Dir, log)
	if err != nil {
		return nil, err
	}
	all, err := dryRun(ctx, h2spec)
	if err != nil {
		return nil, err
	}
	var tests []Test
	for _, test := range all {
		if conformance.Selected(conformance.CaseID(test.Name(), conformance.ApproachConnectTLS), job.Cases, job.Exclude, job.Approaches) {
			tests = append(tests, test)
		}
	}
	if len(tests) == 0 {
		return conformance.Recordings{}, nil
	}

	// The shim at 127.0.0.1:S, nginx at 127.0.0.2:S for Marasi and at
	// 127.0.0.1:D for the direct run.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	direct, err := nginx.FreePort()
	if err != nil {
		listener.Close()
		return nil, err
	}
	sh, err := shim.Start(shim.Options{Listener: listener, Target: net.JoinHostPort(TargetHost, port),
		Dial: net.JoinHostPort("127.0.0.1", direct), Proxy: job.Proxy, Settle: s.Settle})
	if err != nil {
		listener.Close()
		return nil, err
	}
	closed := false
	defer func() {
		if !closed {
			sh.Close()
		}
	}()
	run := job.Run
	if run == "" {
		run = conformance.RunDirect
	}
	prefix := fmt.Sprintf("marasi-conformance-%d-%s-%s", os.Getpid(), Name, run)
	server, err := nginx.Start(ctx, nginx.Config{Name: prefix + "-nginx", CA: job.CA, Names: []string{TargetHost},
		Listeners: []nginx.Listener{
			{Address: net.JoinHostPort(marasiAddress, port), TLS: true, HTTP2: true},
			{Address: net.JoinHostPort("127.0.0.1", direct), TLS: true, HTTP2: true},
		}})
	if err != nil {
		return nil, err
	}
	defer server.Stop(filepath.Join(job.Dir, "nginx.log"))
	if err := checkRoute(ctx, sh.Address(), job); err != nil {
		return nil, err
	}

	console, err := os.Create(filepath.Join(job.Dir, "h2spec-connect-tls.log"))
	if err != nil {
		return nil, err
	}
	defer console.Close()
	fmt.Fprintf(log, "connect-tls: h2spec, %d cases, through the shim at %s\n", len(tests), sh.Address())
	type ran struct {
		start, end time.Time
		results    []Result
		output     string
	}
	runs := make([]ran, len(tests))
	for i, test := range tests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := ran{start: time.Now()}
		r.results, r.output, err = s.runCase(ctx, h2spec, sh.Port(), test, job.Dir)
		r.end = time.Now()
		fmt.Fprintf(console, "== %s %s\n%s\n", test.ID(), test.Desc, r.output)
		if err != nil {
			return nil, err
		}
		runs[i] = r
	}
	captures := sh.Close()
	closed = true
	if err := writeCaptures(filepath.Join(job.Dir, "shim-connect-tls.json"), captures); err != nil {
		return nil, err
	}

	recordings := conformance.Recordings{}
	for i, test := range tests {
		var mine []shim.Capture
		for _, c := range captures {
			if !c.Accepted.Before(runs[i].start) && c.Accepted.Before(runs[i].end) {
				mine = append(mine, c)
			}
		}
		recording, err := record(test, runs[i].results, runs[i].output, mine)
		if err != nil {
			return recordings, err
		}
		data, err := json.Marshal(recording)
		if err != nil {
			return recordings, err
		}
		recordings[conformance.CaseID(test.Name(), conformance.ApproachConnectTLS)] = data
	}
	fmt.Fprintf(log, "connect-tls: recorded %d cases\n", len(recordings))
	return recordings, nil
}

// runCase runs h2spec for one case and returns the cases of its JUnit
// report, none when it wrote none, and its console output. h2spec exits
// non-zero when the case fails, so its exit status is not an error.
func (s Suite) runCase(ctx context.Context, h2spec, port string, test Test, dir string) ([]Result, string, error) {
	report := filepath.Join(dir, "h2spec-junit.xml")
	if err := os.Remove(report); err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, h2spec, "--tls", "--insecure", "--host", "127.0.0.1", "--port", port,
		"--timeout", strconv.Itoa(s.timeout()), "--junit-report", report, test.ID())
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, output.String(), fmt.Errorf("running h2spec for %s: %w", test.ID(), err)
	}
	if _, err := os.Stat(report); os.IsNotExist(err) {
		return nil, output.String(), nil
	}
	results, err := LoadJUnit(report)
	return results, output.String(), err
}

// dryRun lists h2spec's cases.
func dryRun(ctx context.Context, h2spec string) ([]Test, error) {
	out, err := exec.CommandContext(ctx, h2spec, "--dryrun").Output()
	if err != nil {
		return nil, fmt.Errorf("listing h2spec's cases: %w", err)
	}
	return ParseDryRun(out)
}

// Compare compares each case's verdict, error and end with the baseline,
// in h2spec's order.
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	return conformance.CompareCases(baseline, got, conformance.CaseComparison{Less: lessCase, Diff: compareCase})
}

func compareCase(caseID string, wantData, gotData json.RawMessage) ([]conformance.Difference, error) {
	var want, got Recording
	if err := json.Unmarshal(wantData, &want); err != nil {
		return nil, fmt.Errorf("decoding the baseline: %w", err)
	}
	if err := json.Unmarshal(gotData, &got); err != nil {
		return nil, err
	}
	var differences []conformance.Difference
	for _, field := range []struct{ name, want, got string }{
		{"verdict", string(want.Verdict), string(got.Verdict)},
		{"error", want.Error, got.Error},
		{"end", string(want.End), string(got.End)},
	} {
		if field.want != field.got {
			differences = append(differences, conformance.Difference{Case: caseID, Field: field.name, Want: field.want, Got: field.got})
		}
	}
	return differences, nil
}

// lessCase orders case IDs as h2spec runs them: by spec, then section and
// place, number by number.
func lessCase(a, b string) bool {
	ka, kb := caseKey(a), caseKey(b)
	for i := 0; i < len(ka) && i < len(kb); i++ {
		if ka[i] != kb[i] {
			return ka[i] < kb[i]
		}
	}
	if len(ka) != len(kb) {
		return len(ka) < len(kb)
	}
	return a < b
}

var specOrder = map[string]int{"generic": 0, "http2": 1, "hpack": 2}

// caseKey turns "http2-6.9.1-3@connect-tls" into [1 6 9 1 3].
func caseKey(caseID string) []int {
	name, _, _ := strings.Cut(caseID, "@")
	parts := strings.Split(name, "-")
	rank, ok := specOrder[parts[0]]
	if !ok {
		rank = len(specOrder)
	}
	key := []int{rank}
	for _, part := range parts[1:] {
		for _, number := range strings.Split(part, ".") {
			n, _ := strconv.Atoi(number)
			key = append(key, n)
		}
	}
	return key
}

// writeCaptures keeps the shim's captures, with timestamps, next to the
// run's report.
func writeCaptures(file string, captures []shim.Capture) error {
	data, err := json.MarshalIndent(captures, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o644)
}

func (s Suite) timeout() int {
	if s.Timeout == 0 {
		return 2
	}
	return s.Timeout
}
