// Package grpcclient is the conformance harness's gRPC client-leg suite.
// grpc-go's interop client, a third-party tester, calls grpc-java's
// interop server through the shim: straight to the server in the direct
// run, and with CONNECT through Marasi in the proxied runs. A Java server
// keeps quirks that grpc-go and Marasi share through x/net/http2 from
// cancelling out. grpc-go's client has none of the compressed cases, so
// grpc-java's interop client runs those.
//
// The client speaks h2 with prior knowledge to the shim, and the shim is
// the TLS client toward the server, with ALPN h2: it trusts the harness CA
// in the direct run and only Marasi's CA through Marasi, so every case of
// a proxied run shows Marasi intercepted its TLS. The shim therefore
// captures the client's leg in plaintext, and a case's recording is the
// client's verdict plus every stream as the client received it (see
// Recording).
//
// The server listens on every address, so Marasi's container reaches it
// at TargetHost, which it resolves to loopback, on the same port the
// direct run dials.
//
// The suite is a required check on all four conformance runs.
package grpcclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/nginx"
	"github.com/tfkr-ae/marasi/internal/conformance/shim"
)

// Name is the suite's name and its baselines directory.
const Name = "grpc-client"

// Target is the suite's one target and baseline file.
const Target = "grpc-java"

// TargetHost is the interop server's name in CONNECT and its certificate.
const TargetHost = "grpc.test"

// Suite is the grpc-client conformance suite.
type Suite struct {
	// Limit is how long a case's client may run before it is stopped and
	// the case recorded as TimedOut; zero means 30 seconds.
	Limit time.Duration
	// Settle is how long the shim watches the server after the client
	// leaves; zero means the shim's default.
	Settle time.Duration
}

var _ conformance.Suite = Suite{}

// Name is "grpc-client".
func (s Suite) Name() string { return Name }

// Runs is all four proxied runs.
func (s Suite) Runs() []conformance.RunName { return conformance.ProxiedRuns }

// Approaches is TLS inside CONNECT, with ALPN h2.
func (s Suite) Approaches() []conformance.Approach {
	return []conformance.Approach{conformance.ApproachConnectTLS}
}

// Targets is grpc-java's interop server.
func (s Suite) Targets() []string { return []string{Target} }

// Deterministic asks the direct run to record every case twice and leave
// out any case whose recording changes.
func (s Suite) Deterministic() {}

// Setup runs Marasi in a container that resolves TargetHost to loopback.
// The went-through check is the intercepted TLS. The Checkpoint and
// extension runs hold and edit HTTP as an HTTP/1.1 suite does.
func (s Suite) Setup() conformance.Setup {
	setup := conformance.HTTPSetup()
	setup.Checkpoint = append(setup.Checkpoint, conformance.CheckpointStream)
	setup.Traffic = conformance.TrafficTLS
	setup.Container = true
	setup.Hosts = []string{TargetHost}
	return setup
}

// Execute runs each selected case's interop client once through one shim,
// against one interop server.
func (s Suite) Execute(ctx context.Context, job conformance.Job) (conformance.Recordings, error) {
	if len(job.Approaches) > 0 && !slices.Contains(job.Approaches, conformance.ApproachConnectTLS) {
		return conformance.Recordings{}, nil
	}
	var cases []Case
	for _, c := range Cases {
		if conformance.Selected(conformance.CaseID(c.Name, conformance.ApproachConnectTLS), job.Cases, job.Exclude, job.Approaches) {
			cases = append(cases, c)
		}
	}
	if len(cases) == 0 {
		return conformance.Recordings{}, nil
	}
	log := job.Log
	if log == nil {
		log = io.Discard
	}
	if err := os.MkdirAll(job.Dir, 0o755); err != nil {
		return nil, err
	}
	goClient, err := installGoClient(ctx, job.Dir, log)
	if err != nil {
		return nil, err
	}
	if err := buildImage(ctx, log); err != nil {
		return nil, err
	}
	config, err := shimTLS(job)
	if err != nil {
		return nil, err
	}

	port, err := nginx.FreePort()
	if err != nil {
		return nil, err
	}
	run := job.Run
	if run == "" {
		run = conformance.RunDirect
	}
	prefix := fmt.Sprintf("marasi-conformance-%d-%s-%s", os.Getpid(), Name, run)
	srv, err := startServer(ctx, prefix+"-server", port, job.CA)
	if err != nil {
		return nil, err
	}
	defer srv.stop(filepath.Join(job.Dir, "server.log"))
	if err := awaitTLS(ctx, net.JoinHostPort("127.0.0.1", port), job.CA); err != nil {
		return nil, err
	}

	sh, err := shim.Start(shim.Options{Target: net.JoinHostPort(TargetHost, port),
		Dial: net.JoinHostPort("127.0.0.1", port), Proxy: job.Proxy, TLS: config, Settle: s.Settle})
	if err != nil {
		return nil, err
	}
	closed := false
	defer func() {
		if !closed {
			sh.Close()
		}
	}()

	console, err := os.Create(filepath.Join(job.Dir, "client-connect-tls.log"))
	if err != nil {
		return nil, err
	}
	defer console.Close()
	fmt.Fprintf(log, "connect-tls: %d interop cases through the shim at %s\n", len(cases), sh.Address())
	type ran struct {
		start, end time.Time
		outcome    Outcome
	}
	runs := make([]ran, len(cases))
	for i, c := range cases {
		r := ran{start: time.Now()}
		var output string
		r.outcome, output, err = s.runCase(ctx, c, goClient, sh.Port(), prefix+"-client")
		r.end = time.Now()
		fmt.Fprintf(console, "== %s (%s): %s %s\n%s\n", c.Name, c.Client, r.outcome.Verdict, r.outcome.Error, output)
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
	for i, c := range cases {
		var mine []shim.Capture
		for _, capture := range captures {
			if !capture.Accepted.Before(runs[i].start) && capture.Accepted.Before(runs[i].end) {
				mine = append(mine, capture)
			}
		}
		recording, err := record(c.Name, runs[i].outcome, mine)
		if err != nil {
			return recordings, err
		}
		data, err := json.Marshal(recording)
		if err != nil {
			return recordings, err
		}
		recordings[conformance.CaseID(c.Name, conformance.ApproachConnectTLS)] = data
	}
	fmt.Fprintf(log, "connect-tls: recorded %d cases\n", len(recordings))
	return recordings, nil
}

// runCase runs one case's interop client against the shim's port in
// plaintext. A client still running at the limit is stopped and the case
// is TimedOut.
func (s Suite) runCase(ctx context.Context, c Case, goClient, port, container string) (Outcome, string, error) {
	limited, cancel := context.WithTimeout(ctx, s.limit())
	defer cancel()
	var cmd *exec.Cmd
	switch c.Client {
	case GoClient:
		cmd = exec.CommandContext(limited, goClient, "-server_host=127.0.0.1", "-server_port="+port, "-use_tls=false", "-test_case="+c.Name)
	case JavaClient:
		_ = exec.Command("docker", "rm", "--force", container).Run()
		defer func() { _ = exec.Command("docker", "rm", "--force", container).Run() }()
		// Docker Desktop carries a host-network container's IPv4
		// connections to the host's loopback, where the shim listens, but
		// not the JVM's dual-stack ones.
		cmd = exec.CommandContext(limited, "docker", "run", "--rm", "--name", container, "--network", "host", InteropImage,
			"java", "-Djava.net.preferIPv4Stack=true", "-cp", "/interop/lib/*", javaMain+"TestServiceClient",
			"--server_host=127.0.0.1", "--server_port="+port, "--use_tls=false", "--test_case="+c.Name)
	default:
		return Outcome{}, "", fmt.Errorf("%s: unknown client %q", c.Name, c.Client)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return Outcome{}, string(output), ctx.Err()
	}
	if limited.Err() != nil {
		return Outcome{Verdict: TimedOut, Error: fmt.Sprintf("no verdict in %s", s.limit())}, string(output), nil
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return outcomeOf(0, string(output)), string(output), nil
	case errors.As(err, &exit):
		return outcomeOf(exit.ExitCode(), string(output)), string(output), nil
	}
	return Outcome{}, string(output), fmt.Errorf("running the %s interop client for %s: %w", c.Client, c.Name, err)
}

// shimTLS is the shim's TLS client configuration, offering only h2: it
// trusts the harness CA in the direct run and only Marasi's CA through
// Marasi.
func shimTLS(job conformance.Job) (*tls.Config, error) {
	var config *tls.Config
	switch {
	case job.Proxy == "" && job.CA == nil:
		return nil, errors.New("the direct run needs the harness CA")
	case job.Proxy == "":
		config = job.CA.ClientTLS(TargetHost)
	case len(job.ProxyCA) == 0:
		return nil, fmt.Errorf("%w: the proxy gave no CA certificate to check the TLS inside CONNECT against", conformance.ErrNotProxied)
	default:
		var err error
		if config, err = conformance.ClientTLSFromPEM(job.ProxyCA, TargetHost); err != nil {
			return nil, err
		}
	}
	config.NextProtos = []string{"h2"}
	return config, nil
}

// awaitTLS waits until the interop server at address completes a TLS
// handshake with ALPN h2, which it does only once it serves: Docker's port
// forwarding can take connections before then.
func awaitTLS(ctx context.Context, address string, ca *conformance.CA) error {
	config := ca.ClientTLS(TargetHost)
	config.NextProtos = []string{"h2"}
	deadline := time.Now().Add(60 * time.Second)
	for {
		dialer := tls.Dialer{Config: config, NetDialer: &net.Dialer{Timeout: 2 * time.Second}}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			protocol := conn.(*tls.Conn).ConnectionState().NegotiatedProtocol
			conn.Close()
			if protocol != "h2" {
				return fmt.Errorf("the interop server negotiated %q, not h2", protocol)
			}
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("waiting for the interop server's TLS at %s: %w", address, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
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

func (s Suite) limit() time.Duration {
	if s.Limit == 0 {
		return 30 * time.Second
	}
	return s.Limit
}
