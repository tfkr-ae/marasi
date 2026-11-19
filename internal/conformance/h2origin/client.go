package h2origin

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// PortPlaceholder replaces the origin's port in every recorded field value,
// so a case records the same fields whichever port its origin listened on.
const PortPlaceholder = "PORT"

// alpn is what the client offers: h2, then http/1.1, as a browser does, so
// a proxy that speaks only HTTP/1.1 can still answer and the recording
// shows what it chose.
var alpn = []string{"h2", "http/1.1"}

// record runs one case, directly when job.Proxy is empty and otherwise
// with CONNECT through the proxy, and returns its recording. The origin
// serves TLS with ALPN h2 and a certificate from the harness CA; the client
// trusts that CA directly and only the proxy's CA through the proxy, so a
// recorded case also shows that the proxy intercepted its TLS.
func (s Suite) record(ctx context.Context, c Case, job conformance.Job) (h2.Recording, error) {
	serverTLS, clientTLS, err := s.tlsConfigs(job)
	if err != nil {
		return h2.Recording{}, err
	}
	o, err := startOrigin(s.listen(), c.Script, s.timeout(), serverTLS)
	if err != nil {
		return h2.Recording{}, fmt.Errorf("starting origin: %w", err)
	}
	defer o.stop()
	authority := net.JoinHostPort(s.originHost(), o.port())
	placeholder := net.JoinHostPort(s.originHost(), PortPlaceholder)
	normalise := func(value string) string { return strings.ReplaceAll(value, authority, placeholder) }

	address := net.JoinHostPort(s.listen(), o.port())
	requests := c.Requests
	var redirect *http1Origin
	if c.Redirect != "" {
		if redirect, err = startHTTP1Origin(s.listen(), c.Redirect, s.timeout(), serverTLS); err != nil {
			return h2.Recording{}, fmt.Errorf("starting the HTTP/1.1 origin: %w", err)
		}
		defer redirect.stop()
		address = net.JoinHostPort(s.listen(), redirect.port())
		requests = redirected(requests, net.JoinHostPort(s.originHost(), redirect.port()))
	}
	if job.Proxy != "" {
		address = job.Proxy
	}
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return h2.Recording{}, fmt.Errorf("dialing %s: %w", address, err)
	}
	defer raw.Close()
	if job.Proxy != "" {
		if err := connect(raw, authority, s.timeout()); err != nil {
			return h2.Recording{}, err
		}
	}
	handshake, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	conn := tls.Client(raw, clientTLS)
	if err := conn.HandshakeContext(handshake); err != nil {
		return h2.Recording{}, fmt.Errorf("shaking hands over TLS as %s: %w", clientTLS.ServerName, err)
	}
	protocol := conn.ConnectionState().NegotiatedProtocol
	recorder := h2.NewRecorder(protocol)
	client := recorder.Connection(normalise)
	switch {
	case protocol == "h2":
		if err := s.exchange(conn, recorder, requests, authority); err != nil {
			return h2.Recording{}, err
		}
		client = recorder.Connection(normalise)
	case redirect != nil && job.Proxy == "":
		streams, err := s.exchangeHTTP1(conn, c.Requests, authority)
		if err != nil {
			return h2.Recording{}, err
		}
		client.Streams = normaliseStreams(streams, normalise)
	}
	// The origin's view is taken before the client closes, as in h1-origin.
	// A redirected case records only the origin its requests went to.
	origins := o.snapshot(normalise)
	if redirect != nil {
		origins = redirect.snapshot(normalise)
	}
	return h2.Recording{Client: []h2.Connection{client}, Origin: origins}, nil
}

// redirected is requests, each with RedirectField naming target last.
func redirected(requests []Request, target string) []Request {
	out := make([]Request, len(requests))
	for i, r := range requests {
		r.Fields = append(slices.Clone(r.Fields), h2.Field{Name: RedirectField, Value: target})
		out[i] = r
	}
	return out
}

// exchange sends every request on its own stream, then records what comes
// back until every stream ended or the peer went away, and a moment more.
// A peer that stops answering is recorded as it is when the time runs out.
// Request bodies keep to the peer's flow-control windows, reading what
// comes back while they wait, and every DATA received is acknowledged with
// WINDOW_UPDATE.
func (s Suite) exchange(conn net.Conn, recorder *h2.Recorder, requests []Request, authority string) error {
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		return fmt.Errorf("sending the connection preface: %w", err)
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(http2.Setting{ID: http2.SettingEnablePush, Val: 0}); err != nil {
		return fmt.Errorf("sending SETTINGS: %w", err)
	}
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	encode := func(fields []h2.Field) []byte {
		block.Reset()
		for _, f := range fields {
			_ = encoder.WriteField(hpack.HeaderField{Name: f.Name, Value: f.Value})
		}
		return bytes.Clone(block.Bytes())
	}
	windows := newWindows()
	// handle answers, accounts and records one frame received.
	handle := func(frame http2.Frame) error {
		if err := answer(framer, frame); err != nil {
			return err
		}
		windows.received(frame)
		if err := replenish(framer, frame); err != nil {
			return err
		}
		return recorder.Frame(frame)
	}
	deadline := time.Now().Add(s.timeout())
	readOne := func() error {
		_ = conn.SetReadDeadline(deadline)
		frame, err := framer.ReadFrame()
		if err != nil {
			return err
		}
		return handle(frame)
	}
	for i, r := range requests {
		id := uint32(2*i + 1)
		recorder.Open(id)
		fields := append(h2.Fields(":method", r.Method, ":scheme", "https", ":authority", authority, ":path", r.Path), r.Fields...)
		hasBody, hasTrailers := r.Body != "", len(r.Trailers) > 0
		if err := writeBlock(framer, id, encode(fields), !hasBody && !hasTrailers, r.Fragment); err != nil {
			return fmt.Errorf("sending request %d: %w", i, err)
		}
		for body := []byte(r.Body); len(body) > 0; {
			n := windows.allowed(id, len(body))
			if n == 0 {
				if err := readOne(); err != nil {
					return fmt.Errorf("waiting for window to send request %d body: %w", i, err)
				}
				continue
			}
			windows.spend(id, n)
			if err := framer.WriteData(id, n == len(body) && !hasTrailers, body[:n]); err != nil {
				return fmt.Errorf("sending request %d body: %w", i, err)
			}
			body = body[n:]
		}
		if hasTrailers {
			if err := framer.WriteHeaders(http2.HeadersFrameParam{
				StreamID: id, BlockFragment: encode(r.Trailers), EndStream: true, EndHeaders: true,
			}); err != nil {
				return fmt.Errorf("sending request %d trailers: %w", i, err)
			}
		}
	}

	ended := func() bool {
		gone, last := recorder.GoneAway()
		for i := range len(requests) {
			if !recorder.Closed(i) && !(gone && recorder.StreamID(i) > last) {
				return false
			}
		}
		return true
	}
	readUntil := func(until time.Time, stop func() bool) {
		deadline = until
		for !stop() {
			if readOne() != nil {
				return
			}
		}
	}
	readUntil(time.Now().Add(s.timeout()), ended)
	readUntil(time.Now().Add(s.settle()), func() bool { return false })
	return nil
}

// answer acknowledges SETTINGS and PING.
func answer(framer *http2.Framer, frame http2.Frame) error {
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if !f.IsAck() {
			return framer.WriteSettingsAck()
		}
	case *http2.PingFrame:
		if !f.IsAck() {
			return framer.WritePing(true, f.Data)
		}
	}
	return nil
}

// replenish gives back the flow-control window a DATA frame took, on the
// connection and, while it is open, on its stream.
func replenish(framer *http2.Framer, frame http2.Frame) error {
	f, ok := frame.(*http2.DataFrame)
	if !ok || f.Length == 0 {
		return nil
	}
	if err := framer.WriteWindowUpdate(0, f.Length); err != nil {
		return err
	}
	if f.StreamEnded() {
		return nil
	}
	return framer.WriteWindowUpdate(f.StreamID, f.Length)
}

// connect sends CONNECT and reads the proxy's answer, which must be 200
// with nothing after it before TLS.
func connect(conn net.Conn, authority string, timeout time.Duration) error {
	request := "CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return fmt.Errorf("sending CONNECT: %w", err)
	}
	defer conn.SetReadDeadline(time.Time{})
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var answer []byte
	buf := make([]byte, 1)
	// One byte at a time, so nothing of the tunnel is read here.
	for !bytes.HasSuffix(answer, []byte("\r\n\r\n")) {
		n, err := conn.Read(buf)
		answer = append(answer, buf[:n]...)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				err = errors.New("timed out")
			}
			return fmt.Errorf("reading the answer to CONNECT %q: %w", answer, err)
		}
	}
	line, _, _ := strings.Cut(string(answer), "\r\n")
	if !strings.Contains(line, " 200 ") && !strings.HasSuffix(line, " 200") {
		return fmt.Errorf("the proxy refused CONNECT: %q", line)
	}
	return nil
}

// tlsConfigs returns the origin's server configuration, from the harness
// CA with ALPN h2, and the client's, which trusts the harness CA in the
// direct run and only the proxy's CA through a proxy.
func (s Suite) tlsConfigs(job conformance.Job) (server, client *tls.Config, err error) {
	if job.CA == nil {
		return nil, nil, errors.New("connect-tls needs the harness CA")
	}
	if server, err = job.CA.ServerTLS(s.originHost()); err != nil {
		return nil, nil, err
	}
	server.NextProtos = []string{"h2"}
	if job.Proxy == "" {
		client = job.CA.ClientTLS(s.originHost())
	} else {
		if len(job.ProxyCA) == 0 {
			return nil, nil, errors.New("connect-tls through a proxy needs the proxy's CA certificate, and the proxy gave none")
		}
		if client, err = conformance.ClientTLSFromPEM(job.ProxyCA, s.originHost()); err != nil {
			return nil, nil, err
		}
	}
	client.NextProtos = alpn
	return server, client, nil
}
