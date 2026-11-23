package marasi

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/wordlist"
)

// launchH2 launches raw over HTTPS through h and returns the finished launch.
func launchH2(t *testing.T, h *viewHarness, raw string) <-chan domain.Launch {
	t.Helper()
	launchpad, err := h.repository.CreateLaunchpad("h2", "")
	if err != nil {
		t.Fatalf("creating launchpad: %v", err)
	}
	done := make(chan domain.Launch, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		launch, err := h.proxy.LaunchSync(ctx, raw, launchpad.String(), true)
		if err != nil {
			t.Errorf("LaunchSync() returned unexpected error: %v", err)
		}
		done <- launch
	}()
	return done
}

func waitLaunch(t *testing.T, done <-chan domain.Launch) domain.Launch {
	t.Helper()
	select {
	case launch := <-done:
		return launch
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the launch")
		return domain.Launch{}
	}
}

func TestProxy_LaunchOverH2(t *testing.T) {
	t.Run("should send an HTTP/2 launch to the origin over HTTP/2 with its fields in the order typed", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		raw := "POST /submit?a=1 HTTP/2\r\n:scheme: https\r\n:authority: " + origin.addr + "\r\nzz-First: 1\r\nUser-Agent: launch\r\naccept: */*\r\n\r\nbody"

		done := launchH2(t, h, raw)
		upstream := origin.accept(t)
		id := upstream.nthStream(t, 0)
		request := upstream.stream(t, id)
		upstream.headers(id, false, ":status", "200", "content-type", "text/plain")
		upstream.data(id, true, "ok")

		want := h2Fields{
			{":method", "POST", false}, {":path", "/submit?a=1", false},
			{":scheme", "https", false}, {":authority", origin.addr, false},
			{"zz-First", "1", false}, {"User-Agent", "launch", false}, {"accept", "*/*", false},
		}
		if len(request.headers) != 1 || !equalFields(request.headers[0], want) || string(request.data) != "body" {
			t.Fatalf("the origin's request\nwanted:\n%v %q\ngot:\n%v %q", want, "body", request.headers, request.data)
		}
		if launch := waitLaunch(t, done); launch.Status != domain.LaunchCompleted {
			t.Fatalf("the launch\nwanted:\ncompleted\ngot:\n%+v", launch)
		}
		stored := capturedRequest(t, h)
		wantRaw := "POST /submit?a=1 HTTP/2\r\n:scheme: https\r\n:authority: " + origin.addr + "\r\nzz-First: 1\r\nUser-Agent: launch\r\naccept: */*\r\n\r\nbody"
		if string(stored.Raw) != wantRaw || stored.ClientProtocol != domain.ProtocolHTTP2 || stored.OriginProtocol != domain.ProtocolHTTP2 {
			t.Fatalf("the stored request\nwanted:\n%q h2->h2\ngot:\n%q %s->%s", wantRaw, stored.Raw, stored.ClientProtocol, stored.OriginProtocol)
		}
	})

	t.Run("should send a plain-HTTP launch written as HTTP/2 to the origin by prior knowledge", func(t *testing.T) {
		origin := newH2COrigin(t)
		responses := make(chan domain.ProxyResponse, 4)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true, onResponse: func(res domain.ProxyResponse) { responses <- res }})
		launchpad, err := h.repository.CreateLaunchpad("h2c", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		raw := "GET /x HTTP/2\r\n:authority: " + origin + "\r\nx-test: One\r\n\r\n"

		launch, err := h.proxy.LaunchSync(context.Background(), raw, launchpad.String(), false)
		if err != nil || launch.Status != domain.LaunchCompleted {
			t.Fatalf("the launch\nwanted:\ncompleted\ngot:\n%+v %v", launch, err)
		}
		select {
		case res := <-responses:
			if want := "HTTP/2.0 GET /x x-test=One body="; !strings.HasSuffix(string(res.Raw), want) {
				t.Fatalf("the stored response\nwanted:\na body of %q\ngot:\n%q", want, res.Raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the stored response")
		}
	})

	t.Run("should send an Armory request written as HTTP/2 over HTTP/2, its run kept off the wire", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		runID := uuid.Must(uuid.NewV7())
		raw := "GET /fuzz HTTP/2\r\n:authority: " + origin.addr + "\r\nx-payload: 1\r\n\r\n"

		sent := make(chan error, 1)
		go func() { sent <- h.proxy.SendArmoryRequest(context.Background(), raw, runID, true) }()
		upstream := origin.accept(t)
		id := upstream.nthStream(t, 0)
		request := upstream.stream(t, id)
		upstream.headers(id, true, ":status", "204")

		want := h2Fields{{":method", "GET", false}, {":path", "/fuzz", false}, {":authority", origin.addr, false}, {":scheme", "https", false}, {"x-payload", "1", false}}
		if len(request.headers) != 1 || !equalFields(request.headers[0], want) {
			t.Fatalf("the origin's request\nwanted:\n%v\ngot:\n%v", want, request.headers)
		}
		if err := <-sent; err != nil {
			t.Fatalf("SendArmoryRequest() returned unexpected error: %v", err)
		}
		if stored := capturedRequest(t, h); fmt.Sprint(stored.Metadata["armory_run_id"]) != runID.String() {
			t.Fatalf("the stored request's metadata\nwanted:\narmory_run_id %s\ngot:\n%v", runID, stored.Metadata)
		}
	})

	t.Run("should send a message-form gRPC Armory template as well-formed envelopes over HTTP/2, stored as a streamed pair", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		configDir := t.TempDir()
		wordlists, err := wordlist.NewManager(configDir)
		if err != nil {
			t.Fatal(err)
		}
		payload := strings.Repeat("p", 200)
		if err := os.WriteFile(filepath.Join(configDir, "wordlists", "words.txt"), []byte(payload+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := &domain.ArmoryRun{
			ID:               uuid.Must(uuid.NewV7()),
			TemplateSnapshot: "POST /test.Echo/Say HTTP/2\r\n:authority: " + origin.addr + "\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\n--- message ---\n1:LEN {\"@@short@@\"}\n",
			TemplateFraming:  "grpc",
			UseHTTPS:         true,
			Wordlists:        []string{"words.txt"},
			Status:           domain.ArmoryRunDraft,
			AttackType:       domain.ArmoryAttackHarpoon,
			MaxConcurrent:    1,
		}
		repo := &wireArmoryRepository{run: run, updates: make(chan domain.ArmoryRunStatus, 4)}
		manager, err := armory.NewManager(repo, wordlists, h.proxy.SendArmoryRequest)
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.StartRun(run.ID); err != nil {
			t.Fatalf("StartRun() returned unexpected error: %v", err)
		}

		upstream := origin.accept(t)
		id := upstream.nthStream(t, 0)
		request := upstream.stream(t, id)
		// Field 1, length 200 as the varint c8 01, then the payload, in an
		// envelope declaring those 203 bytes.
		message := "\x0a\xc8\x01" + payload
		if want := grpcEnvelope(0, message); string(request.data) != want {
			t.Fatalf("the origin's request body\nwanted:\n%q\ngot:\n%q", want, request.data)
		}
		upstream.headers(id, false, ":status", "200", "content-type", "application/grpc")
		upstream.data(id, false, grpcEnvelope(0, ""))
		upstream.headers(id, true, "grpc-status", "0")

		for status := range repo.updates {
			if status == domain.ArmoryRunCompleted {
				break
			}
			if status != domain.ArmoryRunInProgress {
				t.Fatalf("the run\nwanted:\ncomplete\ngot:\n%s", status)
			}
		}
		stored := capturedRequest(t, h)
		if fmt.Sprint(stored.Metadata["armory_run_id"]) != run.ID.String() {
			t.Fatalf("the stored request's metadata\nwanted:\narmory_run_id %s\ngot:\n%v", run.ID, stored.Metadata)
		}
		messages := storedMessages(t, h.proxy, stored.ID)
		if len(messages) != 2 || messages[0].Direction != "request" || string(messages[0].Payload) != message || messages[1].Direction != "response" {
			t.Fatalf("the stored stream messages\nwanted:\nthe request message, then the response's\ngot:\n%+v", messages)
		}
		metadata, err := h.proxy.TrafficRepo.GetMetadata(stored.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d := direction(t, metadata, "request"); d["framing"] != "grpc" || d["ending"] != "complete" {
			t.Fatalf("metadata.streamed.request\nwanted:\ngrpc, complete\ngot:\n%v", d)
		}
	})

	t.Run("should answer an HTTP/2 launch with a 502 naming the cause when the origin will not speak HTTP/2", func(t *testing.T) {
		tests := []struct {
			name  string
			alpn  []string
			cause string
		}{
			{name: "the origin offers only http/1.1", alpn: []string{"http/1.1"}, cause: "no application protocol"},
			{name: "the origin answers with no ALPN", alpn: nil, cause: "did not choose h2"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				origin := newALPNOrigin(t, tt.alpn)
				responses := make(chan domain.ProxyResponse, 4)
				h := startViewHarness(t, viewHarnessConfig{secureOrigin: true, onResponse: func(res domain.ProxyResponse) { responses <- res }})
				raw := "GET / HTTP/2\r\n:authority: " + origin.addr + "\r\n\r\n"

				// As over HTTP/1.1, a proxy error response fails the launch.
				if launch := waitLaunch(t, launchH2(t, h, raw)); launch.Status != domain.LaunchFailed || launch.Reason != domain.LaunchReasonUpstreamError {
					t.Fatalf("the launch\nwanted:\nfailed, upstream_error\ngot:\n%+v", launch)
				}
				select {
				case res := <-responses:
					if res.StatusCode != 502 || !strings.Contains(string(res.Raw), tt.cause) {
						t.Fatalf("the stored response\nwanted:\n502 naming %q\ngot:\n%s %q", tt.cause, res.Status, res.Raw)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for the stored response")
				}
				if got, _ := origin.received(); len(got) != 0 {
					t.Fatalf("the origin\nwanted:\nno request\ngot:\n%q", got)
				}
			})
		}
	})
}

// capturedRequest waits for the next request h captured.
func capturedRequest(t *testing.T, h *viewHarness) domain.ProxyRequest {
	t.Helper()
	select {
	case req := <-h.captured:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the captured request")
		return domain.ProxyRequest{}
	}
}

// newALPNOrigin is an HTTP/1.1 origin over TLS that answers ALPN with alpn,
// recording what it reads.
func newALPNOrigin(t *testing.T, alpn []string) *rawOrigin {
	t.Helper()
	issuer, _ := newTestLeafIssuer(t, 24*time.Hour)
	config := issuer.config("127.0.0.1:443")
	config.NextProtos = alpn
	l, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	origin := &rawOrigin{addr: l.Addr().String(), response: "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go origin.serve(conn)
		}
	}()
	return origin
}

func TestReadRawRequest_H2(t *testing.T) {
	t.Run("should refuse HTTP/2 text it cannot send", func(t *testing.T) {
		tests := []struct {
			name, raw, want string
		}{
			{name: "no authority and no host", raw: "GET / HTTP/2\r\nx-a: 1\r\n\r\n", want: "host header not found"},
			{name: "a CONNECT", raw: "CONNECT example.com:443 HTTP/2\r\n:authority: example.com:443\r\n\r\n", want: "CONNECT cannot be sent raw"},
			{name: "a field line without a colon", raw: "GET / HTTP/2\r\n:authority: example.com\r\nbroken\r\n\r\n", want: "has no colon"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := readRawRequest(tt.raw, true); err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("readRawRequest()\nwanted:\nan error naming %q\ngot:\n%v", tt.want, err)
				}
			})
		}
	})

	t.Run("should keep a host field as typed and add no :authority", func(t *testing.T) {
		r, err := readRawRequest("GET /p HTTP/2\nHost: example.com\n\n", false)
		if err != nil {
			t.Fatalf("readRawRequest() returned unexpected error: %v", err)
		}
		want := h2Fields{{":method", "GET", false}, {":path", "/p", false}, {":scheme", "http", false}, {"Host", "example.com", false}}
		if !equalFields(r.h2.fields, want) || r.view.Host != "example.com" {
			t.Fatalf("the request\nwanted:\n%v host example.com\ngot:\n%v host %q", want, r.h2.fields, r.view.Host)
		}
	})
}

// wireArmoryRepository holds one Armory run and reports each status written.
type wireArmoryRepository struct {
	domain.ArmoryRepository
	run     *domain.ArmoryRun
	updates chan domain.ArmoryRunStatus
}

func (repo *wireArmoryRepository) GetArmoryRun(uuid.UUID) (*domain.ArmoryRun, error) {
	return repo.run, nil
}

func (repo *wireArmoryRepository) UpdateArmoryRun(run *domain.ArmoryRun) error {
	repo.updates <- run.Status
	return nil
}
