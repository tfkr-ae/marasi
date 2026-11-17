//go:build !windows

package service

import (
	"fmt"
	"net"
	"os"
)

// listenerFile duplicates the socket of listener, a TCP listener, so the
// socket stays bound after listener closes
// (docs/adr/2026-10-18-restart-hands-over-and-rolls-back.md).
func listenerFile(listener net.Listener) (*os.File, error) {
	tcp, ok := listener.(*net.TCPListener)
	if !ok {
		return nil, errSocketHandoverUnsupported
	}
	file, err := tcp.File()
	if err != nil {
		return nil, fmt.Errorf("duplicating listening socket: %w", err)
	}
	return file, nil
}
