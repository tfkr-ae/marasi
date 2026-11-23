package marasi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tfkr-ae/marasi/stream"
)

// grpcStatusField is the field a gRPC response ends with, in its trailers
// or, for a trailers-only response, its head.
const grpcStatusField = "grpc-status"

// isGRPCResponse reports whether contentType names a gRPC or gRPC-Web
// response, the ones that end with a grpc-status.
func isGRPCResponse(contentType string) bool {
	switch stream.FramingOf(contentType, true) {
	case stream.FramingGRPC, stream.FramingGRPCWeb:
		return true
	}
	return false
}

// responseGRPCStatus is the grpc-status of a response stored whole: from
// its trailers, else from its head when it is trailers-only. It is nil for
// a response that is not gRPC or has no whole-number grpc-status.
func responseGRPCStatus(res *http.Response) *int {
	if res == nil || !isGRPCResponse(res.Header.Get("Content-Type")) {
		return nil
	}
	if status := parseGRPCStatus(res.Trailer.Get(grpcStatusField)); status != nil {
		return status
	}
	return parseGRPCStatus(res.Header.Get(grpcStatusField))
}

// trailersGRPCStatus is the grpc-status among a streamed direction's
// trailers, or nil when they have none.
func trailersGRPCStatus(trailers h2Fields) *int {
	for _, f := range trailers {
		if strings.EqualFold(f.Name, grpcStatusField) {
			return parseGRPCStatus(f.Value)
		}
	}
	return nil
}

// trailersFrameGRPCStatus is the grpc-status in a stream message with flags
// and its decoded payload when it is a gRPC-Web trailers frame, the trailer
// block a gRPC-Web response normally ends with inside its body. It is nil
// for any other message, or a block with no whole-number grpc-status.
func trailersFrameGRPCStatus(framing stream.Framing, flags byte, payload []byte) *int {
	if !stream.IsTrailersFrame(framing, flags) {
		return nil
	}
	fields, err := parseTrailerBlock(payload)
	if err != nil {
		return nil
	}
	return trailersGRPCStatus(fields)
}

// parseGRPCStatus reads a grpc-status value, nil when it is not a whole
// number.
func parseGRPCStatus(value string) *int {
	status, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return &status
}
