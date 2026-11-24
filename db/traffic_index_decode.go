package db

import (
	"bytes"
	"strings"

	"github.com/tfkr-ae/marasi/internal/contentcoding"
)

// bodyDecoderVersion is the version of the rules the body decoder follows. It
// is the traffic index job's version, so bumping it re-indexes every pair when
// a project opens. The first decoder kept the version that body index rows
// written before decoding already had: pairs indexed before decoding are
// re-decoded only by an explicit rebuild of the job (docs/adr/2026-10-14-search-index-splits-annotations-from-bodies.md).
const bodyDecoderVersion = 1

// maxDecodedBodySize caps a decoded body, so a small compressed body cannot
// expand into gigabytes of index text.
const maxDecodedBodySize = 10 << 20

// indexableMessageBody returns the text of a message body to index. It
// decodes the body by the Content-Encoding of head, in the reverse of the
// order the codings were applied. When the body cannot be decoded, or decodes
// to more than maxDecodedBodySize, the body is indexed as stored. Either way
// a binary body indexes nothing.
func indexableMessageBody(head, body []byte) any {
	if decoded, err := contentcoding.Decode(contentCodings(head), body, maxDecodedBodySize); err == nil {
		return indexableBody(decoded)
	}
	return indexableBody(body)
}

// contentCodings returns the content codings of a message head in the order
// they were applied, lower case, across every Content-Encoding header line.
// identity codings are left out. A line that starts with ":" is a
// pseudo-header field of the HTTP/2 text form, not a header field.
func contentCodings(head []byte) []string {
	var values []string
	for _, line := range bytes.Split(head, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		name, value, ok := strings.Cut(string(line), ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "content-encoding") {
			values = append(values, value)
		}
	}
	return contentcoding.Parse(values)
}
