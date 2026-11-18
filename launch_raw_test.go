package marasi

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// wireOrigin is an origin that records each request exactly as it arrived,
// head and body, and answers it with a short 200.
type wireOrigin struct {
	addr     string
	requests chan string
}

func newWireOrigin(t *testing.T) *wireOrigin {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	origin := &wireOrigin{addr: l.Addr().String(), requests: make(chan string, 8)}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go origin.serve(conn)
		}
	}()
	return origin
}

func (origin *wireOrigin) serve(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		head, err := h1.ReadRequestHead(br)
		if err != nil {
			return
		}
		framing, err := h1.RequestFraming(head)
		if err != nil {
			return
		}
		body, err := h1.ReadBody(br, framing)
		if err != nil {
			return
		}
		var got bytes.Buffer
		head.WriteTo(&got)
		body.WriteTo(&got)
		origin.requests <- got.String()
		if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"); err != nil {
			return
		}
	}
}

// received waits for the next request the origin read.
func (origin *wireOrigin) received(t *testing.T) string {
	t.Helper()
	select {
	case got := <-origin.requests:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the origin")
		return ""
	}
}

// launchOnH1Engine launches raw through a proxy on the new engine and returns
// what the origin received.
func launchOnH1Engine(t *testing.T, origin *wireOrigin, raw string) string {
	t.Helper()
	h := startLaunchHarnessWith(t, []func(*Proxy) error{withH1Engine()})
	if _, err := h.proxy.Launch(raw, h.launchpadID.String(), false); err != nil {
		t.Fatalf("Launch() returned unexpected error: %v", err)
	}
	got := origin.received(t)
	if launch := h.waitFinished(t); launch.Status != "completed" {
		t.Fatalf("\nwanted:\ncompleted launch\ngot:\n%+v", launch)
	}
	return got
}

func TestH1Engine_LaunchIsSentRaw(t *testing.T) {
	tests := []struct {
		name string
		raw  string // %s is the origin's host
		want string
	}{
		{
			name: "unusual case, order and whitespace reach the origin as typed",
			raw:  "GET /search?q=a|b HTTP/1.1\r\nzz-Last-First: 1\r\nhOsT: %s\r\naccept:   */*  \r\nX-dup: one\r\nx-DUP: two\r\n\r\n",
			want: "GET /search?q=a|b HTTP/1.1\r\nzz-Last-First: 1\r\nhOsT: %s\r\naccept:   */*  \r\nX-dup: one\r\nx-DUP: two\r\n\r\n",
		},
		{
			name: "a path a URL parser would re-escape is sent as typed",
			raw:  "GET /a|b/%7e/c'd HTTP/1.1\r\nHost: %s\r\n\r\n",
			want: "GET /a|b/%7e/c'd HTTP/1.1\r\nHost: %s\r\n\r\n",
		},
		{
			name: "an edited body gets its length on the Content-Length line it had",
			raw:  "POST /form HTTP/1.1\r\nHost: %s\r\ncontent-LENGTH:  999 \r\nX-After: 1\r\n\r\nname=edited",
			want: "POST /form HTTP/1.1\r\nHost: %s\r\ncontent-LENGTH:  11 \r\nX-After: 1\r\n\r\nname=edited",
		},
		{
			name: "a body typed without a Content-Length gets one at the end of the head",
			raw:  "POST /form HTTP/1.1\r\nHost: %s\r\nX-Last: 1\r\n\r\nabc",
			want: "POST /form HTTP/1.1\r\nHost: %s\r\nX-Last: 1\r\nContent-Length: 3\r\n\r\nabc",
		},
		{
			name: "a chunked launch gets no Content-Length",
			raw:  "POST /upload HTTP/1.1\r\nHost: %s\r\ntransfer-encoding: chunked\r\n\r\n3;ext=1\r\nabc\r\n0\r\nx-Trailer: t\r\n\r\n",
			want: "POST /upload HTTP/1.1\r\nHost: %s\r\ntransfer-encoding: chunked\r\n\r\n3;ext=1\r\nabc\r\n0\r\nx-Trailer: t\r\n\r\n",
		},
		{
			name: "a multipart body keeps its CRLFs and bare LFs",
			raw:  "POST /upload HTTP/1.1\r\nHost: %s\r\nContent-Type: multipart/form-data; boundary=XyZ\r\nContent-Length: 1\r\n\r\n--XyZ\r\nContent-Disposition: form-data; name=\"f\"\r\n\r\nline one\nline two\r\n--XyZ--\r\n",
			want: "POST /upload HTTP/1.1\r\nHost: %s\r\nContent-Type: multipart/form-data; boundary=XyZ\r\nContent-Length: 79\r\n\r\n--XyZ\r\nContent-Disposition: form-data; name=\"f\"\r\n\r\nline one\nline two\r\n--XyZ--\r\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := newWireOrigin(t)
			got := launchOnH1Engine(t, origin, sprintfHost(tt.raw, origin.addr))
			if want := sprintfHost(tt.want, origin.addr); got != want {
				t.Fatalf("the origin received\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})
	}
}

// sprintfHost puts host in place of the one %s in format.
func sprintfHost(format, host string) string {
	return string(bytes.Replace([]byte(format), []byte("%s"), []byte(host), 1))
}
