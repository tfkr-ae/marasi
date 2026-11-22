package streamorigin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// PortPlaceholder replaces the origin's port in every recorded field value,
// so a case records the same fields whichever port its origin listened on.
const PortPlaceholder = "PORT"

// record runs one case on one approach, directly when job.Proxy is empty
// and through the proxy otherwise, and returns its recording. On
// connect-tls the origin serves a certificate from the harness CA; the
// client trusts that CA directly and only the proxy's CA through a proxy.
func (s Suite) record(ctx context.Context, c Case, approach conformance.Approach, job conformance.Job) (Recording, error) {
	var serverTLS, clientTLS *tls.Config
	if approach == conformance.ApproachConnectTLS {
		var err error
		if serverTLS, clientTLS, err = s.tlsConfigs(job, c.Protocol); err != nil {
			return Recording{}, err
		}
	}
	t := newTranscript()
	o, err := s.startOrigin(c, t, serverTLS)
	if err != nil {
		return Recording{}, fmt.Errorf("starting origin: %w", err)
	}
	defer o.stop()
	authority := net.JoinHostPort(s.originHost(), o.port())
	normalise := func(value string) string {
		return strings.ReplaceAll(value, authority, net.JoinHostPort(s.originHost(), PortPlaceholder))
	}

	address := net.JoinHostPort(s.listen(), o.port())
	if job.Proxy != "" {
		address = job.Proxy
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return Recording{}, fmt.Errorf("dialing %s: %w", address, err)
	}
	defer conn.Close()
	if job.Proxy != "" && approach != conformance.ApproachAbsolute {
		if err := connect(conn, authority, s.timeout()); err != nil {
			return Recording{}, err
		}
	}
	recording := Recording{}
	if clientTLS != nil {
		handshake, cancel := context.WithTimeout(ctx, s.timeout())
		defer cancel()
		secure := tls.Client(conn, clientTLS)
		if err := secure.HandshakeContext(handshake); err != nil {
			return Recording{}, fmt.Errorf("shaking hands over TLS as %s: %w", clientTLS.ServerName, err)
		}
		conn = secure
		recording.ClientALPN = secure.ConnectionState().NegotiatedProtocol
	}

	steps := renderClient(c, authority)
	var reading sync.WaitGroup
	var client sender
	switch {
	case c.Protocol == ProtocolH2 && recording.ClientALPN == "h2":
		h, err := newH2Client(t, conn)
		if err != nil {
			return Recording{}, fmt.Errorf("opening HTTP/2: %w", err)
		}
		reading.Go(func() { h.read(nil) })
		client = h
	case c.Protocol == ProtocolHTTP1:
		h := newH1Conn(SideClient, t, conn)
		if job.Proxy != "" && approach == conformance.ApproachAbsolute {
			h.absolute = authority
		}
		method := ""
		if len(steps) > 0 && len(steps[0].Fields) > 0 {
			method = steps[0].Fields[0].Value
		}
		reading.Go(func() { h.read(bufio.NewReader(conn), method, nil) })
		client = h
	}
	if client != nil {
		// A send that fails is the proxy's doing, and what arrived shows it.
		_ = runScript(t, SideClient, steps, client, map[string]int{}, s.timeout(), s.endTimeout())
	}
	// Wait for the origin's script when it started (for instance, a case where
	// the response ends before the client sends the rest of the request), so
	// taking the snapshot does not race with the origin receiving the request's end.
	o.waitScript()
	time.Sleep(s.settle())
	recording.OriginALPN = o.alpns()
	recording.Arrivals = t.snapshot(normalise)
	_ = conn.Close()
	reading.Wait()
	return recording, nil
}

// renderClient is the client's script with "{authority}" replaced and, on
// h2, :scheme and :authority added after :method.
func renderClient(c Case, authority string) []Step {
	steps := slices.Clone(c.Client)
	for i, step := range steps {
		if step.Kind != StepHead {
			continue
		}
		var fields []h2.Field
		for _, f := range step.Fields {
			fields = append(fields, h2.Field{Name: f.Name, Value: strings.ReplaceAll(f.Value, "{authority}", authority)})
			if c.Protocol == ProtocolH2 && f.Name == ":method" {
				fields = append(fields, h2.Fields(":scheme", "https", ":authority", authority)...)
			}
		}
		steps[i].Fields = fields
	}
	return steps
}

// origin is a running origin fixture for one case: it records what every
// connection it accepts receives, and runs the case's origin script once,
// on the first request whose head arrives.
type origin struct {
	listener net.Listener
	suite    Suite
	c        Case
	t        *transcript
	once     sync.Once

	mu         sync.Mutex
	conns      []net.Conn
	alpn       []string
	scriptDone chan struct{}
	wg         sync.WaitGroup
}

func (s Suite) startOrigin(c Case, t *transcript, config *tls.Config) (*origin, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(s.listen(), "0"))
	if err != nil {
		return nil, err
	}
	if config != nil {
		listener = tls.NewListener(listener, config)
	}
	o := &origin{listener: listener, suite: s, c: c, t: t}
	o.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			o.mu.Lock()
			o.conns = append(o.conns, conn)
			i := len(o.alpn)
			o.alpn = append(o.alpn, "")
			o.mu.Unlock()
			o.wg.Go(func() { o.serve(conn, i) })
		}
	})
	return o, nil
}

func (o *origin) port() string {
	_, port, _ := net.SplitHostPort(o.listener.Addr().String())
	return port
}

func (o *origin) serve(conn net.Conn, i int) {
	if secure, ok := conn.(*tls.Conn); ok {
		_ = conn.SetDeadline(time.Now().Add(o.suite.timeout()))
		if err := secure.Handshake(); err != nil {
			return
		}
		_ = conn.SetDeadline(time.Time{})
		o.mu.Lock()
		o.alpn[i] = secure.ConnectionState().NegotiatedProtocol
		o.mu.Unlock()
	}
	if o.c.Protocol == ProtocolH2 {
		h, ok := newH2Origin(o.t, conn)
		if !ok {
			return
		}
		h.read(func() { o.script(h) })
		return
	}
	h := newH1Conn(SideOrigin, o.t, conn)
	h.read(bufio.NewReader(conn), "", func() { o.script(h) })
}

// script runs the origin's script once, in its own goroutine, its request
// head already awaited. The origin waits EndTimeout for everything: the
// client gives up first, so the recording shows the client's timeout, and
// the origin still answers what the client sends after it.
func (o *origin) script(s sender) {
	o.once.Do(func() {
		done := make(chan struct{})
		o.mu.Lock()
		o.scriptDone = done
		o.mu.Unlock()
		o.wg.Go(func() {
			defer close(done)
			_ = runScript(o.t, SideOrigin, o.c.Origin, s, map[string]int{KindHead: 1}, o.suite.endTimeout(), o.suite.endTimeout())
		})
	})
}

func (o *origin) waitScript() {
	o.mu.Lock()
	done := o.scriptDone
	o.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (o *origin) alpns() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.alpn)
}

// stop closes the listener and every connection, and waits for the
// origin's goroutines.
func (o *origin) stop() {
	_ = o.listener.Close()
	o.mu.Lock()
	for _, conn := range o.conns {
		_ = conn.Close()
	}
	o.mu.Unlock()
	o.wg.Wait()
}

// connect sends CONNECT and reads the proxy's answer, which must be 200
// with nothing after it.
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
			return fmt.Errorf("reading the answer to CONNECT %q: %w", answer, err)
		}
	}
	line, _, _ := strings.Cut(string(answer), "\r\n")
	if parts := strings.Fields(line); len(parts) < 2 || parts[1] != "200" {
		return fmt.Errorf("the proxy refused CONNECT: %q", line)
	}
	return nil
}

// tlsConfigs returns the origin's server configuration, from the harness
// CA with ALPN protocol only, and the client's, which offers the same and
// trusts the harness CA in the direct run and only the proxy's CA through
// a proxy.
func (s Suite) tlsConfigs(job conformance.Job, protocol string) (server, client *tls.Config, err error) {
	if job.CA == nil {
		return nil, nil, errors.New("connect-tls needs the harness CA")
	}
	if server, err = job.CA.ServerTLS(s.originHost()); err != nil {
		return nil, nil, err
	}
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
	server.NextProtos = []string{protocol}
	client.NextProtos = []string{protocol}
	return server, client, nil
}
