package db

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// gzipText compresses text for a Content-Encoding: gzip body.
func gzipText(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(text)); err != nil {
		t.Fatalf("compressing body: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("compressing body: %v", err)
	}
	return buf.Bytes()
}

// repeatedTermPairs hold runs of one character, where a term that repeats a
// trigram is searched by its text rather than its trigram positions.
func repeatedTermPairs(t *testing.T) []textPair {
	return []textPair{
		{
			name:     "run20",
			request:  "GET /a HTTP/1.1\r\nHost: example.com\r\n\r\n",
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n<" + strings.Repeat("x", 20) + ">"),
		},
		{
			name:     "run19",
			request:  "GET /b HTTP/1.1\r\nHost: example.com\r\n\r\n",
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n<" + strings.Repeat("x", 19) + "> <" + strings.Repeat("x", 19) + ">"),
		},
		{
			name:     "upper",
			request:  "POST /c HTTP/1.1\r\nHost: example.com\r\n\r\n" + strings.Repeat("X", 25),
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nok"),
		},
		{
			name:     "gzip",
			request:  "GET /d HTTP/1.1\r\nHost: example.com\r\n\r\n",
			response: append([]byte("HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\nContent-Type: text/plain\r\n\r\n"), gzipText(t, "é"+strings.Repeat("xxX", 5))...),
		},
		{
			name:     "head",
			request:  "GET /e HTTP/1.1\r\nHost: example.com\r\nX-Pad: " + strings.Repeat("x", 30) + "\r\n\r\n",
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nok"),
		},
		{
			name:     "kelvin",
			request:  "GET /f HTTP/1.1\r\nHost: example.com\r\n\r\n",
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nkkkKkk"),
		},
	}
}

func TestTrafficRepo_ListTrafficRepeatedTrigramSearch(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, repeatedTermPairs(t))

	x20 := strings.Repeat("x", 20)
	tests := []struct {
		query string
		want  string // names newest first
	}{
		{query: `response_body:"` + x20 + `"`, want: "run20"},
		{query: `response_body:"` + strings.Repeat("x", 19) + `"`, want: "run19,run20"},
		{query: `request_body:"` + x20 + `"`, want: "upper"},
		{query: `request_head:"` + x20 + `"`, want: "head"},
		{query: `response_head:"` + x20 + `"`, want: ""},
		{query: `"` + x20 + `"`, want: "head,upper,run20"},
		{query: `"` + strings.ToUpper(x20) + `"`, want: "head,upper,run20"},
		{query: `"<` + x20 + `>"`, want: "run20"},
		{query: `NOT "` + x20 + `"`, want: "kelvin,gzip,run19"},
		{query: `"` + x20 + `" OR "okok"`, want: "head,upper,run20"},
		{query: `response_body:"xxXxxXxxXxxXxxX"`, want: "gzip,run19,run20"},
		{query: `response_body:"éxxxxxxxx"`, want: "gzip"},
		// FTS5 folds the Kelvin sign to k, which the text check leaves to
		// FTS5 itself.
		{query: `response_body:"kkkkkk"`, want: "kelvin"},
	}
	for _, test := range tests {
		t.Run("should find "+test.want+" for "+test.query, func(t *testing.T) {
			if got := listNames(t, repo, ids, test.query); got != test.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", test.want, got)
			}
		})
	}
}

func TestPairTextCheck(t *testing.T) {
	t.Parallel()
	response := []byte("HTTP/1.1 200 OK\r\n\r\nAbc xxxx café")
	tests := []struct {
		name        string
		column      string
		term        string
		match, know bool
	}{
		{name: "should match ASCII in another case", column: "response_body", term: "ABC XX", match: true, know: true},
		{name: "should match non-ASCII written the same", column: "response_body", term: "xx café", match: true, know: true},
		{name: "should leave a miss in non-ASCII text to FTS5", column: "response_body", term: "xxxxx", know: false},
		{name: "should leave a non-ASCII case difference to FTS5", column: "response_body", term: "CAFÉ", know: false},
		{name: "should settle a miss in ASCII text", column: "response_head", term: "xxxxx", know: true},
		{name: "should match in any column", column: "", term: "200 ok", match: true, know: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			match, known, err := pairTextCheck(nil, response, test.column, test.term)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if match != test.match || known != test.know {
				t.Fatalf("\nwanted:\nmatch %v known %v\ngot:\nmatch %v known %v", test.match, test.know, match, known)
			}
		})
	}
}
