package autobahn

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// Suite modes. In fuzzingclient Autobahn is the client and tests the upstream
// server; in fuzzingserver Autobahn is the server and tests the client.
const (
	ModeFuzzingClient = "fuzzingclient"
	ModeFuzzingServer = "fuzzingserver"
)

// Peers. strict is Autobahn's own testee; lenient is the harness echo peer.
const (
	PeerStrict  = "strict"
	PeerLenient = "lenient"
)

//go:embed wstest_proxy.py
var wstestProxy []byte

// Suite runs one Autobahn mode against one peer, directly or through a proxy.
type Suite struct {
	Image   string    // Autobahn testsuite Docker image
	Dir     string    // working directory for spec, wrapper, and reports
	Cases   []string  // Autobahn case patterns to run
	Exclude []string  // Autobahn case patterns to skip
	Log     io.Writer // Docker and harness progress output
}

var containerSeq atomic.Int64

// Run executes mode against peer and returns the report. proxy is the
// proxy listener host:port, or empty for a direct run.
func (s Suite) Run(ctx context.Context, mode, peer, proxy string) (Report, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "wstest_proxy.py"), wstestProxy, 0o644); err != nil {
		return nil, err
	}
	reports := filepath.Join(s.Dir, "reports")
	if err := os.RemoveAll(reports); err != nil {
		return nil, err
	}
	// A free port can be taken again before the server binds it, so a server
	// that never listens gets a fresh port and another attempt.
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		var port int
		if port, err = freePort(); err != nil {
			return nil, err
		}
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		switch mode {
		case ModeFuzzingClient:
			err = s.runFuzzingClient(ctx, peer, proxy, address)
		case ModeFuzzingServer:
			err = s.runFuzzingServer(ctx, peer, proxy, address)
		default:
			return nil, fmt.Errorf("unknown mode %q", mode)
		}
		var startErr serverStartError
		if !errors.As(err, &startErr) {
			break
		}
		fmt.Fprintf(s.Log, "attempt %d: %v\n", attempt, err)
	}
	if err != nil {
		return nil, err
	}
	return LoadReport(reports, peer)
}

// serverStartError is a server that did not listen on its port in time.
type serverStartError struct{ error }

func (s Suite) spec(extra map[string]any) error {
	spec := map[string]any{
		"outdir":              "/work/reports",
		"cases":               s.Cases,
		"exclude-cases":       nonNil(s.Exclude),
		"exclude-agent-cases": map[string]any{},
	}
	for k, v := range extra {
		spec[k] = v
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "spec.json"), data, 0o644)
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s Suite) runFuzzingClient(ctx context.Context, peer, proxy, upstream string) error {
	var stop func(logs bool)
	switch peer {
	case PeerStrict:
		var err error
		stop, err = s.startContainer(ctx, "wstest", "-m", "testeeserver", "-w", "ws://"+upstream)
		if err != nil {
			return err
		}
		defer func() { stop(false) }()
	case PeerLenient:
		listener, err := net.Listen("tcp", upstream)
		if err != nil {
			return err
		}
		defer listener.Close()
		go ServeLenient(listener)
	default:
		return fmt.Errorf("unknown peer %q", peer)
	}
	if err := waitForPort(ctx, upstream); err != nil {
		if stop != nil {
			stop(true)
			stop = func(bool) {}
		}
		return serverStartError{fmt.Errorf("waiting for %s server: %w", peer, err)}
	}
	if err := s.spec(map[string]any{
		"servers": []map[string]any{{"agent": peer, "url": "ws://" + upstream}},
	}); err != nil {
		return err
	}
	return s.runContainer(ctx, proxy, "pypy", "/work/wstest_proxy.py", "-m", "fuzzingclient", "-s", "/work/spec.json")
}

func (s Suite) runFuzzingServer(ctx context.Context, peer, proxy, upstream string) error {
	if err := s.spec(map[string]any{"url": "ws://" + upstream}); err != nil {
		return err
	}
	stop, err := s.startContainer(ctx, "wstest", "-m", "fuzzingserver", "-s", "/work/spec.json", "-u", "0")
	if err != nil {
		return err
	}
	if err := waitForPort(ctx, upstream); err != nil {
		stop(true)
		return serverStartError{fmt.Errorf("waiting for fuzzingserver: %w", err)}
	}
	defer stop(false)
	switch peer {
	case PeerStrict:
		err = s.runContainer(ctx, proxy, "pypy", "/work/wstest_proxy.py", "-m", "testeeclient", "-w", "ws://"+upstream, "-i", peer)
	case PeerLenient:
		err = RunLenientClient(ctx, upstream, proxy, peer)
	default:
		err = fmt.Errorf("unknown peer %q", peer)
	}
	if err != nil {
		return err
	}
	return waitForFile(ctx, filepath.Join(s.Dir, "reports", "index.json"))
}

func (s Suite) dockerArgs(name, proxy string) []string {
	// The image is amd64-only; naming the platform keeps arm64 hosts quiet.
	// Unbuffered output makes each "Running test case" line reach the log
	// when the case starts, not in batches.
	// Containers are removed by removeContainer, not --rm, so a container
	// that exits on its own can still be inspected.
	//
	// PyPy sizes its GC nursery from the "cache size" in /proc/cpuinfo,
	// which is the L3 on Intel hosts: 130 MB on some GitHub runners, where
	// wstest then grows to 6-10 GB and the kernel OOM-kills it. A fixed 4 MB
	// nursery, what PyPy picks on AMD hosts, keeps memory the same on any host.
	args := []string{"run", "--name", name, "--platform", "linux/amd64", "--network", "host", "-e", "PYTHONUNBUFFERED=1", "-e", "PYPY_GC_NURSERY=4MB", "-v", s.Dir + ":/work"}
	if proxy != "" {
		args = append(args, "-e", "WSTEST_PROXY="+proxy)
	}
	return append(args, s.Image)
}

func containerName() string {
	return fmt.Sprintf("marasi-autobahn-%d-%d", os.Getpid(), containerSeq.Add(1))
}

// runContainer runs the image in the foreground and removes it if ctx ends first.
func (s Suite) runContainer(ctx context.Context, proxy string, command ...string) error {
	name := containerName()
	cmd := exec.Command("docker", append(s.dockerArgs(name, proxy), command...)...)
	cmd.Stdout, cmd.Stderr = s.Log, s.Log
	if err := cmd.Start(); err != nil {
		return err
	}
	defer removeContainer(name)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			s.logState(name)
			return fmt.Errorf("docker %v: %w", command, err)
		}
		return nil
	case <-ctx.Done():
		removeContainer(name)
		<-done
		return ctx.Err()
	}
}

// startContainer runs the image in the background and returns its stop
// function, which first copies the container's output to the log when asked.
func (s Suite) startContainer(ctx context.Context, command ...string) (func(logs bool), error) {
	name := containerName()
	args := append(s.dockerArgs(name, ""), command...)
	args = append([]string{args[0], "-d"}, args[1:]...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = s.Log
	if err := cmd.Run(); err != nil {
		// Docker may have created the container before the start failed.
		removeContainer(name)
		return nil, fmt.Errorf("starting %v: %w", command, err)
	}
	return func(logs bool) {
		s.logState(name)
		if logs {
			cmd := exec.Command("docker", "logs", name)
			cmd.Stdout, cmd.Stderr = s.Log, s.Log
			_ = cmd.Run()
		}
		removeContainer(name)
	}, nil
}

// logState logs a container's exit state, including whether the kernel
// OOM-killed it, unless it is still running.
func (s Suite) logState(name string) {
	out, err := exec.Command("docker", "inspect", "--format",
		"{{.State.Status}} exit={{.State.ExitCode}} oomkilled={{.State.OOMKilled}} error={{printf \"%q\" .State.Error}} started={{.State.StartedAt}} finished={{.State.FinishedAt}}", name).Output()
	if err != nil || bytes.HasPrefix(out, []byte("running ")) {
		return
	}
	fmt.Fprintf(s.Log, "container %s: %s", name, out)
}

func removeContainer(name string) {
	_ = exec.Command("docker", "rm", "-f", name).Run()
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func waitForPort(ctx context.Context, address string) error {
	return poll(ctx, 60*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
}

func waitForFile(ctx context.Context, file string) error {
	return poll(ctx, 30*time.Second, func() bool {
		data, err := os.ReadFile(file)
		return err == nil && json.Valid(data)
	})
}

func poll(ctx context.Context, limit time.Duration, ready func() bool) error {
	deadline := time.Now().Add(limit)
	for !ready() {
		if time.Now().After(deadline) {
			return errors.New("timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}
