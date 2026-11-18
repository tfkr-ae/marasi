package h1_test

import (
	"bufio"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// readWhole reads one request, head and body, from raw.
func readWhole(t *testing.T, raw string) (*h1.RequestHead, *h1.Body) {
	t.Helper()
	br := bufio.NewReader(strings.NewReader(raw))
	head, err := h1.ReadRequestHead(br)
	if err != nil {
		t.Fatalf("reading head: %v", err)
	}
	framing, err := h1.RequestFraming(head)
	if err != nil {
		t.Fatalf("framing: %v", err)
	}
	body, err := h1.ReadBody(br, framing)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return head, body
}

func TestFrameRequestBody(t *testing.T) {
	cases := []struct {
		name    string
		request string
		edit    func(head *h1.RequestHead)
		payload string
		want    string
	}{
		{
			name:    "should send an unchanged chunked body exactly as it was read",
			request: "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n2;e=1\r\nab\r\n1\r\nc\r\n0\r\nX-T: 1\r\n\r\n",
			payload: "abc",
			want:    "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n2;e=1\r\nab\r\n1\r\nc\r\n0\r\nX-T: 1\r\n\r\n",
		},
		{
			name:    "should re-chunk an edited chunked body and keep its trailers",
			request: "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\nX-T: 1\r\n\r\n",
			payload: "0123456789abcdefg",
			want:    "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n11\r\n0123456789abcdefg\r\n0\r\nX-T: 1\r\n\r\n",
		},
		{
			name:    "should re-chunk an emptied chunked body as the last chunk only",
			request: "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\n",
			payload: "",
			want:    "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
		},
		{
			name:    "should remove a Content-Length an edit added to a chunked body",
			request: "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\n",
			edit: func(head *h1.RequestHead) {
				v := head.Header.View()
				v["Content-Length"] = []string{"2"}
				head.Header.Merge(v)
			},
			payload: "hi",
			want:    "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nhi\r\n0\r\n\r\n",
		},
		{
			name:    "should rewrite Content-Length in place with its spelling",
			request: "POST / HTTP/1.1\r\ncontent-length:3\r\nX-A: a\r\n\r\nabc",
			payload: "hello",
			want:    "POST / HTTP/1.1\r\ncontent-length: 5\r\nX-A: a\r\n\r\nhello",
		},
		{
			name:    "should keep a Content-Length body set to itself byte for byte",
			request: "POST / HTTP/1.1\r\ncontent-length:  3 \r\n\r\nabc",
			payload: "abc",
			want:    "POST / HTTP/1.1\r\ncontent-length:  3 \r\n\r\nabc",
		},
		{
			name:    "should append Content-Length when an edit adds a body to a request with neither",
			request: "GET / HTTP/1.1\r\nX-A: a\r\n\r\n",
			payload: "hi",
			want:    "GET / HTTP/1.1\r\nX-A: a\r\nContent-Length: 2\r\n\r\nhi",
		},
		{
			name:    "should add nothing to a request with neither and no body",
			request: "GET / HTTP/1.1\r\nX-A: a\r\n\r\n",
			payload: "",
			want:    "GET / HTTP/1.1\r\nX-A: a\r\n\r\n",
		},
		{
			name:    "should re-chunk a body whose head an edit switched to chunked",
			request: "POST / HTTP/1.1\r\nContent-Length: 3\r\n\r\nabc",
			edit:    func(head *h1.RequestHead) { setField(head, "Transfer-Encoding", "chunked") },
			payload: "abc",
			want:    "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			head, body := readWhole(t, c.request)
			if c.edit != nil {
				c.edit(head)
			}
			out := h1.FrameRequestBody(head, body, []byte(c.payload))
			if got := string(head.Bytes()) + string(out); got != c.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.want, got)
			}
		})
	}
}

// setField replaces the framing fields with one field, through the view.
func setField(head *h1.RequestHead, name, value string) {
	v := head.Header.View()
	for k := range v {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") {
			delete(v, k)
		}
	}
	v[name] = []string{value}
	if err := head.Header.Merge(v); err != nil {
		panic(err)
	}
}

func TestFrameResponseBody(t *testing.T) {
	read := func(t *testing.T, raw, method string) (*h1.ResponseHead, *h1.Body) {
		t.Helper()
		br := bufio.NewReader(strings.NewReader(raw))
		head, err := h1.ReadResponseHead(br)
		if err != nil {
			t.Fatalf("reading head: %v", err)
		}
		framing, _ := h1.ResponseFraming(head, method)
		body, err := h1.ReadBody(br, framing)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		return head, body
	}
	cases := []struct {
		name     string
		response string
		method   string
		payload  string
		want     string
	}{
		{
			name:     "should drop the Content-Length of a response framed with both and keep its chunks",
			response: "HTTP/1.1 200 OK\r\nContent-Length: 99\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\n\r\n",
			method:   "GET",
			payload:  "ok",
			want:     "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\n\r\n",
		},
		{
			name:     "should send an edited body that runs until close as it is",
			response: "HTTP/1.1 200 OK\r\nTransfer-Encoding: gzip\r\n\r\nzzz",
			method:   "GET",
			payload:  "yy",
			want:     "HTTP/1.1 200 OK\r\nTransfer-Encoding: gzip\r\n\r\nyy",
		},
		{
			name:     "should send no body for a response to HEAD",
			response: "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n",
			method:   "HEAD",
			payload:  "ignored",
			want:     "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			head, body := read(t, c.response, c.method)
			out := h1.FrameResponseBody(head, c.method, body, []byte(c.payload))
			if got := string(head.Bytes()) + string(out); got != c.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.want, got)
			}
		})
	}
}

func TestRemoveConnectionOptions(t *testing.T) {
	cases := []struct {
		name        string
		head        string
		keepUpgrade bool
		want        string
	}{
		{
			name: "should remove Connection and every field it names on any of its lines",
			head: "GET / HTTP/1.1\r\nConnection: X-A, keep-alive\r\nX-A: 1\r\nkeep-alive: t=5\r\nCONNECTION: te\r\nTe: trailers\r\nX-B: 2\r\n\r\n",
			want: "GET / HTTP/1.1\r\nX-B: 2\r\n\r\n",
		},
		{
			name: "should change no bytes when there is no Connection",
			head: "GET / HTTP/1.1\r\nKeep-Alive: t=5\r\n\r\n",
			want: "GET / HTTP/1.1\r\nKeep-Alive: t=5\r\n\r\n",
		},
		{
			name:        "should keep an upgrade with only its option in Connection",
			head:        "GET / HTTP/1.1\r\nconnection: keep-alive, upgrade\r\nUpgrade: websocket\r\n\r\n",
			keepUpgrade: true,
			want:        "GET / HTTP/1.1\r\nconnection: upgrade\r\nUpgrade: websocket\r\n\r\n",
		},
		{
			name:        "should keep a Connection naming only upgrade byte for byte",
			head:        "GET / HTTP/1.1\r\nConnection:  Upgrade\r\nUpgrade: websocket\r\n\r\n",
			keepUpgrade: true,
			want:        "GET / HTTP/1.1\r\nConnection:  Upgrade\r\nUpgrade: websocket\r\n\r\n",
		},
		{
			name: "should remove an upgrade the proxy does not carry",
			head: "HTTP/1.1 200 OK\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n",
			want: "HTTP/1.1 200 OK\r\n\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(c.head))
			var got string
			if strings.HasPrefix(c.head, "HTTP/") {
				head, err := h1.ReadResponseHead(br)
				if err != nil {
					t.Fatalf("reading head: %v", err)
				}
				head.Header.RemoveConnectionOptions(c.keepUpgrade)
				got = string(head.Bytes())
			} else {
				head, err := h1.ReadRequestHead(br)
				if err != nil {
					t.Fatalf("reading head: %v", err)
				}
				head.Header.RemoveConnectionOptions(c.keepUpgrade)
				got = string(head.Bytes())
			}
			if got != c.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.want, got)
			}
		})
	}
}
