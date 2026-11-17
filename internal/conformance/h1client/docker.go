package h1client

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ProbeCommit is the Http11Probe commit the harness builds
// (https://github.com/MDA2AV/Http11Probe, MIT): "Update the Roadrunner
// entry to 0.10.4", 2026-10-24.
const ProbeCommit = "d50d818a0e008e9feb3bb9860ad5d9cc40342bc3"

// ProbeImage is the tag of the image built from Dockerfile at ProbeCommit.
const ProbeImage = "marasi-conformance-http11probe:" + ProbeCommit

//go:embed Dockerfile
var dockerfile []byte

// buildProbe builds ProbeImage unless Docker has it already. The first
// build fetches the source and NuGet packages and takes a few minutes.
func buildProbe(ctx context.Context, log io.Writer) error {
	if exec.CommandContext(ctx, "docker", "image", "inspect", ProbeImage).Run() == nil {
		return nil
	}
	fmt.Fprintf(log, "building %s\n", ProbeImage)
	cmd := exec.CommandContext(ctx, "docker", "build", "--build-arg", "COMMIT="+ProbeCommit, "--tag", ProbeImage, "-")
	cmd.Stdin = bytes.NewReader(dockerfile)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", ProbeImage, err)
	}
	return nil
}

// probe is one Http11Probe run against host:port.
type probe struct {
	name    string   // container name
	host    string   // the name Http11Probe dials, which resolves to 127.0.0.1
	port    string   // the shim's port
	tests   []string // test IDs to run; empty means every test
	timeout int      // Http11Probe's connect and read timeout per test, in seconds
	dir     string   // where results.json and the console output go
	prefix  string   // file name prefix in dir
}

// run runs Http11Probe in a container on the host's network and loads its
// report.
func (p probe) run(ctx context.Context) (Report, error) {
	const output = "/tmp/results.json"
	_ = exec.Command("docker", "rm", "--force", p.name).Run()
	defer func() { _ = exec.Command("docker", "rm", "--force", p.name).Run() }()
	args := []string{"run", "--name", p.name, "--network", "host", "--add-host", p.host + ":127.0.0.1", ProbeImage,
		"--host", p.host, "--port", p.port, "--timeout", strconv.Itoa(p.timeout), "--output", output}
	for _, test := range p.tests {
		args = append(args, "--test", test)
	}
	console, err := os.Create(filepath.Join(p.dir, p.prefix+".log"))
	if err != nil {
		return Report{}, err
	}
	defer console.Close()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = console, console
	if err := cmd.Run(); err != nil {
		return Report{}, fmt.Errorf("running Http11Probe: docker %s: %w (output in %s)", strings.Join(args, " "), err, console.Name())
	}
	file := filepath.Join(p.dir, p.prefix+".json")
	if out, err := exec.CommandContext(ctx, "docker", "cp", p.name+":"+output, file).CombinedOutput(); err != nil {
		return Report{}, fmt.Errorf("copying Http11Probe's report: %w: %s", err, out)
	}
	return LoadReport(file)
}
