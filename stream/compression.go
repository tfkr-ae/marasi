package stream

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"strings"
)

// Per-message compression (spec: Compression): gRPC and gRPC-Web set the
// envelope's compressed flag on a message compressed with the grpc-encoding
// of its direction's head, Connect with connect-content-encoding. Each
// message is compressed on its own, so the proxy decodes it for display and
// editing, forwards an unedited one as it came, and sends an edited one
// uncompressed with the flag cleared. See the 2026-11-20 amendment of
// docs/adr/2026-10-11-proxy-terminates-permessage-deflate-per-leg.md.

// flagCompressed is the envelope flag bit of a compressed message.
const flagCompressed = 1

// EncodingHeader is the head field naming framing's per-message encoding,
// "" for a framing without per-message compression.
func EncodingHeader(f Framing) string {
	switch f {
	case FramingGRPC, FramingGRPCWeb:
		return "grpc-encoding"
	case FramingConnect:
		return "connect-content-encoding"
	}
	return ""
}

// Decodable reports whether the proxy decodes messages compressed with
// encoding: gzip and deflate. Others are shown as they came and cannot be
// edited.
func Decodable(encoding string) bool {
	switch normalEncoding(encoding) {
	case "gzip", "deflate":
		return true
	}
	return false
}

// Decompress decodes payload compressed with encoding, a Decodable one. It
// reads at most limit+1 bytes of output, so a payload that decodes past
// limit costs no more than one at it; that is an error starting "too
// large".
func Decompress(encoding string, payload []byte, limit int64) ([]byte, error) {
	var r io.ReadCloser
	var err error
	switch normalEncoding(encoding) {
	case "gzip":
		r, err = gzip.NewReader(bytes.NewReader(payload))
	case "deflate":
		r, err = zlib.NewReader(bytes.NewReader(payload))
	default:
		return nil, fmt.Errorf("the %q encoding is not decoded", encoding)
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("too large: the message decodes past the %d-byte limit", limit)
	}
	return out, nil
}

// Compress compresses payload with encoding, a Decodable one, as a sender
// compresses a message: an injected one the proxy sends compressed.
func Compress(encoding string, payload []byte) ([]byte, error) {
	var out bytes.Buffer
	var w io.WriteCloser
	switch normalEncoding(encoding) {
	case "gzip":
		w = gzip.NewWriter(&out)
	case "deflate":
		w = zlib.NewWriter(&out)
	default:
		return nil, fmt.Errorf("the %q encoding is not encoded", encoding)
	}
	if _, err := w.Write(payload); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CompressedFlags is flags with the compressed flag set: the flags of a message
// sent compressed.
func CompressedFlags(flags byte) byte {
	return flags | flagCompressed
}

func normalEncoding(encoding string) string {
	return strings.ToLower(strings.TrimSpace(encoding))
}
