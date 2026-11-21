package marasi

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// decompressLimitThreshold is the body threshold of the decompression limit
// tests: small, so a body of zeros that compresses to a few bytes passes it.
const decompressLimitThreshold = 1 << 10

func TestCompressedResponseModifier_DecompressLimit(t *testing.T) {
	cases := []struct {
		name   string
		coding string
		body   func(*testing.T, string) (io.ReadCloser, int)
	}{
		{name: "gzip", coding: "gzip", body: testGzipBody},
		{name: "brotli", coding: "br", body: testBrotliBody},
	}
	for _, tc := range cases {
		t.Run("should leave a "+tc.name+" body that decodes past the body threshold coded and log it", func(t *testing.T) {
			proxy := &Proxy{
				Config:         &Config{BodyThreshold: decompressLimitThreshold},
				DBWriteChannel: make(chan any, 1),
			}
			codedBody, _ := tc.body(t, strings.Repeat("\x00", decompressLimitThreshold+1))
			coded, _ := io.ReadAll(codedBody)
			res := testResponse(string(coded))
			res.Header.Set("Content-Encoding", tc.coding)
			pair := &Pair{
				Request:  &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "example.com", Path: "/bomb"}},
				Response: res,
			}

			err := CompressedResponseModifier(proxy, pair)

			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if got, _ := io.ReadAll(res.Body); !bytes.Equal(got, coded) {
				t.Fatalf("\nwanted:\nthe coded body, %d bytes\ngot:\n%d bytes", len(coded), len(got))
			}
			if res.Header.Get("Content-Encoding") != tc.coding || pair.decoded != nil {
				t.Fatalf("\nwanted:\nthe response untouched\ngot:\n%v, %v", res.Header, pair.decoded)
			}
			if got, _ := pair.Metadata[contentDecodingErrorMetadataKey].(string); !strings.Contains(got, ErrDecodedContentTooLarge.Error()) {
				t.Fatalf("\nwanted:\ncontent_decoding_error containing %q\ngot:\n%v", ErrDecodedContentTooLarge.Error(), pair.Metadata)
			}
			select {
			case item := <-proxy.DBWriteChannel:
				entry, ok := item.(*domain.Log)
				if !ok || entry.Level != "WARN" || !strings.Contains(entry.Message, "https://example.com/bomb") {
					t.Fatalf("\nwanted:\na WARN log naming https://example.com/bomb\ngot:\n%#v", item)
				}
			default:
				t.Fatalf("\nwanted:\na WARN log\ngot:\nnone")
			}
		})

		t.Run("should decode a "+tc.name+" body that decodes to exactly the body threshold", func(t *testing.T) {
			proxy := &Proxy{Config: &Config{BodyThreshold: decompressLimitThreshold}}
			want := strings.Repeat("\x00", decompressLimitThreshold)
			codedBody, _ := tc.body(t, want)
			coded, _ := io.ReadAll(codedBody)
			res := testResponse(string(coded))
			res.Header.Set("Content-Encoding", tc.coding)
			pair := &Pair{Response: res}

			if err := CompressedResponseModifier(proxy, pair); err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if got, _ := io.ReadAll(res.Body); string(got) != want {
				t.Fatalf("\nwanted:\n%d decoded bytes\ngot:\n%d bytes", len(want), len(got))
			}
			if res.Header.Get("Content-Encoding") != "" {
				t.Fatalf("\nwanted:\nno Content-Encoding\ngot:\n%q", res.Header.Get("Content-Encoding"))
			}
		})
	}
}
