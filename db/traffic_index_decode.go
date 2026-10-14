package db

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// bodyDecoderVersion is the version of the rules decodeBody follows. It is
// the traffic index job's version, so bumping it re-indexes every pair when a
// project opens. The first decoder kept the version that body index rows
// written before decoding already had: pairs indexed before decoding are
// re-decoded only by an explicit rebuild of the job (ADR-0038).
const bodyDecoderVersion = 1

// maxDecodedBodySize caps a decoded body, so a small compressed body cannot
// expand into gigabytes of index text.
const maxDecodedBodySize = 10 << 20

var errDecodedBodyTooLarge = errors.New("decoded body is larger than the cap")

// indexableMessageBody returns the text of a message body to index. It
// decodes the body by the Content-Encoding of head, in the reverse of the
// order the codings were applied. When the body cannot be decoded, or decodes
// to more than maxDecodedBodySize, the body is indexed as stored. Either way
// a binary body indexes nothing.
func indexableMessageBody(head, body []byte) any {
	if decoded, err := decodeBody(contentCodings(head), body); err == nil {
		return indexableBody(decoded)
	}
	return indexableBody(body)
}

// contentCodings returns the content codings of a message head in the order
// they were applied, lower case, across every Content-Encoding header line.
// identity codings are left out.
func contentCodings(head []byte) []string {
	var codings []string
	for _, line := range bytes.Split(head, []byte("\n")) {
		name, value, ok := strings.Cut(string(line), ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "content-encoding") {
			continue
		}
		for _, coding := range strings.Split(value, ",") {
			coding = strings.ToLower(strings.TrimSpace(coding))
			if coding != "" && coding != "identity" {
				codings = append(codings, coding)
			}
		}
	}
	return codings
}

// decodeBody undoes codings, applied in order, on body.
func decodeBody(codings []string, body []byte) ([]byte, error) {
	for i := len(codings) - 1; i >= 0 && len(body) > 0; i-- {
		var err error
		if body, err = decodeCoding(codings[i], body); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", codings[i], err)
		}
	}
	return body, nil
}

func decodeCoding(coding string, body []byte) ([]byte, error) {
	switch coding {
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		return readCapped(r)
	case "deflate":
		// deflate is meant to be zlib-wrapped, but some servers send raw
		// deflate data.
		if r, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			if decoded, err := readCapped(r); err == nil || errors.Is(err, errDecodedBodyTooLarge) {
				return decoded, err
			}
		}
		return readCapped(flate.NewReader(bytes.NewReader(body)))
	case "br":
		return readCapped(brotli.NewReader(bytes.NewReader(body)))
	case "zstd":
		r, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxDecodedBodySize*2))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readCapped(r)
	}
	return nil, fmt.Errorf("unknown content coding %q", coding)
}

// readCapped reads all of r, failing once it holds more than
// maxDecodedBodySize bytes.
func readCapped(r io.Reader) ([]byte, error) {
	decoded, err := io.ReadAll(io.LimitReader(bufio.NewReader(r), maxDecodedBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(decoded) > maxDecodedBodySize {
		return nil, errDecodedBodyTooLarge
	}
	return decoded, nil
}
