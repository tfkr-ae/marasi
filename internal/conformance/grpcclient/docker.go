package grpcclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// InteropVersion is the io.grpc:grpc-interop-testing version (Apache-2.0)
// the image holds: grpc-java's interop server, and its interop client for
// the compressed cases.
const InteropVersion = "1.85.1"

// InteropImage is the tag of the image built from Dockerfile at
// InteropVersion. It names the Dockerfile's content too, so an image Docker
// already has is rebuilt when the Dockerfile changes.
var InteropImage = "marasi-conformance-grpc-java:" + InteropVersion + "-" + dockerfileSum()

// dockerfileSum is the first 12 hex digits of the Dockerfile's SHA-256.
func dockerfileSum() string {
	sum := sha256.Sum256(dockerfile)
	return hex.EncodeToString(sum[:])[:12]
}

// GoInterop is grpc-go's interop client (Apache-2.0), which the harness
// installs with go install so it never enters go.mod.
const GoInterop = "google.golang.org/grpc/interop/client@v1.85.0"

//go:embed Dockerfile
var dockerfile []byte

// buildImage builds InteropImage unless Docker has it already.
func buildImage(ctx context.Context, log io.Writer) error {
	if exec.CommandContext(ctx, "docker", "image", "inspect", InteropImage).Run() == nil {
		return nil
	}
	fmt.Fprintf(log, "building %s\n", InteropImage)
	cmd := exec.CommandContext(ctx, "docker", "build", "--build-arg", "VERSION="+InteropVersion, "--tag", InteropImage, "-")
	cmd.Stdin = bytes.NewReader(dockerfile)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", InteropImage, err)
	}
	return nil
}

// installGoClient builds GoInterop into dir and returns the binary's path.
// Go's build cache makes it quick after the first time; running the binary
// once per case is cheaper than a go run per case, which links every time.
func installGoClient(ctx context.Context, dir string, log io.Writer) (string, error) {
	bin, err := filepath.Abs(filepath.Join(dir, "bin"))
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "go", "install", GoInterop)
	cmd.Env = append(os.Environ(), "GOBIN="+bin, "GOFLAGS=")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("installing %s: %w", GoInterop, err)
	}
	return filepath.Join(bin, "client"), nil
}

// javaMain is the package of grpc-java's interop server and client.
const javaMain = "io.grpc.testing.integration."

// server is grpc-java's interop server in a container on the host's
// network.
type server struct{ name string }

// startServer starts grpc-java's interop server on port of every address,
// serving TLS with a certificate the harness CA issued for TargetHost. The
// server loads its certificate as the classpath resource certs/server1.pem,
// so the harness's goes in a directory ahead of the jars.
func startServer(ctx context.Context, name, port string, ca *conformance.CA) (*server, error) {
	if ca == nil {
		return nil, fmt.Errorf("the interop server needs the harness CA")
	}
	dir, err := os.MkdirTemp("", "conformance-grpc-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cert, key, err := ca.Issue(TargetHost)
	if err != nil {
		return nil, err
	}
	certs := filepath.Join(dir, "certs")
	if err := os.Mkdir(certs, 0o755); err != nil {
		return nil, err
	}
	for file, data := range map[string][]byte{"server1.pem": cert, "server1.key": key} {
		if err := os.WriteFile(filepath.Join(certs, file), data, 0o644); err != nil {
			return nil, err
		}
	}
	_ = exec.Command("docker", "rm", "--force", name).Run()
	if err := docker(ctx, "create", "--name", name, "--network", "host", InteropImage,
		"java", "-cp", "/override:/interop/lib/*", javaMain+"TestServiceServer", "--port="+port, "--use_tls=true"); err != nil {
		return nil, err
	}
	s := &server{name: name}
	if err := docker(ctx, "cp", dir, name+":/override"); err != nil {
		s.stop("")
		return nil, err
	}
	if err := docker(ctx, "start", name); err != nil {
		s.stop("")
		return nil, err
	}
	if err := conformance.AwaitListener(ctx, "127.0.0.1:"+port); err != nil {
		logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
		s.stop("")
		return nil, fmt.Errorf("starting the interop server: %w\n%s", err, logs)
	}
	return s, nil
}

// stop writes the server's log to logFile, unless it is empty, and
// removes the container.
func (s *server) stop(logFile string) {
	if logFile != "" {
		if logs, err := exec.Command("docker", "logs", s.name).CombinedOutput(); err == nil {
			_ = os.WriteFile(logFile, logs, 0o644)
		}
	}
	_ = exec.Command("docker", "rm", "--force", s.name).Run()
}

func docker(ctx context.Context, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
