package streamorigin_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// mode is how the stand-in proxy carries bytes.
type mode int

const (
	// splice forwards bytes as they come.
	splice mode = iota
	// late holds the bytes of each direction until the sender has been
	// quiet for lateQuiet, as a proxy that reads a body whole would, so
	// a message the client waits for arrives after the client gave up.
	late
)

// lateQuiet is longer than the suite's Timeout in these tests.
const lateQuiet = 600 * time.Millisecond

// standIn is an in-process forward proxy that takes CONNECT and absolute
// form and splices to the origin on loopback. It does not intercept TLS:
// it gives the harness CA as its own, so the client trusts the origin.
type standIn struct {
	listener net.Listener
	mode     mode
	ca       *conformance.CA
	newest   atomic.Int64

	mu    sync.Mutex
	conns []net.Conn
	wg    sync.WaitGroup
}

func startStandIn(t *testing.T, m mode, ca *conformance.CA) *standIn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &standIn{listener: listener, mode: m, ca: ca}
	s.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.track(conn)
			s.wg.Go(func() { s.serve(conn) })
		}
	})
	t.Cleanup(s.Stop)
	return s
}

func (s *standIn) Address() string { return s.listener.Addr().String() }

// Newest is the number of connections the stand-in carried.
func (s *standIn) Newest(context.Context, conformance.TrafficKind) (string, error) {
	if n := s.newest.Load(); n > 0 {
		return strconv.FormatInt(n, 10), nil
	}
	return "", nil
}

// Certificate is the harness CA, so a client inside CONNECT trusts the
// origin the stand-in splices it to.
func (s *standIn) Certificate(context.Context) ([]byte, error) { return s.ca.PEM(), nil }

func (s *standIn) Stop() {
	_ = s.listener.Close()
	s.mu.Lock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *standIn) track(conn net.Conn) {
	s.mu.Lock()
	s.conns = append(s.conns, conn)
	s.mu.Unlock()
}

func (s *standIn) serve(client net.Conn) {
	defer func() { _ = client.Close() }()
	reader := bufio.NewReader(client)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	var head strings.Builder
	for {
		field, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		head.WriteString(field)
		if strings.TrimSpace(field) == "" {
			break
		}
	}
	parts := strings.Fields(line)
	if len(parts) != 3 {
		return
	}
	var authority, first string
	if parts[0] == "CONNECT" {
		authority = parts[1]
		if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
	} else {
		rest, ok := strings.CutPrefix(parts[1], "http://")
		if !ok {
			return
		}
		var path string
		authority, path, _ = strings.Cut(rest, "/")
		first = parts[0] + " /" + path + " " + parts[2] + "\r\n" + head.String()
	}
	_, port, _ := net.SplitHostPort(authority)
	origin, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return
	}
	s.track(origin)
	s.newest.Add(1)
	defer func() { _ = origin.Close() }()
	if _, err := io.WriteString(origin, first); err != nil {
		return
	}
	var pumps sync.WaitGroup
	pumps.Go(func() { s.pump(origin, reader); _ = origin.Close() })
	pumps.Go(func() { s.pump(client, origin); _ = client.Close() })
	pumps.Wait()
}

// pump copies from src to dst, at once or, in late mode, once src has
// been quiet for lateQuiet.
func (s *standIn) pump(dst io.Writer, src io.Reader) {
	if s.mode == splice {
		_, _ = io.Copy(dst, src)
		return
	}
	var mu sync.Mutex
	var held []byte
	var timer *time.Timer
	flush := func() {
		mu.Lock()
		defer mu.Unlock()
		_, _ = dst.Write(held)
		held = nil
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		mu.Lock()
		held = append(held, buf[:n]...)
		mu.Unlock()
		if err != nil {
			if timer != nil {
				timer.Stop()
			}
			flush()
			return
		}
		if timer == nil {
			timer = time.AfterFunc(lateQuiet, flush)
		} else {
			timer.Reset(lateQuiet)
		}
	}
}
