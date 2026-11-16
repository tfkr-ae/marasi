package h1origin_test

import (
	"bufio"
	"context"
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
// trailer of a chunked response.
type standIn struct {
	listener    net.Listener
	broken      bool
	connections atomic.Int64
	silent      bool // Newest reports no traffic, as a misrouted run would
	wg          sync.WaitGroup
}

func startStandIn(t *testing.T, broken bool) *standIn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &standIn{listener: listener, broken: broken}
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

func (s *standIn) Stop() {
	_ = s.listener.Close()
	s.wg.Wait()
}

func (s *standIn) serve(client net.Conn) {
	defer client.Close()
	reader := bufio.NewReader(client)
	head, err := readHead(reader)
	if err != nil {
		return
	}
	method, rest, _ := strings.Cut(head[0], " ")
	target, version, _ := strings.Cut(rest, " ")
	var authority string
	if method == "CONNECT" {
		authority = target
		if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
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
	_, port, _ := net.SplitHostPort(authority)
	origin, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
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
		if tcp, ok := origin.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
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
		if tcp, ok := client.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
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
