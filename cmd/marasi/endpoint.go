package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tfkr-ae/marasi/service"
)

// Environment variables that select a remote endpoint and its control token.
// The token has no flag of its own, so it never shows in ps or shell history.
const (
	remoteVariable = "MARASI_REMOTE"
	tokenVariable  = "MARASI_TOKEN"
)

// instanceEndpoint is the service instance a command talks to. Every command
// reaches its instance through one, so a new way to reach an instance is
// added in resolveInstanceEndpoint and nowhere else.
type instanceEndpoint struct {
	label   string           // how messages name the instance, such as "instance work is not running"
	path    string           // instance path under the config dir, empty for a remote endpoint; the socket, lock and log names extend it
	control service.Endpoint // where the instance's control listener answers
}

// endpoint is the instance this process's command talks to, resolved by
// prepareInstancePath.
var endpoint instanceEndpoint

// resolveInstanceEndpoint resolves --config-dir and --instance into the
// instance a command talks to. It rejects an invalid name and a socket path
// over the portable limit.
func resolveInstanceEndpoint(configDir, name string) (instanceEndpoint, error) {
	path, err := resolveInstancePath(configDir, name)
	if err != nil {
		return instanceEndpoint{}, err
	}
	return localInstanceEndpoint(filepath.Base(path), path), nil
}

// resolveRemoteEndpoint resolves a --remote URL into the instance a command
// talks to. The control token comes from tokenFile when it is set, and from
// MARASI_TOKEN otherwise. No local path is checked, since the instance's
// socket is on its instance machine.
func resolveRemoteEndpoint(rawURL, tokenFile string) (instanceEndpoint, error) {
	token, err := remoteToken(tokenFile)
	if err != nil {
		return instanceEndpoint{}, err
	}
	control, err := service.RemoteEndpoint(rawURL, token)
	if err != nil {
		return instanceEndpoint{}, err
	}
	return instanceEndpoint{label: control.String(), control: control}, nil
}

// remoteToken reads the control token from tokenFile, or from MARASI_TOKEN
// when tokenFile is empty.
func remoteToken(tokenFile string) (string, error) {
	if tokenFile != "" {
		content, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("reading control token file: %w", err)
		}
		token := strings.TrimSpace(string(content))
		if token == "" {
			return "", fmt.Errorf("control token file %s is empty", tokenFile)
		}
		return token, nil
	}
	token := strings.TrimSpace(os.Getenv(tokenVariable))
	if token == "" {
		return "", errors.New("--remote needs the instance's control token: set " + tokenVariable + " or pass --token-file PATH")
	}
	return token, nil
}

// localInstanceEndpoint is the instance at path on this machine, named label
// in messages. path must already be a resolved instance path.
func localInstanceEndpoint(label, path string) instanceEndpoint {
	e := instanceEndpoint{label: label, path: path}
	e.control = service.SocketEndpoint(e.socketPath())
	return e
}

// remote reports whether the instance is reached at a network address.
func (e instanceEndpoint) remote() bool {
	return e.control.Remote()
}

// unreachable is the error for a request to the instance that got no
// response: a rejected control token, or no instance answering.
func (e instanceEndpoint) unreachable(err error) error {
	if errors.Is(err, service.ErrTokenRejected) {
		return fmt.Errorf("%w: check %s or --token-file", err, tokenVariable)
	}
	return instanceNotRunningError{name: e.label, remote: e.remote()}
}

// name is the instance's name as its path records it.
func (e instanceEndpoint) name() string {
	return filepath.Base(e.path)
}

// socketPath is the instance's control socket on this machine.
func (e instanceEndpoint) socketPath() string {
	return e.path + ".sock"
}

// lockPath is the instance's lock file on this machine.
func (e instanceEndpoint) lockPath() string {
	return e.path + ".lock"
}

// client creates a control API client for the instance.
func (e instanceEndpoint) client() *service.Client {
	return service.NewEndpointClient(e.control)
}
