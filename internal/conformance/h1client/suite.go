// Package h1client is the conformance harness's HTTP/1.1 client-leg suite.
// Http11Probe, a third-party tester, probes nginx through the shim:
// straight to nginx in the direct run, and with CONNECT through Marasi in
// the proxied runs. A case's recording is Http11Probe's verdict and the
// shim's parsed capture of the case's connection.
//
// Martian routes a request inside CONNECT by its Host field rather than by
// the CONNECT target. Http11Probe's requests name the shim's port in Host,
// so nginx listens on that same port at 127.0.0.2, where Marasi in its
// container resolves nginx's name, while the shim has it at 127.0.0.1. The
// direct run reaches nginx on a second port at 127.0.0.1.
package h1client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h1"
	"github.com/tfkr-ae/marasi/internal/conformance/nginx"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

// Name is the suite's name and its baselines directory.
const Name = "h1-client"

// Target is the suite's one target and baseline file.
const Target = "nginx"

// TargetHost is nginx's name in Http11Probe's requests and in CONNECT.
const TargetHost = "nginx.test"

// marasiAddress is where Marasi's container resolves TargetHost: a
// loopback address the shim does not listen on.
const marasiAddress = "127.0.0.2"

// elsewhere are hosts some tests name in Host. Martian routes by Host, so
// Marasi's container resolves them to a loopback address nothing listens
// on, and no test reaches the internet.
var elsewhere = []string{"example.com", "other.example.com", "evil.example.com"}

// Suite is the h1-client conformance suite.
type Suite struct {
	// Timeout is Http11Probe's connect and read timeout per test, in
	// seconds; zero means 5, Http11Probe's default.
	Timeout int
	// Settle is how long the shim watches nginx after a test leaves;
	// zero means the shim's default.
	Settle time.Duration
}

var _ conformance.Suite = Suite{}

// Name is "h1-client".
func (s Suite) Name() string { return Name }

// Runs is every proxied run.
func (s Suite) Runs() []conformance.RunName { return conformance.ProxiedRuns }

// Approaches are plain HTTP inside CONNECT and TLS inside CONNECT, which
// the shim adds.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachConnectPlain, conformance.ApproachConnectTLS}
}

// Targets is nginx.
func (s Suite) Targets() []string { return []string{Target} }

// Setup is the HTTP setup of every proxied run, with Marasi in a container
// that resolves nginx's name to marasiAddress.
func (s Suite) Setup() conformance.Setup {
	setup := conformance.HTTPSetup()
	setup.Container = true
	setup.Hosts = []string{TargetHost + ":" + marasiAddress}
	for _, host := range elsewhere {
		setup.Hosts = append(setup.Hosts, host+":127.0.0.3")
	}
	return setup
}

// Execute runs Http11Probe once per selected approach, through one shim
// each, against one nginx that serves both.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	var approaches []conformance.Approach
	for _, a := range s.Approaches() {
		if len(job.Approaches) == 0 || slices.Contains(job.Approaches, a) {
			approaches = append(approaches, a)
		}
	}
	if len(approaches) == 0 {
		return conformance.Recordings{}, nil
	}
	log := job.Log
	if log == nil {
		log = io.Discard
	}
	if err := os.MkdirAll(job.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := buildProbe(ctx, log); err != nil {
		return nil, err
	}

	// Each approach: a shim at 127.0.0.1:S, nginx at 127.0.0.2:S for
	// Marasi and at 127.0.0.1:D for the direct run.
	type leg struct {
		approach conformance.Approach
		shim     *shim.Shim
	}
	var legs []leg
	defer func() {
		for _, l := range legs {
			l.shim.Close()
		}
	}()
	var listeners []nginx.Listener
	for _, approach := range approaches {
		secure := approach == conformance.ApproachConnectTLS
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
		options := shim.Options{Listener: listener, Target: net.JoinHostPort(TargetHost, port),
			Dial: net.JoinHostPort("127.0.0.1", direct), Proxy: job.Proxy, Settle: s.Settle}
		if secure {
			if options.TLS, err = clientTLS(job); err != nil {
				listener.Close()
				return nil, err
			}
		}
		sh, err := shim.Start(options)
		if err != nil {
			listener.Close()
			return nil, err
		}
		legs = append(legs, leg{approach, sh})
		listeners = append(listeners,
			nginx.Listener{Address: net.JoinHostPort(marasiAddress, port), TLS: secure},
			nginx.Listener{Address: net.JoinHostPort("127.0.0.1", direct), TLS: secure})
	}
	run := job.Run
	if run == "" {
		run = conformance.RunDirect
	}
	prefix := fmt.Sprintf("marasi-conformance-%d-%s-%s", os.Getpid(), Name, run)
	server, err := nginx.Start(ctx, nginx.Config{Name: prefix + "-nginx", Listeners: listeners, CA: job.CA, Names: []string{TargetHost}})
	if err != nil {
		return nil, err
	}
	defer server.Stop(filepath.Join(job.Dir, "nginx.log"))

	tests := exactTests(job.Cases)
	recordingsOut := conformance.Recordings{}
	for _, l := range legs {
		p := probe{name: prefix + "-http11probe", host: TargetHost, port: l.shim.Port(), tests: tests,
			timeout: s.timeout(), dir: job.Dir, prefix: "http11probe-" + string(l.approach)}
		fmt.Fprintf(log, "%s: Http11Probe through the shim at %s\n", l.approach, l.shim.Address())
		report, err := p.run(ctx)
		captures := l.shim.Close()
		if werr := writeCaptures(filepath.Join(job.Dir, "shim-"+string(l.approach)+".json"), captures); werr != nil && err == nil {
			err = werr
		}
		if err != nil {
			return recordingsOut, err
		}
		got, err := recordings(report, captures, l.approach, TargetHost, l.shim.Port(), func(id string) bool {
			return conformance.Selected(id, job.Cases, job.Exclude, job.Approaches)
		})
		if err != nil {
			return recordingsOut, fmt.Errorf("%s: %w", l.approach, err)
		}
		for id, data := range got {
			recordingsOut[id] = data
		}
		fmt.Fprintf(log, "%s: recorded %d cases\n", l.approach, len(got))
	}
	return recordingsOut, nil
}

// Compare compares the recordings with the baseline, ordered by case ID,
// and checks the leg rules on the responses Marasi sent the client.
func (s Suite) Compare(_ string, baseline, got conformance.Recordings) (conformance.Comparison, error) {
	return conformance.CompareCases(baseline, got, nil, func(a, b string) bool { return a < b }, h1.Compare, h1.Check)
}

// clientTLS is the shim's TLS client configuration: it trusts the harness
// CA in the direct run and Marasi's CA through Marasi.
func clientTLS(job conformance.Job) (*tls.Config, error) {
	if job.Proxy == "" {
		if job.CA == nil {
			return nil, errors.New("connect-tls needs the harness CA")
		}
		return job.CA.ClientTLS(TargetHost), nil
	}
	if len(job.ProxyCA) == 0 {
		return nil, errors.New("connect-tls through a proxy needs the proxy's CA certificate, and the proxy gave none")
	}
	return conformance.ClientTLSFromPEM(job.ProxyCA, TargetHost)
}

// exactTests returns the test IDs the case patterns name exactly, or nil
// when any pattern is a glob, which means every test runs and the
// recordings are narrowed afterwards.
func exactTests(patterns []string) []string {
	var tests []string
	for _, pattern := range patterns {
		id, _, _ := strings.Cut(pattern, "@")
		if strings.ContainsAny(id, `*?[\`) {
			return nil
		}
		if !slices.Contains(tests, id) {
			tests = append(tests, id)
		}
	}
	return tests
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
		return 5
	}
	return s.Timeout
}
