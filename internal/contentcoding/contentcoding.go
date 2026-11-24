// Package contentcoding reads a message's content codings and undoes them.
// The proxy decodes a response body with it for the chain and storage, and
// the search index decodes stored bodies with it, so both decode the same
// codings the same way.
package contentcoding

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// ErrTooLarge means a body decodes to more than the limit Decode was given.
var ErrTooLarge = errors.New("decoded content is larger than the limit")

// ErrUnknown means a coding is not one Decode undoes.
var ErrUnknown = errors.New("unknown content coding")

// Parse returns the content codings in the values of a message's
// Content-Encoding field lines, in the order they were applied, lower case,
// with identity left out: "gzip, BR" on one line, or gzip and br on two,
// give [gzip br].
func Parse(values []string) []string {
	var codings []string
	for _, value := range values {
		for _, coding := range strings.Split(value, ",") {
			coding = strings.ToLower(strings.TrimSpace(coding))
			if coding != "" && coding != "identity" {
				codings = append(codings, coding)
			}
		}
	}
	return codings
}

// Known reports whether Decode undoes every one of codings.
func Known(codings []string) bool {
	for _, coding := range codings {
		switch coding {
		case "gzip", "x-gzip", "deflate", "br", "zstd":
		default:
			return false
		}
	}
	return true
}

// Decode undoes codings, applied in order, on body, last first. It reads at
// most limit decoded bytes at each step: a body that decodes to more fails
// with ErrTooLarge. A coding Known does not accept fails with ErrUnknown.
func Decode(codings []string, body []byte, limit int64) ([]byte, error) {
	if !Known(codings) {
		return nil, fmt.Errorf("%w in %q", ErrUnknown, strings.Join(codings, ", "))
	}
	for i := len(codings) - 1; i >= 0 && len(body) > 0; i-- {
		var err error
		if body, err = decode(codings[i], body, limit); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", codings[i], err)
		}
	}
	return body, nil
}

func decode(coding string, body []byte, limit int64) ([]byte, error) {
	switch coding {
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readLimited(r, limit)
	case "deflate":
		// deflate is meant to be zlib-wrapped (RFC 9110 §8.4.1.2), but some
		// servers send raw deflate data.
		if r, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			if decoded, err := readLimited(r, limit); err == nil || errors.Is(err, ErrTooLarge) {
				return decoded, err
			}
		}
		return readLimited(flate.NewReader(bytes.NewReader(body)), limit)
	case "br":
		return readLimited(brotli.NewReader(bytes.NewReader(body)), limit)
	case "zstd":
		r, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(zstdMaxMemory(limit)))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readLimited(r, limit)
	}
	return nil, fmt.Errorf("%w %q", ErrUnknown, coding)
}

// zstdMaxMemory bounds the memory a zstd frame may ask for: twice limit,
// so a frame's window cannot outgrow what the body may decode to.
func zstdMaxMemory(limit int64) uint64 {
	if limit <= 0 || limit > math.MaxInt64/2 {
		return math.MaxInt64
	}
	return uint64(limit) * 2
}

// readLimited reads all of r, failing once it holds more than limit bytes.
// One byte past limit is read, so a body of exactly limit bytes decodes.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	n := limit + 1
	if n <= 0 {
		n = math.MaxInt64
	}
	decoded, err := io.ReadAll(io.LimitReader(bufio.NewReader(r), n))
	if err != nil {
		return nil, err
	}
	if int64(len(decoded)) > limit {
		return nil, ErrTooLarge
	}
	return decoded, nil
}
