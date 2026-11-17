//go:build windows

package service

import (
	"net"
	"os"
)

// listenerFile always fails: Windows cannot pass a socket through
// exec.Cmd.ExtraFiles, so a restart there binds the address again.
func listenerFile(net.Listener) (*os.File, error) {
	return nil, errSocketHandoverUnsupported
}
