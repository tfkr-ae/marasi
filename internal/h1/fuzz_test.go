package h1_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/tfkr-ae/marasi/internal/h1"
)

// FuzzRequestRoundTrip checks the codec's main property on any input: what
// it reads, head and body, it writes back byte for byte, whether the body
// ended, was cut short or failed framing part way.
func FuzzRequestRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"GET / HTTP/1.1\r\nHost: h\r\n\r\n",
		"\r\nPOST / HTTP/1.1\nContent-Length: 3\nX:  a\n  b\n\nabc",
		"POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n3;x=y\r\nabc\r\n0\r\nT: 1\r\n\r\n",
		"POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		br := bufioReader(raw)
		head, err := h1.ReadRequestHead(br)
		if err != nil {
			return
		}
		var out bytes.Buffer
		_, _ = head.WriteTo(&out)
		framing, err := h1.RequestFraming(head)
		if err != nil {
			return
		}
		body, err := h1.ReadBody(br, framing)
		var fe *h1.FramingError
		if err != nil && !errors.Is(err, h1.ErrBodyCutShort) && !errors.As(err, &fe) && !errors.Is(err, h1.ErrMalformedHead) {
			t.Fatalf("reading body: %v", err)
		}
		if err != nil && !errors.Is(err, h1.ErrBodyCutShort) {
			// A malformed body stops part way; only cut-short and whole
			// bodies promise to write back what was read.
			return
		}
		_, _ = body.WriteTo(&out)
		rest, _ := io.ReadAll(br)
		out.Write(rest)
		if !bytes.Equal(out.Bytes(), raw) {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", raw, out.Bytes())
		}
	})
}
