package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ContainerImage is the Linux image Marasi runs in for the suites that need
// a container, pinned by digest. Alpine carries the system roots
// (/etc/ssl/certs/ca-certificates.crt) that a harness CA is added to.
const ContainerImage = "alpine@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f"

// Paths inside the container.
const (
	containerBinary    = "/usr/local/bin/marasi"
	containerConfigDir = "/root/mcf"
	// containerRoots is the image's system roots; containerBundle is them
	// plus the harness CA, which SSL_CERT_FILE names.
	containerRoots  = "/etc/ssl/certs/ca-certificates.crt"
	containerBundle = "/etc/ssl/conformance-roots.pem"
)

// ContainerSettings says how to start Marasi in a Linux container.
type ContainerSettings struct {
	Binary string // the marasi binary built by make linux-<Arch>
	Arch   string // its architecture, such as arm64; the container's platform is linux/<Arch>
	Image  string // base image; empty means ContainerImage
	// CA is the harness CA. When set, SSL_CERT_FILE in the container
	// names the image's system roots plus it, so Marasi verifies the
	// targets' certificates with no Marasi setting changed.
	CA *CA
}

// StartContainer starts a container on the host's network, copies the
// binary in and launches a service instance there. Every name in hosts
// resolves to 127.0.0.1 inside it, or to the address after it for a
// "name:address" entry, so Marasi reaches targets that listen on
// the host's loopback. The proxy listener is on loopback too, where the
// harness reaches it: Docker Desktop shares the host network with the Mac
// when host networking is on, as Autobahn needs too. With settings.CA set,
// Go's TLS in the container trusts it as well as the system roots.
// SSL_CERT_FILE works only on Linux, which is why Marasi runs there.
func StartContainer(ctx context.Context, settings ContainerSettings, name, project string, hosts []string) (*Instance, error) {
	image := settings.Image
	if image == "" {
		image = ContainerImage
	}
	removeContainer(name)
	args := []string{"run", "--detach", "--name", name, "--platform", "linux/" + settings.Arch, "--network", "host"}
	for _, host := range hosts {
		if !strings.Contains(host, ":") {
			host += ":127.0.0.1"
		}
		args = append(args, "--add-host", host)
	}
	if settings.CA != nil {
		args = append(args, "--env", "SSL_CERT_FILE="+containerBundle)
	}
	args = append(args, image, "sleep", "infinity")
	if err := docker(ctx, args...); err != nil {
		return nil, err
	}
	if settings.CA != nil {
		// The bundle exists before Marasi starts, and Go reads the roots
		// on first use.
		script := "cat " + containerRoots + " - > " + containerBundle
		if err := dockerInput(ctx, settings.CA.PEM(), "exec", "-i", name, "sh", "-c", script); err != nil {
			removeContainer(name)
			return nil, fmt.Errorf("writing the roots bundle: %w", err)
		}
	}
	if err := docker(ctx, "cp", settings.Binary, name+":"+containerBinary); err != nil {
		removeContainer(name)
		return nil, err
	}
	instance := &Instance{Binary: containerBinary, ConfigDir: containerConfigDir, Name: "cf", Container: name}
	// The control listener's network address on loopback stands in for the
	// unix socket, which Docker Desktop does not share with the host. It
	// has no password: only processes on this machine reach loopback.
	out, err := instance.cli(ctx, nil, "service", "start", "--project-name", project, "--address", "127.0.0.1", "--port", "0",
		"--control-address", "127.0.0.1:0", "--no-password", "--json")
	if err != nil {
		removeContainer(name)
		return nil, err
	}
	var started struct {
		ProxyListener   string `json:"proxy_listener"`
		ControlListener string `json:"control_listener"`
	}
	if err := json.Unmarshal(out, &started); err != nil || started.ProxyListener == "" || started.ControlListener == "" {
		instance.Stop()
		return nil, fmt.Errorf("reading service start output %q: %w", out, err)
	}
	instance.Listener, instance.Control = started.ProxyListener, started.ControlListener
	for _, address := range []string{instance.Listener, instance.Control} {
		if err := AwaitListener(ctx, address); err != nil {
			instance.Stop()
			return nil, err
		}
	}
	return instance, nil
}

// AwaitListener waits until address takes connections from the host.
// Docker Desktop forwards a port a host-network container listens on only
// a moment after it starts listening.
func AwaitListener(ctx context.Context, address string) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			return conn.Close()
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("listener %s in the container never took a connection from the host: %w", address, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ContainerLauncher starts Marasi in a Linux container for each proxied
// conformance run of a suite whose Setup asks for one.
func ContainerLauncher(settings ContainerSettings, log io.Writer) Launcher {
	return func(ctx context.Context, suite Suite, run RunName, dir string) (Proxy, error) {
		if _, err := os.Stat(settings.Binary); err != nil {
			return nil, fmt.Errorf("marasi for Linux (make linux-%s): %w", settings.Arch, err)
		}
		project := "conformance-" + suite.Name() + "-" + string(run)
		name := fmt.Sprintf("marasi-conformance-%d-%s-%s", os.Getpid(), suite.Name(), run)
		instance, err := StartContainer(ctx, settings, name, project, suite.Setup().Hosts)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(log, "marasi in container %s, proxy listener %s\n", name, instance.Listener)
		return prepare(ctx, instance, suite, run, dir, log)
	}
}

// Launchers picks the container launcher for a suite whose Setup asks for
// a container and the host launcher for any other.
func Launchers(host, container Launcher) Launcher {
	return func(ctx context.Context, suite Suite, run RunName, dir string) (Proxy, error) {
		if suite.Setup().Container {
			return container(ctx, suite, run, dir)
		}
		return host(ctx, suite, run, dir)
	}
}

func docker(ctx context.Context, args ...string) error {
	return dockerInput(ctx, nil, args...)
}

// dockerInput runs docker with stdin as its input.
func dockerInput(ctx context.Context, stdin []byte, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func removeContainer(name string) {
	_ = exec.Command("docker", "rm", "--force", name).Run()
}
