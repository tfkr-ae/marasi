package stream

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func TestDecompress(t *testing.T) {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(strings.Repeat("x", 100)))
	w.Close()

	t.Run("should decode a gzip payload within the limit", func(t *testing.T) {
		out, err := Decompress("GZIP", b.Bytes(), 100)
		if err != nil || string(out) != strings.Repeat("x", 100) {
			t.Fatalf("\nwanted:\n100 bytes of x\ngot:\n%q, %v", out, err)
		}
	})

	t.Run("should refuse a payload that decodes past the limit as too large", func(t *testing.T) {
		if _, err := Decompress("gzip", b.Bytes(), 99); err == nil || !strings.HasPrefix(err.Error(), "too large") {
			t.Fatalf("\nwanted:\nan error starting \"too large\"\ngot:\n%v", err)
		}
	})

	t.Run("should decode only gzip and deflate", func(t *testing.T) {
		for encoding, want := range map[string]bool{"gzip": true, " Deflate ": true, "snappy": false, "identity": false, "": false} {
			if Decodable(encoding) != want {
				t.Errorf("Decodable(%q)\nwanted:\n%t\ngot:\n%t", encoding, want, !want)
			}
		}
	})
}
