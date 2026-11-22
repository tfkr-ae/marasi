package marasi

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
)

// storedStreamed is the pair's stored streamed state.
func storedStreamed(t *testing.T, proxy *Proxy, id uuid.UUID) *domain.StreamedState {
	t.Helper()
	if err := proxy.flushDBWrites(); err != nil {
		t.Fatal(err)
	}
	row, err := proxy.TrafficRepo.GetRequestResponseRow(id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Streamed
}

// listedIDs are the ids of the pairs query lists.
func listedIDs(t *testing.T, proxy *Proxy, query string) []uuid.UUID {
	t.Helper()
	page, err := proxy.TrafficRepo.ListTraffic(domain.TrafficQuery{Text: query, Limit: 50})
	if err != nil {
		t.Fatalf("ListTraffic(%q) returned unexpected error: %v", query, err)
	}
	ids := make([]uuid.UUID, len(page.Items))
	for i, item := range page.Items {
		ids[i] = item.ID
	}
	return ids
}

func directionState(d *domain.StreamedDirectionState) string {
	if d == nil {
		return "<nil>"
	}
	return d.Framing + "/" + d.Ending
}

func TestProxy_StreamedPairState(t *testing.T) {
	t.Run("should store a call's request framing while it is live, its response framing once its head is admitted and both endings once it ends, announcing it started then updated", func(t *testing.T) {
		started := make(chan domain.StreamedPairStarted, 4)
		updated := make(chan domain.StreamedPairUpdated, 4)
		call := startGRPCCall(t, func(p *Proxy) {
			p.OnStreamedPairStarted = func(e domain.StreamedPairStarted) { started <- e }
			p.OnStreamedPairUpdated = func(e domain.StreamedPairUpdated) { updated <- e }
		})
		var req domain.ProxyRequest
		select {
		case req = <-call.captured.request:
		case <-time.After(5 * time.Second):
			t.Fatal("the request was not captured")
		}
		select {
		case e := <-started:
			if e.PairID != req.ID || len(e.Framings) != 1 || e.Framings["request"] != "grpc" {
				t.Fatalf("streamed_pair.started\nwanted:\nthe request's grpc framing\ngot:\n%+v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the pair was not announced started")
		}

		state := storedStreamed(t, call.proxy, req.ID)
		if state == nil || directionState(state.Request) != "grpc/" || state.Response != nil {
			t.Fatalf("the live pair's streamed state\nwanted:\nrequest grpc, no ending, no response\ngot:\n%+v", state)
		}
		if ids := listedIDs(t, call.proxy, "streamed:*"); len(ids) != 1 || ids[0] != req.ID {
			t.Errorf("streamed:*\nwanted:\nthe live pair\ngot:\n%v", ids)
		}
		if ids := listedIDs(t, call.proxy, `response_framing = "grpc"`); len(ids) != 0 {
			t.Errorf(`response_framing = "grpc" before the response head\nwanted:\nnothing\ngot:\n%v`, ids)
		}

		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "ping 1"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping 1"))
		select {
		case e := <-updated:
			if e.PairID != req.ID || e.Framings["request"] != "grpc" || e.Framings["response"] != "grpc" {
				t.Fatalf("streamed_pair.updated\nwanted:\nboth directions grpc\ngot:\n%+v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the response direction was not announced")
		}
		state = storedStreamed(t, call.proxy, req.ID)
		if state == nil || directionState(state.Request) != "grpc/" || directionState(state.Response) != "grpc/" {
			t.Fatalf("the streamed state once the response head was admitted\nwanted:\nboth grpc, no endings\ngot:\n%+v", state)
		}
		if ids := listedIDs(t, call.proxy, `response_framing = "grpc" AND request_framing = "grpc"`); len(ids) != 1 {
			t.Errorf("both framings\nwanted:\nthe live pair\ngot:\n%v", ids)
		}

		if _, err := call.proxy.JoinedRequest(req.ID); !errors.Is(err, ErrStreamedRequestOpen) {
			t.Fatalf("JoinedRequest() while the request is open\nwanted:\nErrStreamedRequestOpen\ngot:\n%v", err)
		}
		call.client.data(1, true, grpcEnvelope(0, "pong 1"))
		call.upstream.stream(t, call.id)
		// The request ended while the response goes on: the request is
		// whole, so it joins.
		joined, err := call.proxy.JoinedRequest(req.ID)
		if err != nil {
			t.Fatalf("JoinedRequest() once the request ended returned unexpected error: %v", err)
		}
		if !joined.Streamed || joined.Messages != 1 || !strings.HasSuffix(string(joined.Raw), "\r\n\r\n"+grpcEnvelope(0, "pong 1")) {
			t.Fatalf("the joined request\nwanted:\nthe head and pong 1 in its envelope\ngot:\n%+v %q", joined, joined.Raw)
		}

		call.upstream.headers(call.id, true, "grpc-status", "0")
		var res domain.ProxyResponse
		select {
		case res = <-call.captured.response:
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}
		if res.Streamed == nil || directionState(res.Streamed.Request) != "grpc/complete" || directionState(res.Streamed.Response) != "grpc/complete" {
			t.Errorf("the announced response's streamed state\nwanted:\nboth complete\ngot:\n%+v", res.Streamed)
		}
		state = storedStreamed(t, call.proxy, req.ID)
		if state == nil || directionState(state.Request) != "grpc/complete" || directionState(state.Response) != "grpc/complete" {
			t.Fatalf("the ended pair's streamed state\nwanted:\nboth grpc, complete\ngot:\n%+v", state)
		}
	})

	t.Run("should record the endings of a call the client resets", func(t *testing.T) {
		call := startGRPCCall(t)
		call.upstream.headers(call.id, false, ":status", "200", "content-type", "application/grpc")
		call.upstream.data(call.id, false, grpcEnvelope(0, "ping"))
		awaitData(t, call.client, "the client", 1, grpcEnvelope(0, "ping"))
		call.client.write(func(f *http2.Framer) error { return f.WriteRSTStream(1, http2.ErrCodeCancel) })
		req, _ := streamedResponse(t, call.captured)
		state := storedStreamed(t, call.proxy, req.ID)
		if state == nil || directionState(state.Request) != "grpc/cancelled" || directionState(state.Response) != "grpc/cancelled" {
			t.Fatalf("the cancelled pair's streamed state\nwanted:\nboth cancelled\ngot:\n%+v", state)
		}
	})
}

func TestProxy_JoinedRequestReplays(t *testing.T) {
	t.Run("should join a streamed HTTP/2 launch's request messages into a body that launches the same call again", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		body := grpcEnvelope(0, "one") + grpcEnvelope(0, "two") + grpcEnvelope(0, "three")
		raw := "POST /test.Echo/Collect HTTP/2\r\n:authority: " + origin.addr + "\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\n" + body

		answer := func(upstream *h2TestPeer, n int) string {
			id := upstream.nthStream(t, n)
			request := upstream.stream(t, id)
			upstream.headers(id, false, ":status", "200", "content-type", "application/grpc")
			upstream.data(id, false, grpcEnvelope(0, "collected"))
			upstream.headers(id, true, "grpc-status", "0")
			return string(request.data)
		}
		done := launchH2(t, h, raw)
		upstream := origin.accept(t)
		answer(upstream, 0)
		first := waitLaunch(t, done)
		if first.Status != domain.LaunchCompleted {
			t.Fatalf("the launch\nwanted:\ncompleted\ngot:\n%+v", first)
		}

		joined, err := h.proxy.JoinedRequest(first.ID)
		if err != nil {
			t.Fatalf("JoinedRequest() returned unexpected error: %v", err)
		}
		head, joinedBody, _ := strings.Cut(string(joined.Raw), "\r\n\r\n")
		if !joined.Streamed || joined.Messages != 3 || joined.Omitted != 0 || joinedBody != body {
			t.Fatalf("the joined request\nwanted:\nthree messages, the launched body\ngot:\n%+v %q", joined, joinedBody)
		}
		if strings.Contains(strings.ToLower(head), "content-length") {
			t.Errorf("the joined HTTP/2 head\nwanted:\nno content-length added\ngot:\n%q", head)
		}

		again := launchH2(t, h, string(joined.Raw))
		replayed := answer(origin.accept(t), 0)
		if replayed != body {
			t.Fatalf("the replayed request body\nwanted:\n%q\ngot:\n%q", body, replayed)
		}
		if launch := waitLaunch(t, again); launch.Status != domain.LaunchCompleted {
			t.Fatalf("the replay\nwanted:\ncompleted\ngot:\n%+v", launch)
		}
	})

	t.Run("should join a gRPC-Web launch over HTTP/1.1 with a Content-Length of the joined body", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{})
		launchpad, err := h.repository.CreateLaunchpad("grpc-web", "")
		if err != nil {
			t.Fatal(err)
		}
		body := grpcEnvelope(0, "one") + grpcEnvelope(0, "two")
		raw := "POST /test.Echo/Collect HTTP/1.1\r\nHost: " + origin.addr + "\r\nContent-Type: application/grpc-web+proto\r\n\r\n" + body
		done := make(chan domain.Launch, 1)
		go func() {
			launch, _ := h.proxy.LaunchSync(t.Context(), raw, launchpad.String(), false)
			done <- launch
		}()
		upstream := origin.accept(t)
		io.ReadFull(upstream.br, make([]byte, len(body)))
		io.WriteString(upstream, "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"+
			chunk(grpcEnvelope(0x80, "grpc-status: 0\r\n"))+"0\r\n\r\n")
		launch := waitLaunch(t, done)
		if launch.Status != domain.LaunchCompleted {
			t.Fatalf("the launch\nwanted:\ncompleted\ngot:\n%+v", launch)
		}

		joined, err := h.proxy.JoinedRequest(launch.ID)
		if err != nil {
			t.Fatalf("JoinedRequest() returned unexpected error: %v", err)
		}
		head, err := readHead(joined.Raw)
		if err != nil {
			t.Fatal(err)
		}
		_, joinedBody, _ := strings.Cut(string(joined.Raw), "\r\n\r\n")
		if joinedBody != body || head.Get("Content-Length") != strconv.Itoa(len(body)) || head.Get("Transfer-Encoding") != "" {
			t.Fatalf("the joined HTTP/1.1 request\nwanted:\nthe body with Content-Length %d\ngot:\n%q", len(body), joined.Raw)
		}
	})

	t.Run("should return a pair that is not streamed as stored", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		id := uuid.Must(uuid.NewV7())
		raw := "POST /form HTTP/1.1\r\nHost: example.com\r\nContent-Length: 3\r\n\r\na=1"
		if err := h.repository.InsertRequest(&domain.ProxyRequest{ID: id, Scheme: "http", Method: "POST", Host: "example.com", Path: "/form", Raw: []byte(raw), RequestedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		joined, err := h.proxy.JoinedRequest(id)
		if err != nil || joined.Streamed || string(joined.Raw) != raw {
			t.Fatalf("JoinedRequest()\nwanted:\nthe raw request as stored\ngot:\n%+v %q %v", joined, joined.Raw, err)
		}
	})

	t.Run("should report a pair that is not stored as not found", func(t *testing.T) {
		h := startViewHarness(t, viewHarnessConfig{})
		if _, err := h.proxy.JoinedRequest(uuid.Must(uuid.NewV7())); !errors.Is(err, domain.ErrPairNotFound) {
			t.Fatalf("JoinedRequest()\nwanted:\nErrPairNotFound\ngot:\n%v", err)
		}
	})
}

// readHead reads the HTTP/1.1 head of raw.
func readHead(raw []byte) (textprotoHeader, error) {
	br := bufio.NewReader(strings.NewReader(string(raw)))
	if _, err := br.ReadString('\n'); err != nil {
		return nil, err
	}
	header := textprotoHeader{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return header, nil
		}
		name, value, _ := strings.Cut(line, ":")
		header[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
}

// textprotoHeader is a head's fields by lowercased name.
type textprotoHeader map[string]string

func (h textprotoHeader) Get(name string) string { return h[strings.ToLower(name)] }
