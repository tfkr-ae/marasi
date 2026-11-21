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

// streamMessagePage is a page of GET /stream/{pair_id}/message.
type streamMessagePage struct {
	Items []struct {
		ID         uuid.UUID      `json:"id"`
		PairID     uuid.UUID      `json:"pair_id"`
		Direction  string         `json:"direction"`
		Framing    string         `json:"framing"`
		Flags      *int           `json:"flags"`
		Payload    []byte         `json:"payload"`
		Compressed bool           `json:"compressed"`
		CreatedAt  time.Time      `json:"created_at"`
		Metadata   map[string]any `json:"metadata"`
	} `json:"items"`
	NextCursor *uuid.UUID `json:"next_cursor"`
}

func TestStreamMessageRoutes(t *testing.T) {
	conn, err := db.New(t.TempDir()+"/stream.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	repo := db.NewProxyRepo(conn)
	server := newTestServer(&marasi.Proxy{TrafficRepo: repo, StreamRepo: repo}, func() {})

	pair, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	err = repo.InsertRequest(&domain.ProxyRequest{
		ID: pair, Scheme: "https", Method: "POST", Host: "grpc.example", Path: "/test.Echo/PingPong",
		Raw: []byte("POST /test.Echo/PingPong HTTP/2\r\ncontent-type: application/grpc\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for i, direction := range []string{domain.StreamDirectionRequest, domain.StreamDirectionResponse, domain.StreamDirectionRequest} {
		id, _ := uuid.NewV7()
		ids = append(ids, id)
		flags := uint8(i)
		err := repo.InsertStreamMessage(&domain.StreamMessage{
			ID: id, PairID: pair, Direction: direction, Framing: "grpc", Flags: &flags,
			Payload: []byte{byte('a' + i)}, CreatedAt: time.Now(), Metadata: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	path := "/stream/" + pair.String() + "/message"

	t.Run("should page a pair's stream messages in the order they arrived", func(t *testing.T) {
		var first, second streamMessagePage
		response := requestControlAPI(server, http.MethodGet, path+"?limit=2", "")
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &first) != nil {
			t.Fatalf("first page\nwanted:\n200 and a page\ngot:\n%d %s", response.Code, response.Body)
		}
		if len(first.Items) != 2 || first.NextCursor == nil || *first.NextCursor != ids[1] {
			t.Fatalf("first page\nwanted:\n2 items and the second's id as next_cursor\ngot:\n%s", response.Body)
		}
		response = requestControlAPI(server, http.MethodGet, path+"?limit=2&cursor="+first.NextCursor.String(), "")
		if json.Unmarshal(response.Body.Bytes(), &second) != nil || len(second.Items) != 1 || second.NextCursor != nil {
			t.Fatalf("second page\nwanted:\n1 item and no next_cursor\ngot:\n%s", response.Body)
		}
		got := []uuid.UUID{first.Items[0].ID, first.Items[1].ID, second.Items[0].ID}
		for i := range ids {
			if got[i] != ids[i] {
				t.Fatalf("order\nwanted:\n%v\ngot:\n%v", ids, got)
			}
		}
		item := first.Items[1]
		if item.PairID != pair || item.Direction != "response" || item.Framing != "grpc" || item.Flags == nil || *item.Flags != 1 || string(item.Payload) != "b" {
			t.Fatalf("second message\nwanted:\nresponse grpc flags 1 payload b\ngot:\n%+v", item)
		}
	})

	t.Run("should get one stream message of the pair", func(t *testing.T) {
		response := requestControlAPI(server, http.MethodGet, path+"/"+ids[2].String(), "")
		var item map[string]any
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &item) != nil || item["id"] != ids[2].String() || item["direction"] != "request" {
			t.Fatalf("\nwanted:\n200 and the third message\ngot:\n%d %s", response.Code, response.Body)
		}
	})

	t.Run("should answer not found for an unknown pair or message, or a message of another pair, and bad request for a bad ID or page", func(t *testing.T) {
		other, _ := uuid.NewV7()
		for _, test := range []struct {
			path   string
			status int
			body   string
		}{
			{"/stream/" + other.String() + "/message", http.StatusNotFound, "{\"error\":\"not_found\"}\n"},
			{path + "/" + other.String(), http.StatusNotFound, "{\"error\":\"not_found\"}\n"},
			{"/stream/" + other.String() + "/message/" + ids[0].String(), http.StatusNotFound, "{\"error\":\"not_found\"}\n"},
			{"/stream/not-a-uuid/message", http.StatusBadRequest, "{\"error\":\"bad_request\"}\n"},
			{path + "/not-a-uuid", http.StatusBadRequest, "{\"error\":\"bad_request\"}\n"},
			{path + "?limit=0", http.StatusBadRequest, "{\"error\":\"bad_request\"}\n"},
			{path + "?cursor=bad", http.StatusBadRequest, "{\"error\":\"bad_request\"}\n"},
		} {
			assertControlAPIResponse(t, requestControlAPI(server, http.MethodGet, test.path, ""), test.status, test.body)
		}
	})

	t.Run("should give a compressed message a text form only when its payload was decoded", func(t *testing.T) {
		flags := uint8(1)
		decoded, _ := uuid.NewV7()
		raw, _ := uuid.NewV7()
		failed, _ := uuid.NewV7()
		for _, m := range []domain.StreamMessage{
			{ID: decoded, Payload: []byte{0x08, 0x01}, Metadata: map[string]any{"encoding": "gzip"}},
			{ID: raw, Payload: []byte{0x08, 0x01}, Metadata: map[string]any{"encoding": "snappy"}},
			{ID: failed, Payload: []byte{0x08, 0x01}, Metadata: map[string]any{"encoding": "gzip", "inflate_error": "bad"}},
		} {
			m.PairID, m.Direction, m.Framing, m.Flags, m.Compressed, m.CreatedAt = pair, domain.StreamDirectionRequest, "grpc", &flags, true, time.Now()
			if err := repo.InsertStreamMessage(&m); err != nil {
				t.Fatal(err)
			}
		}
		for _, test := range []struct {
			id   uuid.UUID
			text any
		}{{decoded, "1:VARINT 1\n"}, {raw, nil}, {failed, nil}} {
			var item map[string]any
			response := requestControlAPI(server, http.MethodGet, path+"/"+test.id.String(), "")
			if json.Unmarshal(response.Body.Bytes(), &item) != nil || item["text"] != test.text {
				t.Errorf("the text form of %s\nwanted:\n%v\ngot:\n%s", test.id, test.text, response.Body)
			}
		}
	})
}
