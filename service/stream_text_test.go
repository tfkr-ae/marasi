package service

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestStreamMessageTextForm(t *testing.T) {
	conn, err := db.New(t.TempDir()+"/stream.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	repo := db.NewProxyRepo(conn)
	server := newTestServer(&marasi.Proxy{TrafficRepo: repo, StreamRepo: repo}, func() {})

	// insertPair stores a pair whose request head names requestType and,
	// when responseType is not empty, a response naming it.
	insertPair := func(t *testing.T, requestType, responseType string) uuid.UUID {
		t.Helper()
		id, _ := uuid.NewV7()
		err := repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "POST", Host: "grpc.example", Path: "/test.Echo/PingPong",
			Raw: []byte("POST /test.Echo/PingPong HTTP/2\r\nhost: grpc.example\r\ncontent-type: " + requestType + "\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if responseType != "" {
			err := repo.InsertResponse(&domain.ProxyResponse{
				ID: id, Status: "200 OK", StatusCode: 200, ContentType: responseType,
				Raw: []byte("HTTP/2 200 OK\r\ncontent-type: " + responseType + "\r\n\r\n"), Metadata: map[string]any{}, RespondedAt: time.Now(),
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	insertMessage := func(t *testing.T, pair uuid.UUID, direction, framing string, payload []byte) uuid.UUID {
		t.Helper()
		id, _ := uuid.NewV7()
		err := repo.InsertStreamMessage(&domain.StreamMessage{
			ID: id, PairID: pair, Direction: direction, Framing: framing, Payload: payload, CreatedAt: time.Now(), Metadata: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	getText := func(t *testing.T, pair, id uuid.UUID) *string {
		t.Helper()
		response := requestControlAPI(server, http.MethodGet, "/stream/"+pair.String()+"/message/"+id.String(), "")
		var item struct {
			Payload []byte  `json:"payload"`
			Text    *string `json:"text"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &item) != nil {
			t.Fatalf("\nwanted:\n200 and a message\ngot:\n%d %s", response.Code, response.Body)
		}
		return item.Text
	}
	assertText := func(t *testing.T, want string, got *string) {
		t.Helper()
		if got == nil || *got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%v", want, got)
		}
	}

	grpc := insertPair(t, "application/grpc", "")

	t.Run("should give a protobuf message's text form next to its payload", func(t *testing.T) {
		id := insertMessage(t, grpc, domain.StreamDirectionRequest, "grpc", []byte{0x08, 0x96, 0x01})
		assertText(t, "1:VARINT 150\n", getText(t, grpc, id))
	})

	t.Run("should show a payload that is not protobuf as bytes, not an error", func(t *testing.T) {
		id := insertMessage(t, grpc, domain.StreamDirectionRequest, "grpc", []byte{0x0a, 0x05, 'a'})
		assertText(t, "`0a0561`\n", getText(t, grpc, id))
	})

	t.Run("should read a response message by the request's codec while the response is not stored", func(t *testing.T) {
		id := insertMessage(t, grpc, domain.StreamDirectionResponse, "grpc", []byte{0x08, 0x01})
		assertText(t, "1:VARINT 1\n", getText(t, grpc, id))
	})

	t.Run("should read a response message by the response's codec", func(t *testing.T) {
		pair := insertPair(t, "application/grpc+json", "application/grpc+json")
		id := insertMessage(t, pair, domain.StreamDirectionResponse, "grpc", []byte(`{"a":1}`))
		assertText(t, `{"a":1}`, getText(t, pair, id))
	})

	t.Run("should give a null text form for a message with none", func(t *testing.T) {
		pair := insertPair(t, "application/json", "text/event-stream")
		id := insertMessage(t, pair, domain.StreamDirectionResponse, "sse", []byte("data: x\n\n"))
		if got := getText(t, pair, id); got != nil {
			t.Fatalf("\nwanted:\nnull\ngot:\n%q", *got)
		}
	})
}
