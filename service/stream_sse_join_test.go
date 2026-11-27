package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestJoinedSSEResponseRoute(t *testing.T) {
	conn, err := db.New(t.TempDir()+"/sse-join.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	repo := db.NewProxyRepo(conn)
	server := newTestServer(&marasi.Proxy{TrafficRepo: repo, StreamRepo: repo}, func() {})

	insertPair := func(reqRaw, resRaw, contentType string, streamed *domain.StreamedState) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		if err := repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "GET", Host: "sse.example", Path: "/events",
			Raw: []byte(reqRaw), Metadata: map[string]any{}, RequestedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if streamed != nil {
			if err := repo.RecordStreamedPair(&domain.StreamedPairRecord{PairID: id, State: *streamed}); err != nil {
				t.Fatal(err)
			}
		}
		if resRaw != "" {
			if err := repo.InsertResponse(&domain.ProxyResponse{
				ID: id, Status: "200 OK", StatusCode: 200, ContentType: contentType,
				Raw: []byte(resRaw), Metadata: map[string]any{}, RespondedAt: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}

	t.Run("should stream an ended SSE response across page boundaries preserving delimiters and filtering non-forwarded messages", func(t *testing.T) {
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n"
		pairID := insertPair(
			"GET /events HTTP/1.1\r\nHost: sse.example\r\n\r\n",
			head,
			"text/event-stream",
			&domain.StreamedState{
				Request:  &domain.StreamedDirectionState{Framing: "grpc", Ending: "complete"},
				Response: &domain.StreamedDirectionState{Framing: "sse", Ending: "complete"},
			},
		)

		// Include request-direction message and all non-forwarded metadata variants
		// alongside >500 forwarded SSE events to span multiple 500-row pages.
		var want bytes.Buffer
		want.WriteString(head)
		insertMsg := func(direction, framing, payload string, compressed bool, meta map[string]any) {
			t.Helper()
			if meta == nil {
				meta = map[string]any{}
			}
			if err := repo.InsertStreamMessage(&domain.StreamMessage{
				ID: uuid.Must(uuid.NewV7()), PairID: pairID, Direction: direction, Framing: framing,
				Payload: []byte(payload), Compressed: compressed, CreatedAt: time.Now(), Metadata: meta,
			}); err != nil {
				t.Fatal(err)
			}
		}

		insertMsg(domain.StreamDirectionRequest, "grpc", "ignored-req", false, nil)
		for i := range 520 {
			var payload string
			switch i % 3 {
			case 0:
				payload = fmt.Sprintf("id: %d\ndata: lf-%d\n\n", i, i)
			case 1:
				payload = fmt.Sprintf("event: tick\r\ndata: crlf-%d\r\n\r\n", i)
			default:
				payload = fmt.Sprintf("data: cr-%d\r\r", i)
			}
			meta := map[string]any{}
			if i == 10 {
				meta["edited"] = true
			}
			if i == 20 {
				meta["injected"] = true
			}
			insertMsg(domain.StreamDirectionResponse, "sse", payload, false, meta)
			want.WriteString(payload)

			switch i {
			case 5:
				insertMsg(domain.StreamDirectionResponse, "sse", "data: dropped\n\n", false, map[string]any{"dropped": true})
			case 15:
				insertMsg(domain.StreamDirectionResponse, "sse", "", false, map[string]any{"too_large": true, "size_seen": 999999})
			case 25:
				insertMsg(domain.StreamDirectionResponse, "sse", "\xff\xfe", true, map[string]any{"encoding": "gzip", "inflate_error": "corrupt"})
			case 519:
				insertMsg(domain.StreamDirectionResponse, "sse", "data: partial", false, map[string]any{"cut_short": true})
			}
		}

		response := requestControlAPI(server, http.MethodGet, "/traffic/"+pairID.String()+"/response/joined", "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET /traffic/{id}/response/joined\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body.String())
		}
		if ct := response.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("Content-Type\nwanted:\napplication/octet-stream\ngot:\n%q", ct)
		}
		if response.Body.String() != want.String() {
			t.Fatalf("joined SSE body mismatch\nwanted:\n%d bytes\ngot:\n%d bytes", want.Len(), response.Body.Len())
		}

		// The ordinary GET /traffic/{id} read still returns just the stored head.
		detailResp := requestControlAPI(server, http.MethodGet, "/traffic/"+pairID.String(), "")
		var detail struct {
			Response struct {
				Raw []byte `json:"raw"`
			} `json:"response"`
		}
		if detailResp.Code != http.StatusOK || json.Unmarshal(detailResp.Body.Bytes(), &detail) != nil {
			t.Fatalf("GET /traffic/{id}\nwanted:\n200\ngot:\n%d %s", detailResp.Code, detailResp.Body.String())
		}
		if string(detail.Response.Raw) != head {
			t.Fatalf("GET /traffic/{id} response.raw\nwanted:\n%q\ngot:\n%q", head, string(detail.Response.Raw))
		}
	})

	t.Run("should return just the head for an ended SSE response with zero events", func(t *testing.T) {
		const head = "HTTP/2 200\r\ncontent-type: text/event-stream\r\n\r\n"
		pairID := insertPair(
			"GET /empty HTTP/2\r\n:authority: sse.example\r\n\r\n",
			head,
			"text/event-stream",
			&domain.StreamedState{
				Response: &domain.StreamedDirectionState{Framing: "sse", Ending: "complete"},
			},
		)
		response := requestControlAPI(server, http.MethodGet, "/traffic/"+pairID.String()+"/response/joined", "")
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/octet-stream" || response.Body.String() != head {
			t.Fatalf("GET /traffic/{id}/response/joined for zero events\nwanted:\n200 application/octet-stream with %q\ngot:\n%d %q %q", head, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	})

	t.Run("should return stream_response_open when the SSE response has no ending", func(t *testing.T) {
		pairID := insertPair(
			"GET /live HTTP/2\r\n:authority: sse.example\r\n\r\n",
			"",
			"",
			&domain.StreamedState{
				Response: &domain.StreamedDirectionState{Framing: "sse"},
			},
		)
		response := requestControlAPI(server, http.MethodGet, "/traffic/"+pairID.String()+"/response/joined", "")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"stream_response_open"`) {
			t.Fatalf("open SSE response\nwanted:\n409 stream_response_open\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("should return response_not_sse for pairs without an SSE response", func(t *testing.T) {
		cases := []struct {
			name   string
			pairID uuid.UUID
		}{
			{
				name:   "ordinary pair",
				pairID: insertPair("GET /plain HTTP/1.1\r\nHost: sse.example\r\n\r\n", "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nok", "text/plain", nil),
			},
			{
				name:   "pair without a response yet",
				pairID: insertPair("GET /pending HTTP/1.1\r\nHost: sse.example\r\n\r\n", "", "", nil),
			},
			{
				name: "gRPC response",
				pairID: insertPair("POST /svc/M HTTP/2\r\n:authority: sse.example\r\n\r\n", "HTTP/2 200\r\ncontent-type: application/grpc\r\n\r\n", "application/grpc", &domain.StreamedState{
					Response: &domain.StreamedDirectionState{Framing: "grpc", Ending: "complete"},
				}),
			},
			{
				name: "NDJSON response",
				pairID: insertPair("GET /logs HTTP/1.1\r\nHost: sse.example\r\n\r\n", "HTTP/1.1 200 OK\r\nContent-Type: application/x-ndjson\r\n\r\n", "application/x-ndjson", &domain.StreamedState{
					Response: &domain.StreamedDirectionState{Framing: "ndjson", Ending: "complete"},
				}),
			},
			{
				name: "ended SSE with no stored response head",
				pairID: insertPair("GET /dropped HTTP/1.1\r\nHost: sse.example\r\n\r\n", "", "", &domain.StreamedState{
					Response: &domain.StreamedDirectionState{Framing: "sse", Ending: "cancelled"},
				}),
			},
		}
		for _, c := range cases {
			t.Run("should refuse "+c.name, func(t *testing.T) {
				response := requestControlAPI(server, http.MethodGet, "/traffic/"+c.pairID.String()+"/response/joined", "")
				if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"response_not_sse"`) {
					t.Fatalf("GET /traffic/{id}/response/joined (%s)\nwanted:\n404 response_not_sse\ngot:\n%d %s", c.name, response.Code, response.Body.String())
				}
			})
		}
	})

	t.Run("should return bad_request for an invalid pair id and not_found for a missing pair or unavailable project", func(t *testing.T) {
		if response := requestControlAPI(server, http.MethodGet, "/traffic/not-a-uuid/response/joined", ""); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"bad_request"`) {
			t.Errorf("invalid id\nwanted:\n400 bad_request\ngot:\n%d %s", response.Code, response.Body.String())
		}
		if response := requestControlAPI(server, http.MethodGet, "/traffic/"+uuid.Must(uuid.NewV7()).String()+"/response/joined", ""); response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"not_found"`) {
			t.Errorf("unknown pair\nwanted:\n404 not_found\ngot:\n%d %s", response.Code, response.Body.String())
		}
		noProject := newTestServer(&marasi.Proxy{}, func() {})
		if response := requestControlAPI(noProject, http.MethodGet, "/traffic/"+uuid.Must(uuid.NewV7()).String()+"/response/joined", ""); response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"not_found"`) {
			t.Errorf("unavailable project\nwanted:\n404 not_found\ngot:\n%d %s", response.Code, response.Body.String())
		}
	})
}

func TestJoinedSSEResponseRoute_LiveAndProjectSwitch(t *testing.T) {
	t.Run("should return 409 while an SSE response is live over the proxy and 200 with the final event once it ends", func(t *testing.T) {
		sendSecond := make(chan struct{})
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusOK)
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte("id: 1\ndata: first\n\n"))
			_ = gz.Flush()
			w.(http.Flusher).Flush()
			<-sendSecond
			_, _ = gz.Write([]byte("id: 2\ndata: final\n\n"))
			_ = gz.Close()
			w.(http.Flusher).Flush()
		}))
		t.Cleanup(origin.Close)

		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		first := canonicalProjectPath(t, filepath.Join(dir, "first.marasi"))
		second := canonicalProjectPath(t, filepath.Join(dir, "second.marasi"))
		server := NewServer(proxy, &statusListener{status: ListenerStatus{Status: ListenerInactive}}, lifecycle, func() {}, "test", "work", first)
		if err := lifecycle.Open(context.Background(), first); err != nil {
			t.Fatalf("opening first project: %v", err)
		}
		started := make(chan uuid.UUID, 1)
		ended := make(chan uuid.UUID, 1)
		if err := proxy.WithOptions(
			marasi.WithStreamedPairStartedHandler(func(e domain.StreamedPairStarted) { started <- e.PairID }),
			marasi.WithStreamedPairEndedHandler(func(e domain.StreamedPairEnded) { ended <- e.PairID }),
			marasi.WithBasePipeline(),
			marasi.WithDefaultModifierPipeline(),
		); err != nil {
			t.Fatalf("installing pipeline: %v", err)
		}
		listener, err := proxy.GetListener("127.0.0.1", "0")
		if err != nil {
			t.Fatalf("listening: %v", err)
		}
		served := make(chan error, 1)
		go func() { served <- proxy.Serve(listener) }()
		t.Cleanup(func() {
			_ = proxy.CloseTransport()
			<-served
		})

		proxyURL, err := url.Parse("http://" + listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableCompression: true}}

		resCh := make(chan *http.Response, 1)
		go func() {
			resp, err := client.Get(origin.URL + "/events")
			if err != nil {
				t.Errorf("SSE request: %v", err)
				close(resCh)
				return
			}
			resCh <- resp
		}()

		var pairID uuid.UUID
		select {
		case pairID = <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("SSE pair did not start")
		}
		resp := <-resCh
		if resp == nil {
			t.FailNow()
		}
		defer resp.Body.Close()

		// While live, GET /traffic/{id}/response/joined must return 409 stream_response_open.
		liveResp := requestControlAPI(server, http.MethodGet, "/traffic/"+pairID.String()+"/response/joined", "")
		if liveResp.Code != http.StatusConflict || !strings.Contains(liveResp.Body.String(), `"stream_response_open"`) {
			t.Fatalf("live GET /traffic/{id}/response/joined\nwanted:\n409 stream_response_open\ngot:\n%d %s", liveResp.Code, liveResp.Body.String())
		}

		// Finish the SSE stream and drain the client response.
		close(sendSecond)
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatalf("reading SSE client body: %v", err)
		}
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatal("SSE pair did not end")
		}

		// Immediately after end, GET /traffic/{id}/response/joined returns 200 with head and both decoded events.
		doneResp := requestControlAPI(server, http.MethodGet, "/traffic/"+pairID.String()+"/response/joined", "")
		if doneResp.Code != http.StatusOK || doneResp.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("ended GET /traffic/{id}/response/joined\nwanted:\n200 application/octet-stream\ngot:\n%d %q %s", doneResp.Code, doneResp.Header().Get("Content-Type"), doneResp.Body.String())
		}
		got := doneResp.Body.String()
		head, body, ok := strings.Cut(got, "\r\n\r\n")
		if !ok {
			t.Fatalf("joined SSE response\nwanted:\nhead and blank line\ngot:\n%q", got)
		}
		if !strings.HasPrefix(head, "HTTP/1.1 200 OK\r\n") || !strings.Contains(head, "Content-Encoding: gzip") || !strings.Contains(head, "Content-Type: text/event-stream") {
			t.Fatalf("embedded head\nwanted:\nstored status line with Content-Encoding: gzip and Content-Type: text/event-stream\ngot:\n%q", head)
		}
		const wantBody = "id: 1\ndata: first\n\nid: 2\ndata: final\n\n"
		if body != wantBody {
			t.Fatalf("joined SSE body\nwanted:\n%q\ngot:\n%q", wantBody, body)
		}

		// Switching to another project makes the old pair unavailable (404 not_found).
		if err := lifecycle.Open(context.Background(), second); err != nil {
			t.Fatalf("opening second project: %v", err)
		}
		switchedResp := requestControlAPI(server, http.MethodGet, "/traffic/"+pairID.String()+"/response/joined", "")
		if switchedResp.Code != http.StatusNotFound || !strings.Contains(switchedResp.Body.String(), `"not_found"`) {
			t.Fatalf("GET after project switch\nwanted:\n404 not_found\ngot:\n%d %s", switchedResp.Code, switchedResp.Body.String())
		}
	})
}
