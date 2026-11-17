package h1_test

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/tfkr-ae/marasi/internal/h1"
)

func TestHeaderMerge(t *testing.T) {
	const head = "GET / HTTP/1.1\r\n" +
		"hOsT: example.com\r\n" +
		"Cookie: a=1\r\n" +
		"X-odd:\t a  b \t\r\n" +
		"cookie: b=2\r\n" +
		"X-Fold: one\r\n  two\r\n" +
		"content-TYPE: text/plain\r\n" +
		"COOKIE:c=3\r\n" +
		"\r\n"
	cases := []struct {
		name string
		edit func(h http.Header)
		want string
	}{
		{
			name: "should change no bytes when nothing is edited",
			edit: func(h http.Header) {},
			want: head,
		},
		{
			name: "should change no bytes when a value is set to itself",
			edit: func(h http.Header) {
				h.Set("X-Odd", "a  b")
				h.Set("X-Fold", "one two")
				h["Cookie"] = []string{"a=1", "b=2", "c=3"}
			},
			want: head,
		},
		{
			name: "should rewrite a changed value in place with the wire name case",
			edit: func(h http.Header) { h.Set("Content-Type", "application/json") },
			want: "GET / HTTP/1.1\r\nhOsT: example.com\r\nCookie: a=1\r\nX-odd:\t a  b \t\r\ncookie: b=2\r\nX-Fold: one\r\n  two\r\n" +
				"content-TYPE: application/json\r\nCOOKIE:c=3\r\n\r\n",
		},
		{
			name: "should put a changed repeated name at its first line",
			edit: func(h http.Header) { h["Cookie"] = []string{"z=9", "y=8"} },
			want: "GET / HTTP/1.1\r\nhOsT: example.com\r\nCookie: z=9\r\nCookie: y=8\r\nX-odd:\t a  b \t\r\nX-Fold: one\r\n  two\r\n" +
				"content-TYPE: text/plain\r\n\r\n",
		},
		{
			name: "should remove every line of a removed name",
			edit: func(h http.Header) { h.Del("Cookie") },
			want: "GET / HTTP/1.1\r\nhOsT: example.com\r\nX-odd:\t a  b \t\r\nX-Fold: one\r\n  two\r\ncontent-TYPE: text/plain\r\n\r\n",
		},
		{
			name: "should remove a name left with no values",
			edit: func(h http.Header) { h["X-Fold"] = []string{} },
			want: "GET / HTTP/1.1\r\nhOsT: example.com\r\nCookie: a=1\r\nX-odd:\t a  b \t\r\ncookie: b=2\r\ncontent-TYPE: text/plain\r\nCOOKIE:c=3\r\n\r\n",
		},
		{
			name: "should replace a folded field with one line",
			edit: func(h http.Header) { h.Set("X-Fold", "flat") },
			want: "GET / HTTP/1.1\r\nhOsT: example.com\r\nCookie: a=1\r\nX-odd:\t a  b \t\r\ncookie: b=2\r\nX-Fold: flat\r\n" +
				"content-TYPE: text/plain\r\nCOOKIE:c=3\r\n\r\n",
		},
		{
			name: "should append new names spelled as the map key in key order",
			edit: func(h http.Header) {
				h["x-lower"] = []string{"1"}
				h.Add("X-Added", "2")
				h.Add("X-Added", "3")
			},
			want: head[:len(head)-2] + "X-Added: 2\r\nX-Added: 3\r\nx-lower: 1\r\n\r\n",
		},
		{
			name: "should treat a raw key as the name it spells",
			edit: func(h http.Header) { h["content-type"] = []string{"text/html"} },
			want: "GET / HTTP/1.1\r\nhOsT: example.com\r\nCookie: a=1\r\nX-odd:\t a  b \t\r\ncookie: b=2\r\nX-Fold: one\r\n  two\r\n" +
				"content-TYPE: text/plain\r\ncontent-TYPE: text/html\r\nCOOKIE:c=3\r\n\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := h1.ReadRequestHead(reader(head))
			if err != nil {
				t.Fatalf("reading head: %v", err)
			}
			view := h.Header.View()
			c.edit(view)
			if err := h.Header.Merge(view); err != nil {
				t.Fatalf("merging: %v", err)
			}
			if got := string(h.Bytes()); got != c.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.want, got)
			}
		})
	}
}

func TestHeaderMergeRefusesInjection(t *testing.T) {
	const head = "GET / HTTP/1.1\r\nX: 1\r\n\r\n"
	cases := []struct {
		name string
		edit func(h http.Header)
	}{
		{"should refuse a CRLF in a value", func(h http.Header) { h.Set("X", "1\r\nEvil: 1") }},
		{"should refuse a NUL in a value", func(h http.Header) { h.Set("X", "1\x00") }},
		{"should refuse a name that is not a token", func(h http.Header) { h["Bad Name"] = []string{"1"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := h1.ReadRequestHead(reader(head))
			view := h.Header.View()
			c.edit(view)
			if err := h.Header.Merge(view); !errors.Is(err, h1.ErrInvalidField) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", h1.ErrInvalidField, err)
			}
			if got := string(h.Bytes()); got != head {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", head, got)
			}
		})
	}
}

func TestHeaderMergeLineEnds(t *testing.T) {
	t.Run("should write new lines with the section's bare LF", func(t *testing.T) {
		h, _ := h1.ReadRequestHead(reader("GET / HTTP/1.1\nX: 1\n\n"))
		view := h.Header.View()
		view.Set("X", "2")
		view.Set("Y", "3")
		if err := h.Header.Merge(view); err != nil {
			t.Fatalf("merging: %v", err)
		}
		want := "GET / HTTP/1.1\nX: 2\nY: 3\n\n"
		if got := string(h.Bytes()); got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})
}

func TestRequestView(t *testing.T) {
	const head = "POST /a/b?q=1 HTTP/1.1\r\nhOsT: example.com\r\ncontent-length: 2\r\nx-Token: t\r\n\r\n"
	t.Run("should expose the request through http.Request", func(t *testing.T) {
		h, _ := h1.ReadRequestHead(reader(head))
		r, err := h.Request()
		if err != nil {
			t.Fatalf("viewing request: %v", err)
		}
		got := []any{r.Method, r.URL.Path, r.URL.RawQuery, r.Host, r.Proto, r.ProtoMinor, r.Header.Get("X-Token"), r.Header.Get("Host"), r.ContentLength, r.RequestURI}
		want := []any{"POST", "/a/b", "q=1", "example.com", "HTTP/1.1", 1, "t", "", int64(2), "/a/b?q=1"}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
			}
		}
	})
	cases := []struct {
		name string
		head string
		edit func(r *http.Request)
		want string
	}{
		{
			name: "should change no bytes when nothing is edited",
			head: head,
			edit: func(r *http.Request) {},
			want: head,
		},
		{
			name: "should change no bytes of an absolute-form request left alone",
			head: "GET http://example.com:8080/a%2Fb?x=%41 HTTP/1.1\r\nHost: other\r\n\r\n",
			edit: func(r *http.Request) {},
			want: "GET http://example.com:8080/a%2Fb?x=%41 HTTP/1.1\r\nHost: other\r\n\r\n",
		},
		{
			name: "should change no bytes of a CONNECT left alone",
			head: "CONNECT example.com:443 HTTP/1.1\r\n\r\n",
			edit: func(r *http.Request) {},
			want: "CONNECT example.com:443 HTTP/1.1\r\n\r\n",
		},
		{
			name: "should rewrite the request line for a new path",
			head: head,
			edit: func(r *http.Request) { r.URL.Path = "/c" },
			want: "POST /c?q=1 HTTP/1.1\r\nhOsT: example.com\r\ncontent-length: 2\r\nx-Token: t\r\n\r\n",
		},
		{
			name: "should keep absolute form for a new path",
			head: "GET http://example.com/a HTTP/1.1\r\n\r\n",
			edit: func(r *http.Request) { r.URL.Path = "/b" },
			want: "GET http://example.com/b HTTP/1.1\r\n\r\n",
		},
		{
			name: "should rewrite the request line for a new method",
			head: head,
			edit: func(r *http.Request) { r.Method = "PUT" },
			want: "PUT /a/b?q=1 HTTP/1.1\r\nhOsT: example.com\r\ncontent-length: 2\r\nx-Token: t\r\n\r\n",
		},
		{
			name: "should rewrite the Host line in place for a new host",
			head: head,
			edit: func(r *http.Request) { r.Host = "other.test" },
			want: "POST /a/b?q=1 HTTP/1.1\r\nhOsT: other.test\r\ncontent-length: 2\r\nx-Token: t\r\n\r\n",
		},
		{
			name: "should merge header edits",
			head: head,
			edit: func(r *http.Request) {
				r.Header.Set("Content-Length", "5")
				r.Header.Del("X-Token")
			},
			want: "POST /a/b?q=1 HTTP/1.1\r\nhOsT: example.com\r\ncontent-length: 5\r\n\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := h1.ReadRequestHead(reader(c.head))
			if err != nil {
				t.Fatalf("reading head: %v", err)
			}
			r, err := h.Request()
			if err != nil {
				t.Fatalf("viewing request: %v", err)
			}
			c.edit(r)
			if err := h.Merge(r); err != nil {
				t.Fatalf("merging: %v", err)
			}
			if got := string(h.Bytes()); got != c.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.want, got)
			}
		})
	}
}

func TestResponseView(t *testing.T) {
	const head = "HTTP/1.1 200 Fine\r\nserver: x\r\nContent-Length: 0\r\n\r\n"
	t.Run("should expose the response through http.Response", func(t *testing.T) {
		h, _ := h1.ReadResponseHead(reader(head))
		r := h.Response()
		got := []any{r.StatusCode, r.Status, r.Proto, r.ProtoMajor, r.Header.Get("Server"), r.ContentLength}
		want := []any{200, "200 Fine", "HTTP/1.1", 1, "x", int64(0)}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
			}
		}
	})
	cases := []struct {
		name string
		head string
		edit func(r *http.Response)
		want string
	}{
		{
			name: "should change no bytes when nothing is edited",
			head: "HTTP/1.1 204\r\nX: 1\r\n\r\n",
			edit: func(r *http.Response) {},
			want: "HTTP/1.1 204\r\nX: 1\r\n\r\n",
		},
		{
			name: "should give a new status code its standard reason",
			head: head,
			edit: func(r *http.Response) { r.StatusCode = 404 },
			want: "HTTP/1.1 404 Not Found\r\nserver: x\r\nContent-Length: 0\r\n\r\n",
		},
		{
			name: "should take the reason from a new status",
			head: head,
			edit: func(r *http.Response) { r.StatusCode, r.Status = 418, "418 Short And Stout" },
			want: "HTTP/1.1 418 Short And Stout\r\nserver: x\r\nContent-Length: 0\r\n\r\n",
		},
		{
			name: "should merge header edits",
			head: head,
			edit: func(r *http.Response) { r.Header.Set("Server", "y") },
			want: "HTTP/1.1 200 Fine\r\nserver: y\r\nContent-Length: 0\r\n\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := h1.ReadResponseHead(reader(c.head))
			if err != nil {
				t.Fatalf("reading head: %v", err)
			}
			r := h.Response()
			c.edit(r)
			if err := h.Merge(r); err != nil {
				t.Fatalf("merging: %v", err)
			}
			if got := string(h.Bytes()); got != c.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", c.want, got)
			}
		})
	}
}

func TestTrailerMerge(t *testing.T) {
	t.Run("should merge a trailer edit and keep the chunks", func(t *testing.T) {
		body, err := readRequest(t, "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nhi\r\n0\r\nx-SUM: 1\r\nOther: 2\r\n\r\n")
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		view := body.Trailer.View()
		view.Set("X-Sum", "9")
		if err := body.Trailer.Merge(view); err != nil {
			t.Fatalf("merging: %v", err)
		}
		var out bytes.Buffer
		_, _ = body.WriteTo(&out)
		want := "2\r\nhi\r\n0\r\nx-SUM: 9\r\nOther: 2\r\n\r\n"
		if out.String() != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, out.String())
		}
	})
}
