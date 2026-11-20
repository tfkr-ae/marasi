package service

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestStreamedPairEventStream(t *testing.T) {
	pairID := uuid.MustParse("019a0000-0000-7000-8000-000000000001")
	messageID := uuid.MustParse("019a0000-0000-7000-8000-000000000002")
	flags := uint8(0)
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	// announce publishes a gRPC call's events through the server's
	// handlers: started, a message of size bytes, progress and the end.
	announce := func(server *Server, size int) {
		server.HandleStreamedPairStarted(domain.StreamedPairStarted{PairID: pairID, Framings: map[string]string{"request": "grpc"}})
		server.HandleStreamMessageProgress(domain.StreamMessageProgress{PairID: pairID, Direction: "response", Framing: "grpc", Bytes: 4096})
		server.HandleStreamMessage(domain.StreamMessage{
			ID: messageID, PairID: pairID, Direction: "response", Framing: "grpc", Flags: &flags,
			Payload: bytes.Repeat([]byte("x"), size), CreatedAt: at, Metadata: map[string]any{},
		})
		server.HandleStreamedPairEnded(domain.StreamedPairEnded{PairID: pairID, Directions: map[string]domain.StreamedDirection{
			"request": {Framing: "grpc", Messages: 2, Ending: "complete"},
			"response": {Framing: "grpc", Messages: 1, Ending: "reset", ResetCode: "INTERNAL_ERROR",
				Trailers: []domain.StreamTrailer{{Name: "grpc-status", Value: "13"}}},
		}})
	}

	wantStarted := `{"pair_id":"` + pairID.String() + `","framings":{"request":"grpc"}}`
	wantProgress := `{"pair_id":"` + pairID.String() + `","direction":"response","framing":"grpc","bytes":4096}`
	wantEnded := `{"pair_id":"` + pairID.String() + `","directions":{"request":{"framing":"grpc","messages":2,"ending":"complete"},` +
		`"response":{"framing":"grpc","messages":1,"ending":"reset","reset_code":"INTERNAL_ERROR","trailers":[{"name":"grpc-status","value":"13"}]}}}`
	message := func(payload string, truncated bool) string {
		raw, _ := json.Marshal(truncated)
		return `{"id":"` + messageID.String() + `","pair_id":"` + pairID.String() + `","direction":"response","framing":"grpc","flags":0,` +
			`"payload":` + payload + `,"compressed":false,"created_at":"2026-11-20T10:00:00Z","metadata":{},"payload_truncated":` + string(raw) + `}`
	}

	for _, tc := range []struct {
		name        string
		size        int
		wantMessage string
	}{
		{"should send a 64 KiB payload in the event", 64 << 10, message(`"`+encodeX(64<<10)+`"`, false)},
		{"should leave out a payload over 64 KiB and mark it truncated", 64<<10 + 1, message("null", true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(nil, func() {})
			httpServer := httptest.NewServer(server)
			defer httpServer.Close()
			response, reader := connectEventStream(t, httpServer.URL)
			defer response.Body.Close()

			announce(server, tc.size)
			for _, want := range []streamFrame{
				{name: "streamed_pair.started", data: wantStarted},
				{name: "stream_message.progress", data: wantProgress},
				{name: "stream_message", data: tc.wantMessage},
				{name: "streamed_pair.ended", data: wantEnded},
			} {
				if got := readStreamFrame(t, reader); got.name != want.name || got.data != want.data {
					t.Fatalf("\nwanted:\n%s %s\ngot:\n%s %s", want.name, want.data, got.name, got.data)
				}
			}
		})
	}

	t.Run("should replay the events to a subscriber that resumes", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		server.events.publish("n", map[string]int{"n": 1})
		announce(server, 3)

		response, reader := connectResumedStream(t, httpServer.URL, eventID(server.RunID(), 1), "")
		defer response.Body.Close()
		for _, want := range []string{"streamed_pair.started", "stream_message.progress", "stream_message", "streamed_pair.ended"} {
			if got := readStreamFrame(t, reader); got.name != want || got.id == "" {
				t.Fatalf("\nwanted:\n%s with an id\ngot:\n%+v", want, got)
			}
		}
	})
}

// encodeX is n bytes of "x" in standard base64, as JSON carries []byte.
func encodeX(n int) string {
	raw, _ := json.Marshal(bytes.Repeat([]byte("x"), n))
	return string(raw[1 : len(raw)-1])
}
