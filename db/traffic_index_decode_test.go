package db

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// encode applies each content coding to body in order, the way a server
// that sends Content-Encoding: <codings> does.
func encode(t *testing.T, body string, codings ...string) string {
	t.Helper()
	data := []byte(body)
	for _, coding := range codings {
		var out bytes.Buffer
		var w io.WriteCloser
		var err error
		switch coding {
		case "gzip":
			w = gzip.NewWriter(&out)
		case "deflate":
			w, err = zlib.NewWriterLevel(&out, zlib.DefaultCompression)
		case "raw-deflate":
			w, err = flate.NewWriter(&out, flate.DefaultCompression)
		case "br":
			w = brotli.NewWriter(&out)
		case "zstd":
			w, err = zstd.NewWriter(&out)
		default:
			t.Fatalf("unknown coding %q", coding)
		}
		if err != nil {
			t.Fatalf("creating %s writer: %v", coding, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("encoding %s: %v", coding, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("closing %s writer: %v", coding, err)
		}
		data = out.Bytes()
	}
	return string(data)
}

func TestTrafficRepo_ListTrafficSearchesDecodedBodies(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	response := func(header, body string) []byte {
		return []byte("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n" + header + "\r\n\r\n" + body)
	}
	ids := captureTextPairs(t, repo, []textPair{
		{name: "gzip", request: "GET /gzip HTTP/1.1\r\n\r\n", response: response("Content-Encoding: gzip", encode(t, "decoded-gzip-text", "gzip"))},
		{name: "deflate", request: "GET /deflate HTTP/1.1\r\n\r\n", response: response("Content-Encoding: deflate", encode(t, "decoded-deflate-text", "deflate"))},
		{name: "rawdeflate", request: "GET /rawdeflate HTTP/1.1\r\n\r\n", response: response("Content-Encoding: deflate", encode(t, "decoded-rawdeflate-text", "raw-deflate"))},
		{name: "zstd", request: "GET /zstd HTTP/1.1\r\n\r\n", response: response("Content-Encoding: zstd", encode(t, "decoded-zstd-text", "zstd"))},
		{name: "stacked", request: "GET /stacked HTTP/1.1\r\n\r\n", response: response("Content-Encoding: gzip, br", encode(t, "decoded-stacked-text", "gzip", "br"))},
		{name: "split", request: "GET /split HTTP/1.1\r\n\r\n", response: response("Content-Encoding: gzip\r\ncontent-encoding: zstd", encode(t, "decoded-split-text", "gzip", "zstd"))},
		{
			name:     "request",
			request:  "POST /upload HTTP/1.1\r\nContent-Encoding: gzip\r\n\r\n" + encode(t, "decoded-request-text", "gzip"),
			response: response("", "plain"),
		},
		{name: "mislabelled", request: "GET /mislabelled HTTP/1.1\r\n\r\n", response: response("Content-Encoding: gzip", "stored-as-text")},
		{name: "unknown", request: "GET /unknown HTTP/1.1\r\n\r\n", response: response("Content-Encoding: compress", "unknown-coding-text")},
	})

	for query, want := range map[string]string{
		`response_body:"decoded-gzip-text"`:       "gzip",
		`response_body:"decoded-deflate-text"`:    "deflate",
		`response_body:"decoded-rawdeflate-text"`: "rawdeflate",
		`"decoded-zstd-text"`:                     "zstd",
		`"decoded-stacked-text"`:                  "stacked",
		`"decoded-split-text"`:                    "split",
		`request_body:"decoded-request-text"`:     "request",
		`response_body:"stored-as-text"`:          "mislabelled",
		`response_body:"unknown-coding-text"`:     "unknown",
	} {
		t.Run(query, func(t *testing.T) {
			if got := listNames(t, repo, ids, query); got != want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})
	}
}

func TestTrafficRepo_RebuildDecodesPairsIndexedBeforeDecoding(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{{
		name:     "old",
		request:  "GET /old HTTP/1.1\r\n\r\n",
		response: []byte("HTTP/1.1 200 OK\r\nContent-Encoding: br\r\n\r\n" + encode(t, "rebuilt-text", "br")),
	}})
	if err := repo.UpdateNote(ids["old"], "kept-note"); err != nil {
		t.Fatalf("inserting note: %v", err)
	}
	// A row an earlier binary wrote, without the decoded body text.
	writeOldBodyIndexRow(t, repo, ids["old"], "kept-note", "")
	assertListNames(t, repo, ids, "before rebuild", `"rebuilt-text"`, "")

	if err := repo.RebuildJob(TrafficIndexJob); err != nil {
		t.Fatalf("RebuildJob: %v", err)
	}
	assertListNames(t, repo, ids, "during rebuild", `note:"kept-note"`, "old")
	finishJob(t, repo, TrafficIndexJob)

	assertListNames(t, repo, ids, "after rebuild", `"rebuilt-text"`, "old")
	assertListNames(t, repo, ids, "after rebuild", `note:"kept-note"`, "old")
}

func TestTrafficRepo_DecodedBodyCap(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	// Past the 10 MiB cap the body is indexed as stored, which is binary,
	// so its text is not searchable.
	huge := "overflowing-text " + strings.Repeat("x", 10<<20)
	ids := captureTextPairs(t, repo, []textPair{
		{name: "huge", request: "GET /huge HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\n\r\n" + encode(t, huge, "gzip"))},
		{name: "fits", request: "GET /fits HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\n\r\n" + encode(t, "overflowing-text fits", "gzip"))},
	})

	if got := listNames(t, repo, ids, `"overflowing-text"`); got != "fits" {
		t.Fatalf("\nwanted:\n%q\ngot:\n%q", "fits", got)
	}
}
