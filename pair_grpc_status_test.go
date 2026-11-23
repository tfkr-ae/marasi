package marasi

import (
	"strconv"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// grpcStatusString is a stored response's grpc-status as text, "none" when
// it has none.
func grpcStatusString(res domain.ProxyResponse) string {
	if res.GRPCStatus == nil {
		return "none"
	}
	return strconv.Itoa(*res.GRPCStatus)
}

func TestPairGRPCStatus(t *testing.T) {
	t.Run("should record the grpc-status of a gRPC call's trailers", func(t *testing.T) {
		call := startGRPCCall(t)
		call.upstream.await(t, "the request head", func(p *h2TestPeer) bool { return len(p.streams[call.id].headers) == 1 })
		call.client.data(1, true, grpcEnvelope(0, "ping"))
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "pong"))
		call.upstream.headers(call.id, true, "grpc-status", "3", "grpc-message", "bad")

		_, res := streamedResponse(t, call.captured)
		if got := grpcStatusString(res); got != "3" {
			t.Fatalf("the stored grpc-status\nwanted:\n3\ngot:\n%s", got)
		}
	})

	t.Run("should record the grpc-status of a trailers-only gRPC response", func(t *testing.T) {
		call := startGRPCCall(t)
		call.upstream.await(t, "the request head", func(p *h2TestPeer) bool { return len(p.streams[call.id].headers) == 1 })
		call.client.data(1, true, grpcEnvelope(0, "ping"))
		call.upstream.headers(call.id, true, ":status", "200", "content-type", "application/grpc", "grpc-status", "7", "grpc-message", "denied")

		_, res := streamedResponse(t, call.captured)
		if got := grpcStatusString(res); got != "7" {
			t.Fatalf("the stored grpc-status\nwanted:\n7\ngot:\n%s", got)
		}
	})

	t.Run("should record no grpc-status for a response that is not gRPC", func(t *testing.T) {
		call := startGRPCCall(t)
		call.upstream.await(t, "the request head", func(p *h2TestPeer) bool { return len(p.streams[call.id].headers) == 1 })
		call.client.data(1, true, grpcEnvelope(0, "ping"))
		call.upstream.headers(call.id, true, ":status", "200", "content-type", "text/plain", "grpc-status", "7")

		_, res := streamedResponse(t, call.captured)
		if got := grpcStatusString(res); got != "none" {
			t.Fatalf("the stored grpc-status\nwanted:\nnone\ngot:\n%s", got)
		}
	})
}
