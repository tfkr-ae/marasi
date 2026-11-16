package h1origin_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// standIn is an in-process forward proxy for the harness's own tests. It
// takes absolute-form requests and CONNECT, dials every origin on loopback,
// and relays bytes, removing Connection and the fields it names from the
// first request and response on each connection. With broken set it instead
// canonicalises the field names of that request and response and forwards
// their Connection fields, and by request path:
//   - /chunked: swallows the first trailer of the chunked response
//   - any other path: adds Via to the request and response
//   - /reframe: re-frames the chunked response to a matching Content-Length
//   - /cl-te: sends Content-Length with the chunked response
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

	path := strings.Fields(head[0] + " -")[1]
	if s.broken {
		canonicalise(head)
		if path != "/chunked" {
			head = append(head, "Via: 1.1 standin")
		}
	} else {
		head = stripConnection(head)
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
	s.relayResponse(client, bufio.NewReader(origin), path)
	<-done
}

// relayResponse copies the origin's bytes to the client, reworking the
// head of the first response, and its body when the stand-in is broken.
func (s *standIn) relayResponse(client net.Conn, origin *bufio.Reader, path string) {
	defer func() {
		if tcp, ok := client.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	head, err := readHead(origin)
	if err != nil {
		return
	}
	writeHead := func(head []string) bool {
		_, err := io.WriteString(client, strings.Join(head, "\r\n")+"\r\n\r\n")
		return err == nil
	}
	if !s.broken {
		if writeHead(stripConnection(head)) {
			_, _ = io.Copy(client, origin)
		}
		return
	}
	canonicalise(head)
	if path != "/chunked" {
		head = append(head, "Via: 1.1 standin")
	}
	chunked := strings.Contains(strings.ToLower(strings.Join(head, "\n")), "transfer-encoding: chunked")
	switch {
	case chunked && path == "/chunked":
		if !writeHead(head) || swallowTrailer(client, origin) != nil {
			return
		}
	case chunked && (path == "/reframe" || path == "/cl-te"):
		raw, body, err := readChunked(origin)
		if err != nil {
			return
		}
		if path == "/reframe" {
			head = slices.DeleteFunc(head, func(line string) bool {
				return strings.HasPrefix(strings.ToLower(line), "transfer-encoding:")
			})
			raw = body
		}
		head = append(head, "Content-Length: "+strconv.Itoa(len(body)))
		if !writeHead(head) {
			return
		}
		if _, err := client.Write(raw); err != nil {
			return
		}
	default:
		if !writeHead(head) {
			return
		}
	}
	_, _ = io.Copy(client, origin)
}

// readChunked reads a chunked body with its trailer section and returns its
// bytes as sent and de-framed.
func readChunked(r *bufio.Reader) (raw, body []byte, err error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		raw = append(raw, line...)
		size, err := strconv.ParseInt(strings.TrimSpace(line), 16, 64)
		if err != nil {
			return nil, nil, err
		}
		if size == 0 {
			break
		}
		chunk := make([]byte, size+2)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return nil, nil, err
		}
		raw = append(raw, chunk...)
		body = append(body, chunk[:size]...)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		raw = append(raw, line...)
		if strings.TrimSpace(line) == "" {
			return raw, body, nil
		}
	}
}

// stripConnection removes Connection and the fields it names, as a proxy must.
func stripConnection(head []string) []string {
	named := map[string]bool{"connection": true}
	for _, line := range head[1:] {
		name, value, _ := strings.Cut(line, ":")
		if strings.EqualFold(strings.TrimSpace(name), "connection") {
			for _, option := range strings.Split(value, ",") {
				named[strings.ToLower(strings.TrimSpace(option))] = true
			}
		}
	}
	kept := head[:1:1]
	for _, line := range head[1:] {
		name, _, _ := strings.Cut(line, ":")
		if !named[strings.ToLower(strings.TrimSpace(name))] {
			kept = append(kept, line)
		}
	}
	return kept
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
