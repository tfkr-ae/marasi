package service

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// envelope is payload in a gRPC envelope with flags.
func envelope(flags byte, payload string) string {
	var prefix [5]byte
	prefix[0] = flags
	binary.BigEndian.PutUint32(prefix[1:], uint32(len(payload)))
	return string(prefix[:]) + payload
}

func TestStreamedPairState(t *testing.T) {
	conn, err := db.New(t.TempDir()+"/streamed.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	repo := db.NewProxyRepo(conn)
	server := newTestServer(&marasi.Proxy{TrafficRepo: repo, StreamRepo: repo}, func() {})

	insert := func(raw string) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		if err := repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "POST", Host: "grpc.example", Path: "/test.Echo/Collect",
			Raw: []byte(raw), Metadata: map[string]any{}, RequestedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	head := "POST /test.Echo/Collect HTTP/2\r\n:authority: grpc.example\r\ncontent-type: application/grpc\r\n\r\n"
	call := insert(head)
	if err := repo.RecordStreamedPair(&domain.StreamedPairRecord{PairID: call, State: domain.StreamedState{
		Request:  &domain.StreamedDirectionState{Framing: "grpc", Ending: "complete"},
		Response: &domain.StreamedDirectionState{Framing: "grpc"},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		direction string
		payload   string
		metadata  map[string]any
	}{
		{"request", "one", nil},
		{"response", "reply", nil},
		{"request", "gone", map[string]any{"dropped": true}},
		{"request", "two", map[string]any{"injected": true}},
	} {
		flags := uint8(0)
		metadata := m.metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		if err := repo.InsertStreamMessage(&domain.StreamMessage{
			ID: uuid.Must(uuid.NewV7()), PairID: call, Direction: m.direction, Framing: "grpc", Flags: &flags,
			Payload: []byte(m.payload), CreatedAt: time.Now(), Metadata: metadata,
		}); err != nil {
			t.Fatal(err)
		}
	}
	plainRaw := "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"
	plain := insert(plainRaw)

	t.Run("should list a streamed pair with its streamed state and any other pair with null", func(t *testing.T) {
		response := requestControlAPI(server, http.MethodGet, "/traffic", "")
		var page struct {
			Items []struct {
				ID       uuid.UUID       `json:"id"`
				Streamed json.RawMessage `json:"streamed"`
			} `json:"items"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Items) != 2 {
			t.Fatalf("GET /traffic\nwanted:\n200 with two pairs\ngot:\n%d %s", response.Code, response.Body)
		}
		want := map[uuid.UUID]string{
			call:  `{"request":{"framing":"grpc","ending":"complete"},"response":{"framing":"grpc","ending":null}}`,
			plain: `null`,
		}
		for _, item := range page.Items {
			if string(item.Streamed) != want[item.ID] {
				t.Errorf("streamed of %s\nwanted:\n%s\ngot:\n%s", item.ID, want[item.ID], item.Streamed)
			}
		}
	})

	t.Run("should filter the list by streamed:* and by framing", func(t *testing.T) {
		for query, want := range map[string]int{`streamed:*`: 1, `NOT streamed:*`: 1, `response_framing = "grpc"`: 1, `request_framing = "connect"`: 0} {
			response := requestControlAPI(server, http.MethodGet, "/traffic/count?q="+strings.ReplaceAll(query, " ", "+"), "")
			var count struct {
				Count int `json:"count"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &count) != nil || count.Count != want {
				t.Errorf("count of %s\nwanted:\n%d\ngot:\n%d %s", query, want, response.Code, response.Body)
			}
		}
	})

	t.Run("should join a streamed pair's forwarded request messages into its raw request", func(t *testing.T) {
		response := requestControlAPI(server, http.MethodGet, "/traffic/"+call.String()+"/request/joined", "")
		var joined struct {
			ID       uuid.UUID `json:"id"`
			Raw      []byte    `json:"raw"`
			Streamed bool      `json:"streamed"`
			Messages int       `json:"messages"`
			Omitted  int       `json:"omitted"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &joined) != nil {
			t.Fatalf("GET /traffic/{id}/request/joined\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
		want := head + envelope(0, "one") + envelope(0, "two")
		if joined.ID != call || !joined.Streamed || joined.Messages != 2 || joined.Omitted != 1 || string(joined.Raw) != want {
			t.Fatalf("the joined request\nwanted:\n%q, 2 messages, 1 omitted\ngot:\n%s", want, response.Body)
		}
	})

	t.Run("should return a pair that is not streamed as stored", func(t *testing.T) {
		response := requestControlAPI(server, http.MethodGet, "/traffic/"+plain.String()+"/request/joined", "")
		var joined struct {
			Raw      []byte `json:"raw"`
			Streamed bool   `json:"streamed"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &joined) != nil || joined.Streamed || string(joined.Raw) != plainRaw {
			t.Fatalf("GET /traffic/{id}/request/joined\nwanted:\n200 with the raw request as stored\ngot:\n%d %s", response.Code, response.Body)
		}
	})

	t.Run("should answer not_found for an unknown pair and bad_request for a malformed id", func(t *testing.T) {
		if response := requestControlAPI(server, http.MethodGet, "/traffic/"+uuid.Must(uuid.NewV7()).String()+"/request/joined", ""); response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"not_found"`) {
			t.Errorf("an unknown pair\nwanted:\n404 not_found\ngot:\n%d %s", response.Code, response.Body)
		}
		if response := requestControlAPI(server, http.MethodGet, "/traffic/nope/request/joined", ""); response.Code != http.StatusBadRequest {
			t.Errorf("a malformed id\nwanted:\n400\ngot:\n%d %s", response.Code, response.Body)
		}
	})
}

func TestStreamedPairUpdatedEvent(t *testing.T) {
	t.Run("should publish streamed_pair.updated with every direction known so far, and traffic.response with a streamed pair's final state", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()
		pairID := uuid.MustParse("019a0000-0000-7000-8000-000000000001")

		server.HandleStreamedPairUpdated(domain.StreamedPairUpdated{PairID: pairID, Framings: map[string]string{"request": "grpc", "response": "grpc"}})
		server.events.publishResponse(domain.ProxyResponse{ID: pairID, StatusCode: 200, Streamed: &domain.StreamedState{
			Request:  &domain.StreamedDirectionState{Framing: "grpc", Ending: "complete"},
			Response: &domain.StreamedDirectionState{Framing: "grpc", Ending: "cut short"},
		}})

		updated := readStreamFrame(t, reader)
		wantUpdated := `{"pair_id":"` + pairID.String() + `","framings":{"request":"grpc","response":"grpc"},` +
			`"streamed":{"request":{"framing":"grpc","ending":null},"response":{"framing":"grpc","ending":null}}}`
		if updated.name != "streamed_pair.updated" || updated.data != wantUpdated {
			t.Fatalf("\nwanted:\nstreamed_pair.updated %s\ngot:\n%s %s", wantUpdated, updated.name, updated.data)
		}
		res := readStreamFrame(t, reader)
		wantStreamed := `"streamed":{"request":{"framing":"grpc","ending":"complete"},"response":{"framing":"grpc","ending":"cut short"}}`
		if res.name != "traffic.response" || !strings.Contains(res.data, wantStreamed) {
			t.Fatalf("\nwanted:\ntraffic.response with %s\ngot:\n%s %s", wantStreamed, res.name, res.data)
		}
	})

	t.Run("should leave streamed out of traffic.response for a pair that is not streamed", func(t *testing.T) {
		server := newTestServer(nil, func() {})
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		response, reader := connectEventStream(t, httpServer.URL)
		defer response.Body.Close()

		server.events.publishResponse(domain.ProxyResponse{ID: uuid.Must(uuid.NewV7()), StatusCode: 200})
		if res := readStreamFrame(t, reader); res.name != "traffic.response" || strings.Contains(res.data, `"streamed"`) {
			t.Fatalf("\nwanted:\ntraffic.response without streamed\ngot:\n%s %s", res.name, res.data)
		}
	})
}
