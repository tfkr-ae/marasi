package h1origin_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// standIn is an in-process forward proxy for the harness's own tests. It
// takes absolute-form requests and CONNECT, dials every origin on loopback,
// and relays bytes. With broken set it canonicalises the field names of the
// first request and response on each connection and swallows the first
// trailer of a chunked response. Inside CONNECT it intercepts TLS as Marasi
// does: it answers with a certificate from its own CA and dials the origin
// over TLS, trusting the harness CA.
type standIn struct {
	listener    net.Listener
	broken      bool
	harnessCA   *conformance.CA // the origins' CA
	ownCA       *conformance.CA // signs what the client sees
	connections atomic.Int64
	silent      bool // Newest reports no traffic, as a misrouted run would
	wg          sync.WaitGroup
}

func startStandIn(t *testing.T, broken bool, harnessCA *conformance.CA) *standIn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ownCA, err := conformance.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	s := &standIn{listener: listener, broken: broken, harnessCA: harnessCA, ownCA: ownCA}
	s.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.connections.Add(1)
			s.wg.Go(func() { s.serve(conn) })
		}
	})
	t.Cleanup(s.Stop)
	return s
}

func (s *standIn) Address() string { return s.listener.Addr().String() }

func (s *standIn) Newest(context.Context, conformance.TrafficKind) (string, error) {
	if s.silent || s.connections.Load() == 0 {
		return "", nil
	}
	return strconv.FormatInt(s.connections.Load(), 10), nil
}

// Certificate is the stand-in's CA certificate, as Marasi's comes from
// certificate get.
func (s *standIn) Certificate(context.Context) ([]byte, error) { return s.ownCA.PEM(), nil }

func (s *standIn) Stop() {
	_ = s.listener.Close()
	s.wg.Wait()
}

func (s *standIn) serve(client net.Conn) {
	defer func() { _ = client.Close() }()
	reader := bufio.NewReader(client)
	head, err := readHead(reader)
	if err != nil {
		return
	}
	method, rest, _ := strings.Cut(head[0], " ")
	target, version, _ := strings.Cut(rest, " ")
	var authority string
	secure := false
	if method == "CONNECT" {
		authority = target
		if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
		if first, err := reader.Peek(1); err == nil && first[0] == 0x16 { // a TLS handshake record
			host, _, _ := net.SplitHostPort(authority)
			config, err := s.ownCA.ServerTLS(host)
			if err != nil {
				return
			}
			client = tls.Server(&bufferedConn{Conn: client, r: reader}, config)
			reader = bufio.NewReader(client)
			secure = true
		}
		if head, err = readHead(reader); err != nil {
			return
		}
	} else {
		u, err := url.Parse(target)
		if err != nil {
			return
		}
		authority = u.Host
		head[0] = method + " " + u.RequestURI() + " " + version
	}
	host, port, _ := net.SplitHostPort(authority)
	var origin net.Conn
	if secure {
		origin, err = tls.Dial("tcp", net.JoinHostPort("127.0.0.1", port), s.harnessCA.ClientTLS(host))
	} else {
		origin, err = net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
	}
	if err != nil {
		return
	}
	defer origin.Close()

	if s.broken {
		canonicalise(head)
	}
	if _, err := io.WriteString(origin, strings.Join(head, "\r\n")+"\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(origin, reader)
		if c, ok := origin.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		close(done)
	}()
	s.relayResponse(client, bufio.NewReader(origin))
	<-done
}

// relayResponse copies the origin's bytes to the client, reworking the
// first response when the stand-in is broken.
func (s *standIn) relayResponse(client net.Conn, origin *bufio.Reader) {
	defer func() {
		if c, ok := client.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}()
	if s.broken {
		head, err := readHead(origin)
		if err != nil {
			return
		}
		canonicalise(head)
		if _, err := io.WriteString(client, strings.Join(head, "\r\n")+"\r\n\r\n"); err != nil {
			return
		}
		if strings.Contains(strings.ToLower(strings.Join(head, "\n")), "transfer-encoding: chunked") {
			if err := swallowTrailer(client, origin); err != nil {
				return
			}
		}
	}
	_, _ = io.Copy(client, origin)
}

// swallowTrailer copies a chunked body and drops its first trailer line.
func swallowTrailer(w io.Writer, r *bufio.Reader) error {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(line), 16, 64)
		if err != nil {
			return err
		}
		if size == 0 {
			break
		}
		if _, err := io.CopyN(w, r, size+2); err != nil {
			return err
		}
	}
	dropped := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			_, err := io.WriteString(w, line)
			return err
		}
		if !dropped {
			dropped = true
			continue
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
}

// bufferedConn reads through r, which may hold bytes already read from Conn.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func readHead(r *bufio.Reader) ([]string, error) {
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return lines, nil
		}
		lines = append(lines, line)
	}
}

func canonicalise(head []string) {
	for i := 1; i < len(head); i++ {
		name, value, _ := strings.Cut(head[i], ":")
		head[i] = textproto.CanonicalMIMEHeaderKey(name) + ":" + value
	}
}
