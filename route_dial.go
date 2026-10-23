package marasi

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"
)

// DefaultUpstreamProxyTimeout bounds reaching an upstream proxy and opening a
// tunnel through it, so a proxy that hangs fails the request.
const DefaultUpstreamProxyTimeout = 30 * time.Second

// UpstreamProxyFailure classifies why a route could not carry a connection.
type UpstreamProxyFailure string

const (
	// UpstreamProxyNotConfigured: the selected name is not in this
	// machine's list.
	UpstreamProxyNotConfigured UpstreamProxyFailure = "not_configured"
	// UpstreamProxyInvalid: the entry, or marasi_config.yaml, cannot be used.
	UpstreamProxyInvalid UpstreamProxyFailure = "invalid"
	// UpstreamProxyRefused: the upstream proxy refused the connection.
	UpstreamProxyRefused UpstreamProxyFailure = "refused"
	// UpstreamProxyAuthRejected: the upstream proxy rejected the credentials,
	// or asked for some when none are set.
	UpstreamProxyAuthRejected UpstreamProxyFailure = "auth_rejected"
	// UpstreamProxyTLSVerification: a certificate did not verify.
	UpstreamProxyTLSVerification UpstreamProxyFailure = "tls_verification"
	// UpstreamProxyTimeout: the upstream proxy did not answer in time.
	UpstreamProxyTimeout UpstreamProxyFailure = "timeout"
	// UpstreamProxyRejected: the upstream proxy would not open the tunnel
	// for another reason, such as an unreachable target.
	UpstreamProxyRejected UpstreamProxyFailure = "rejected"
	// UpstreamProxyFailed: any other failure.
	UpstreamProxyFailed UpstreamProxyFailure = "failed"
)

// UpstreamProxyError is a route's failure to carry a connection. Its text
// names the upstream proxy by name and redacted address and never holds
// credentials, because martian copies it into the client's Warning header.
type UpstreamProxyError struct {
	// Name is the selected upstream proxy's name.
	Name string
	// Address is scheme://host:port, or "" when the entry has none.
	Address string
	// Failure classifies the cause.
	Failure UpstreamProxyFailure
	// Detail says what went wrong in words. It holds no credentials.
	Detail string
	// Err is the underlying error, if any. It is not part of Error's text.
	Err error
}

func (e *UpstreamProxyError) Error() string {
	if e.Address == "" {
		return fmt.Sprintf("upstream proxy %q: %s", e.Name, e.Detail)
	}
	return fmt.Sprintf("upstream proxy %q (%s): %s", e.Name, e.Address, e.Detail)
}

func (e *UpstreamProxyError) Unwrap() error { return e.Err }

func (e *UpstreamProxyError) sameAs(other *UpstreamProxyError) bool {
	if e == nil || other == nil {
		return e == other
	}
	return e.Name == other.Name && e.Address == other.Address && e.Failure == other.Failure && e.Detail == other.Detail
}

// dialFunc opens a network connection, as net.Dialer.DialContext does.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// dial opens the connection the transport speaks HTTP, or runs the utls
// handshake, over. address is the origin after any waypoint rewrite. Every
// scheme's dial is chosen here. A route that cannot carry the connection
// returns an *UpstreamProxyError and never falls back to a direct
// connection (docs/adr/2026-10-23-an-upstream-proxy-fails-closed.md).
func (route *Route) dial(ctx context.Context, dial dialFunc, network, address string) (net.Conn, error) {
	if route.failure != nil {
		return nil, route.failure
	}
	switch route.Scheme {
	case "":
		return dial(ctx, network, address)
	case UpstreamProxyHTTP:
		return route.dialHTTPConnect(ctx, dial, network, address)
	default:
		return nil, route.fail(UpstreamProxyInvalid, fmt.Sprintf("the %s scheme is not supported yet", route.Scheme), nil)
	}
}

// fail builds the route's error for failure.
func (route *Route) fail(failure UpstreamProxyFailure, detail string, err error) *UpstreamProxyError {
	return &UpstreamProxyError{Name: route.Name, Address: route.Address(), Failure: failure, Detail: detail, Err: err}
}

// dialHTTPConnect opens TCP to the upstream proxy and asks it for a tunnel
// to address with CONNECT. Plain-HTTP origins are tunnelled too, so the
// origin sees exactly the bytes the transport writes.
func (route *Route) dialHTTPConnect(ctx context.Context, dial dialFunc, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, route.timeoutOrDefault())
	defer cancel()
	proxyAddress := net.JoinHostPort(route.Host, strconv.Itoa(route.Port))
	conn, err := dial(ctx, network, proxyAddress)
	if err != nil {
		return nil, route.classify("connecting", err)
	}
	tunnel, err := route.connect(ctx, conn, address)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return tunnel, nil
}

// connect asks the upstream proxy on conn for a tunnel to address. It
// gives up when ctx ends.
func (route *Route) connect(ctx context.Context, conn net.Conn, address string) (net.Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// A cancelled request or a forced transport close interrupts the
	// exchange too, not only the deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	request := "CONNECT " + address + " HTTP/1.1\r\nHost: " + address + "\r\n"
	if route.credentials != nil {
		password, _ := route.credentials.Password()
		token := base64.StdEncoding.EncodeToString([]byte(route.credentials.Username() + ":" + password))
		request += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	request += "\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		return nil, route.classify("sending CONNECT", contextCause(ctx, err))
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, route.classify("reading the CONNECT response", contextCause(ctx, err))
	}
	response.Body.Close()
	switch {
	case response.StatusCode == http.StatusProxyAuthRequired:
		return nil, route.fail(UpstreamProxyAuthRejected, "rejected the credentials with "+response.Status, nil)
	case response.StatusCode < 200 || response.StatusCode > 299:
		return nil, route.fail(UpstreamProxyRejected, "refused to open a tunnel to "+address+" with "+response.Status, nil)
	}
	if !stop() {
		return nil, route.classify("opening the tunnel", contextCause(ctx, ctx.Err()))
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, route.classify("opening the tunnel", err)
	}
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

// contextCause prefers ctx's error when ctx has ended, since an I/O error
// after the deadline fired only says the deadline passed.
func contextCause(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// classify wraps err from a step of reaching the upstream proxy.
func (route *Route) classify(step string, err error) *UpstreamProxyError {
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return route.fail(UpstreamProxyTimeout, step+": timed out", err)
	case errors.Is(err, syscall.ECONNREFUSED):
		return route.fail(UpstreamProxyRefused, step+": connection refused", err)
	case errors.Is(err, context.Canceled):
		return route.fail(UpstreamProxyFailed, step+": cancelled", err)
	}
	// Dial and read errors name addresses, never credentials.
	return route.fail(UpstreamProxyFailed, step+": "+err.Error(), err)
}

func (route *Route) timeoutOrDefault() time.Duration {
	if route.timeout > 0 {
		return route.timeout
	}
	return DefaultUpstreamProxyTimeout
}

// bufferedConn returns bytes an upstream proxy sent after its CONNECT
// response before reading from the connection again.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (conn *bufferedConn) Read(p []byte) (int, error) {
	return conn.reader.Read(p)
}
