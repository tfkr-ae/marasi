package main

import (
	"path/filepath"

	"github.com/tfkr-ae/marasi/service"
)

// instanceEndpoint is the service instance a command talks to. Every command
// reaches its instance through one, so a new way to reach an instance is
// added in resolveInstanceEndpoint and nowhere else.
type instanceEndpoint struct {
	label   string           // how messages name the instance, such as "instance work is not running"
	path    string           // instance path under the config dir; the socket, lock and log names extend it
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
	return localInstanceEndpoint(name, path), nil
}

// localInstanceEndpoint is the instance at path on this machine, named label
// in messages. path must already be a resolved instance path.
func localInstanceEndpoint(label, path string) instanceEndpoint {
	e := instanceEndpoint{label: label, path: path}
	e.control = service.SocketEndpoint(e.socketPath())
	return e
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
