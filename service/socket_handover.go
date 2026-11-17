package service

import (
	"errors"
	"net"
	"os"
)

// errSocketHandoverUnsupported means this platform cannot hand a listening
// socket to another process, so a restart closes the socket and the
// successor binds its address again.
var errSocketHandoverUnsupported = errors.New("handing a listening socket to another process is not supported on this platform")

// fileListener serves from file, a socket that listenerFile duplicated. It
// leaves file open for its owner to close.
func fileListener(file *os.File) (net.Listener, error) {
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, wrapError("serving from handed-over socket", err)
	}
	return listener, nil
}
