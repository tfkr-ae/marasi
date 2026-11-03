package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tfkr-ae/marasi/service"
)

// Environment variables that select a remote endpoint and its control
// password. MARASI_PASSWORD also sets the password service start guards the
// network address with.
const (
	remoteVariable   = "MARASI_REMOTE"
	passwordVariable = "MARASI_PASSWORD"
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
// talks to, presenting password when it is not empty. No local path is
// checked, since the instance's socket is on its instance machine.
func resolveRemoteEndpoint(rawURL, password string) (instanceEndpoint, error) {
	control, err := service.RemoteEndpoint(rawURL, password)
	if err != nil {
		return instanceEndpoint{}, err
	}
	return instanceEndpoint{label: control.String(), control: control}, nil
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
// response: a missing or rejected control password, or no instance
// answering.
func (e instanceEndpoint) unreachable(err error) error {
	if errors.Is(err, service.ErrPasswordRejected) {
		if !e.control.HasPassword() {
			return fmt.Errorf("%w: the instance needs one: pass --password or set %s", err, passwordVariable)
		}
		return fmt.Errorf("%w: check --password or %s", err, passwordVariable)
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
