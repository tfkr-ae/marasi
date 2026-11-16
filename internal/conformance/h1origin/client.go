package h1origin

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h1"
)

// PortPlaceholder replaces the origin's port in every recording, so a case
// records the same bytes whichever port its origin listened on.
const PortPlaceholder = "PORT"

// record runs one case on one client approach, directly when job.Proxy is
// empty, and returns its recording. On connect-tls the origin serves TLS
// with a certificate from the harness CA; the client trusts that CA
// directly and the proxy's CA through the proxy.
func (s Suite) record(ctx context.Context, c Case, approach conformance.Approach, job conformance.Job) (h1.Recording, error) {
	proxy := job.Proxy
	secure := approach == conformance.ApproachConnectTLS
	var serverTLS, clientTLS *tls.Config
	if secure {
		var err error
		if serverTLS, clientTLS, err = s.tlsConfigs(job); err != nil {
			return h1.Recording{}, err
		}
	}
	o, err := startOrigin(s.listen(), c.Script, s.timeout(), serverTLS)
	if err != nil {
		return h1.Recording{}, fmt.Errorf("starting origin: %w", err)
	}
	defer o.stop()
	authority := net.JoinHostPort(s.originHost(), o.port())
	normalise := func(raw h1.Raw) h1.Raw {
		return raw.Replace(authority, net.JoinHostPort(s.originHost(), PortPlaceholder))
	}

	address := net.JoinHostPort(s.listen(), o.port())
	if proxy != "" {
		address = proxy
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return h1.Recording{}, fmt.Errorf("dialing %s: %w", address, err)
	}
	defer conn.Close()

	cl := &clientConn{conn: conn, timeout: s.timeout()}
	var tunnel string
	if proxy != "" && approach != conformance.ApproachAbsolute {
		if tunnel, err = cl.connect(authority); err != nil {
			return h1.Recording{}, err
		}
	}
	tunnelled := tunnel == "" || strings.Contains(tunnel, " 200 ")
	if tunnelled && secure {
		if err := cl.handshake(ctx, clientTLS); err != nil {
			return h1.Recording{}, err
		}
	}
	if tunnelled {
		cl.exchange(c.Requests, func(request string) string {
			request = strings.ReplaceAll(request, "{authority}", authority)
			if proxy != "" && approach == conformance.ApproachAbsolute {
				request = absoluteForm(request, authority)
			}
			return request
		})
	}
	if cl.end == "" {
		cl.settle(s.settle())
	}
	// The origin's view is taken before the client closes, so a connection
	// the proxy keeps for reuse and one the client is about to close both
	// record what the origin saw while the case ran.
	recording := h1.Recording{Origin: o.snapshot(normalise)}
	client := h1.NewConnection(normalise(h1.Raw{ToServer: cl.sent, ToClient: cl.received}), cl.end)
	client.Tunnel = tunnel
	recording.Client = []h1.Connection{client}
	return recording, nil
}

// tlsConfigs returns the origin's server configuration, from the harness
// CA, and the client's, which trusts the harness CA in the direct run and
// the proxy's CA through a proxy.
func (s Suite) tlsConfigs(job conformance.Job) (server, client *tls.Config, err error) {
	if job.CA == nil {
		return nil, nil, errors.New("connect-tls needs the harness CA")
	}
	if server, err = job.CA.ServerTLS(s.originHost()); err != nil {
		return nil, nil, err
	}
	if job.Proxy == "" {
		return server, job.CA.ClientTLS(s.originHost()), nil
	}
	if len(job.ProxyCA) == 0 {
		return nil, nil, errors.New("connect-tls through a proxy needs the proxy's CA certificate, and the proxy gave none")
	}
	client, err = conformance.ClientTLSFromPEM(job.ProxyCA, s.originHost())
	return server, client, err
}

// absoluteForm rewrites the request target of request's first line from
// origin form to absolute form.
func absoluteForm(request, authority string) string {
	line, rest, _ := strings.Cut(request, "\n")
	parts := strings.SplitN(line, " ", 3)
	if len(parts) == 3 && strings.HasPrefix(parts[1], "/") {
		parts[1] = "http://" + authority + parts[1]
	}
	return strings.Join(parts, " ") + "\n" + rest
}

// clientConn is the harness client's side of one connection.
type clientConn struct {
	conn     net.Conn
	timeout  time.Duration
	sent     []byte
	received []byte
	end      h1.End // empty while open
}

// connect sends CONNECT and reads the proxy's answer, returning its start
// line. The answer is not part of the recording's bytes.
func (c *clientConn) connect(authority string) (string, error) {
	request := "CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\n\r\n"
	if _, err := io.WriteString(c.conn, request); err != nil {
		return "", fmt.Errorf("sending CONNECT: %w", err)
	}
	deadline := time.Now().Add(c.timeout)
	var answer []byte
	buf := make([]byte, 4<<10)
	for {
		if i := bytes.Index(answer, []byte("\r\n\r\n")); i >= 0 {
			// Anything after the answer belongs to the tunnel.
			c.received = append(c.received, answer[i+4:]...)
			line, _, _ := strings.Cut(string(answer), "\r\n")
			return line, nil
		}
		_ = c.conn.SetReadDeadline(deadline)
		n, err := c.conn.Read(buf)
		answer = append(answer, buf[:n]...)
		if err != nil {
			return "", fmt.Errorf("reading the answer to CONNECT %q: %w", answer, err)
		}
	}
}

// handshake starts TLS on the connection, straight to the origin or inside
// the proxy's tunnel. Only the bytes inside TLS are recorded.
func (c *clientConn) handshake(ctx context.Context, config *tls.Config) error {
	if len(c.received) > 0 {
		return fmt.Errorf("the proxy sent %q after its answer to CONNECT, before TLS", c.received)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	conn := tls.Client(c.conn, config)
	if err := conn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("shaking hands over TLS as %s: %w", config.ServerName, err)
	}
	c.conn = conn
	return nil
}

// exchange sends each request after the response to the one before.
func (c *clientConn) exchange(requests []string, render func(string) string) {
	var methods []string
	for i, request := range requests {
		request = render(request)
		method, _, _ := strings.Cut(request, " ")
		methods = append(methods, method)
		n, err := io.WriteString(c.conn, request)
		c.sent = append(c.sent, request[:n]...)
		if err != nil {
			c.end = readEnd(err)
			return
		}
		deadline := time.Now().Add(c.timeout)
		for {
			responses, _ := h1.ParseResponses(c.received, methods, false)
			if h1.FinalResponses(responses) > i {
				break
			}
			if err := c.read(deadline); err != nil {
				c.end = h1.EndTimeout
				if !errors.Is(err, os.ErrDeadlineExceeded) {
					c.end = readEnd(err)
				}
				return
			}
		}
	}
}

// settle waits a moment after the last response for anything more,
// including the peer closing the connection.
func (c *clientConn) settle(d time.Duration) {
	deadline := time.Now().Add(d)
	for {
		err := c.read(deadline)
		switch {
		case err == nil:
			continue
		case errors.Is(err, os.ErrDeadlineExceeded):
			c.end = h1.EndOpen
		default:
			c.end = readEnd(err)
		}
		return
	}
}

// read reads once before deadline and keeps what came.
func (c *clientConn) read(deadline time.Time) error {
	buf := make([]byte, 32<<10)
	_ = c.conn.SetReadDeadline(deadline)
	n, err := c.conn.Read(buf)
	c.received = append(c.received, buf[:n]...)
	return err
}
