package contentcoding

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func encode(t *testing.T, coding, text string) []byte {
	t.Helper()
	var b bytes.Buffer
	var w io.WriteCloser
	switch coding {
	case "gzip", "x-gzip":
		w = gzip.NewWriter(&b)
	case "deflate":
		w = zlib.NewWriter(&b)
	case "raw-deflate":
		w, _ = flate.NewWriter(&b, flate.DefaultCompression)
	case "br":
		w = brotli.NewWriter(&b)
	case "zstd":
		zw, err := zstd.NewWriter(&b)
		if err != nil {
			t.Fatalf("creating the zstd writer: %v", err)
		}
		w = zw
	default:
		t.Fatalf("no encoder for %q", coding)
	}
	if _, err := io.WriteString(w, text); err != nil {
		t.Fatalf("encoding %s: %v", coding, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing the %s writer: %v", coding, err)
	}
	return b.Bytes()
}

func TestParse(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   []string
	}{
		{name: "should read one coding", values: []string{"gzip"}, want: []string{"gzip"}},
		{name: "should lower the case of a coding", values: []string{"GZIP"}, want: []string{"gzip"}},
		{name: "should read stacked codings on one line in the order applied", values: []string{"gzip, Br"}, want: []string{"gzip", "br"}},
		{name: "should read codings across lines in the order applied", values: []string{"gzip", " zstd "}, want: []string{"gzip", "zstd"}},
		{name: "should leave out identity and empty members", values: []string{"identity, , gzip"}, want: []string{"gzip"}},
		{name: "should give none for no lines", values: nil, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.values); !slices.Equal(got, tc.want) {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", tc.want, got)
			}
		})
	}
}

func TestDecode(t *testing.T) {
	const text = "hello, world"

	for _, coding := range []string{"gzip", "x-gzip", "deflate", "br", "zstd"} {
		t.Run("should decode "+coding, func(t *testing.T) {
			got, err := Decode([]string{coding}, encode(t, coding, text), 1<<20)

			if err != nil || string(got) != text {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q, %v", text, got, err)
			}
		})
	}

	t.Run("should decode raw deflate sent as deflate", func(t *testing.T) {
		got, err := Decode([]string{"deflate"}, encode(t, "raw-deflate", text), 1<<20)

		if err != nil || string(got) != text {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q, %v", text, got, err)
		}
	})

	t.Run("should undo stacked codings last first", func(t *testing.T) {
		coded := encode(t, "br", string(encode(t, "gzip", text)))

		got, err := Decode([]string{"gzip", "br"}, coded, 1<<20)

		if err != nil || string(got) != text {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q, %v", text, got, err)
		}
	})

	t.Run("should refuse a coding it does not know before decoding any", func(t *testing.T) {
		_, err := Decode([]string{"compress", "gzip"}, encode(t, "gzip", text), 1<<20)

		if !errors.Is(err, ErrUnknown) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrUnknown, err)
		}
	})

	for _, coding := range []string{"gzip", "deflate", "br", "zstd"} {
		t.Run("should refuse "+coding+" that decodes past the limit and decode one at exactly the limit", func(t *testing.T) {
			const limit = 1 << 10

			_, err := Decode([]string{coding}, encode(t, coding, strings.Repeat("\x00", limit+1)), limit)
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrTooLarge, err)
			}
			got, err := Decode([]string{coding}, encode(t, coding, strings.Repeat("\x00", limit)), limit)
			if err != nil || len(got) != limit {
				t.Fatalf("\nwanted:\n%d bytes\ngot:\n%d bytes, %v", limit, len(got), err)
			}
		})
	}

	t.Run("should fail on a body that is not in its coding", func(t *testing.T) {
		_, err := Decode([]string{"zstd"}, []byte("not zstd"), 1<<20)

		if err == nil || !strings.Contains(err.Error(), "decoding zstd") {
			t.Fatalf("\nwanted:\nan error naming zstd\ngot:\n%v", err)
		}
	})
}
