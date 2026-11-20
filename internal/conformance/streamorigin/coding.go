package streamorigin

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"strings"

	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// A body whose head names a gzip or deflate Content-Encoding is compressed
// as a whole by its sender, which flushes its compressor after each stream
// message so the receiver can read each as it arrives. The recording cuts
// the inflated body into stream messages, so a proxy that compresses the
// body again with its own compressor still matches message for message;
// the compressed bytes are recorded whole at the end, as a KindCoded
// arrival, where a proxy that terminates the coding on each leg differs.

// contentCoding is the coding a Content-Encoding value names, when the
// suite compresses with it: "gzip", "deflate" or "".
func contentCoding(value string) string {
	switch coding := strings.ToLower(strings.TrimSpace(value)); coding {
	case "gzip", "deflate":
		return coding
	}
	return ""
}

// headCoding is the coding a header section's Content-Encoding names.
func headCoding(fields []h2.Field) string {
	for _, f := range fields {
		if strings.EqualFold(f.Name, "content-encoding") {
			return contentCoding(f.Value)
		}
	}
	return ""
}

// compressor is the writer side of gzip and zlib.
type compressor interface {
	io.Writer
	Flush() error
	Close() error
}

// codingSender compresses the messages a side sends when its head names a
// coding, flushing after each and ending the compressed stream with the
// body.
type codingSender struct {
	sender
	buf bytes.Buffer
	w   compressor
}

func (c *codingSender) head(fields []h2.Field, end bool) error {
	switch headCoding(fields) {
	case "gzip":
		w := gzip.NewWriter(&c.buf)
		// A name in the header makes the sender's bytes its own: a proxy
		// that compresses again writes none.
		w.Name = "origin"
		c.w = w
	case "deflate":
		c.w = zlib.NewWriter(&c.buf)
	}
	return c.sender.head(fields, end)
}

func (c *codingSender) message(data []byte, end bool) error {
	if c.w == nil {
		return c.sender.message(data, end)
	}
	c.w.Write(data)
	if end {
		c.w.Close()
	} else {
		c.w.Flush()
	}
	out := bytes.Clone(c.buf.Bytes())
	c.buf.Reset()
	return c.sender.message(out, end)
}

// inflated is what coded inflates to so far with coding: a stream not yet
// ended inflates as far as its bytes go.
func inflated(coding string, coded []byte) []byte {
	var r io.Reader
	var err error
	if coding == "gzip" {
		r, err = gzip.NewReader(bytes.NewReader(coded))
	} else {
		r, err = zlib.NewReader(bytes.NewReader(coded))
	}
	if err != nil {
		return nil
	}
	out, _ := io.ReadAll(r)
	return out
}
