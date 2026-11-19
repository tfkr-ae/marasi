package marasi

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestReadH2RequestText(t *testing.T) {
	t.Run("should put :method and :path first when there is no list to place them by, with lines as typed", func(t *testing.T) {
		got, err := readH2RequestText([]byte("PUT /x?y=1 HTTP/2\n:scheme: https\n:authority: a.example\nX-Name:  two spaces\n\nbody\n\nmore"), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := h2Fields{{":method", "PUT", false}, {":path", "/x?y=1", false}, {":scheme", "https", false}, {":authority", "a.example", false}, {"X-Name", " two spaces", false}}
		if !equalFields(got.fields, want) || string(got.body) != "body\n\nmore" || got.authority != "" {
			t.Fatalf("wanted:\n%v %q\ngot:\n%v %q %q", want, "body\n\nmore", got.fields, got.body, got.authority)
		}
	})

	t.Run("should put :method and :path back where the shown list had them and keep its never-indexed fields", func(t *testing.T) {
		like := h2Fields{{":authority", "a.example", false}, {":method", "GET", false}, {":scheme", "https", false}, {":path", "/", false}, {"cookie", "s=1", true}}
		got, err := readH2RequestText(h2RequestText(like), like)
		if err != nil {
			t.Fatal(err)
		}
		if !equalFields(got.fields, like) || !got.fields[4].Sensitive {
			t.Fatalf("wanted:\n%v\ngot:\n%v", like, got.fields)
		}
	})

	t.Run("should send an absolute-form target to its authority with its path in :path", func(t *testing.T) {
		got, err := readH2RequestText([]byte("GET https://other.example:8443/p?q HTTP/2\r\n:scheme: https\r\n:authority: a.example\r\n\r\n"), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := h2Fields{{":method", "GET", false}, {":path", "/p?q", false}, {":scheme", "https", false}, {":authority", "other.example:8443", false}}
		if !equalFields(got.fields, want) || got.authority != "other.example:8443" {
			t.Fatalf("wanted:\n%v other.example:8443\ngot:\n%v %q", want, got.fields, got.authority)
		}
	})

	t.Run("should refuse text that is not HTTP/2", func(t *testing.T) {
		for _, raw := range []string{"GET / HTTP/1.1\r\n\r\n", "GET /\r\n\r\n", "GET / HTTP/2\r\nno colon\r\n\r\n"} {
			if _, err := readH2RequestText([]byte(raw), nil); !errors.Is(err, errH2Text) {
				t.Errorf("%q\nwanted:\nerrH2Text\ngot:\n%v", raw, err)
			}
		}
	})
}

func TestReadH2ResponseText(t *testing.T) {
	t.Run("should read the status, ignoring a reason phrase, and the fields as typed", func(t *testing.T) {
		fields, body, err := readH2ResponseText([]byte("HTTP/2 404 Not Found\r\nX-A: 1\r\n\r\nnope"), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := h2Fields{{":status", "404", false}, {"X-A", "1", false}}
		if !equalFields(fields, want) || string(body) != "nope" {
			t.Fatalf("wanted:\n%v %q\ngot:\n%v %q", want, "nope", fields, body)
		}
	})
}

func TestH2RequestEdit_Destination(t *testing.T) {
	// A request held inside a tunnel to tunnel.example:443.
	original := httptest.NewRequest("GET", "https://tunnel.example/held", nil)
	original.Host = "tunnel.example"
	wire := &h2Request{fields: h2Fields{{":method", "GET", false}, {":scheme", "https", false}, {":authority", "tunnel.example", false}, {":path", "/held", false}}}

	t.Run("should keep the held request's destination when the edit changes only :authority and host", func(t *testing.T) {
		edit := h2RequestEdit(original, wire)
		if err := edit.rebuild([]byte("GET /held HTTP/2\r\n:scheme: https\r\n:authority: evil.invalid\r\nhost: evil.invalid\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		pair := &Pair{}
		edit.apply(pair)
		if pair.Request.URL.Host != "tunnel.example" || pair.Request.Host != "evil.invalid" {
			t.Fatalf("wanted it sent to tunnel.example with :authority evil.invalid\ngot: sent to %q with %q", pair.Request.URL.Host, pair.Request.Host)
		}
	})

	t.Run("should send the edit where its request line names when the edit names a target", func(t *testing.T) {
		edit := h2RequestEdit(original, wire)
		if err := edit.rebuild([]byte("GET https://other.example/held HTTP/2\r\n:scheme: https\r\n:authority: tunnel.example\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		pair := &Pair{}
		edit.apply(pair)
		if pair.Request.URL.Host != "other.example" {
			t.Fatalf("wanted it sent to other.example\ngot: %q", pair.Request.URL.Host)
		}
	})
}
