package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// newLaunchServer serves launches for one launchpad through a proxy whose
// client reaches origin directly.
func newLaunchServer(t *testing.T, launchpadID uuid.UUID, origin *httptest.Server) *Server {
	t.Helper()
	repo := &stubLaunchpadRepository{items: map[uuid.UUID]*domain.Launchpad{launchpadID: {ID: launchpadID, Name: "Login"}}}
	proxy := &marasi.Proxy{LaunchpadRepo: repo, Client: origin.Client()}
	server := newTestServer(proxy, func() {})
	if err := proxy.WithOptions(
		marasi.WithLaunchStartedHandler(server.HandleLaunchStarted),
		marasi.WithLaunchFinishedHandler(server.HandleLaunchFinished),
	); err != nil {
		t.Fatalf("installing launch handlers: %v", err)
	}
	return server
}

func launchBody(origin *httptest.Server, path string) string {
	raw := "GET " + path + " HTTP/1.1\r\nHost: " + strings.TrimPrefix(origin.URL, "http://") + "\r\n\r\n"
	return `{"raw":"` + base64.StdEncoding.EncodeToString([]byte(raw)) + `","scheme":"http"}`
}

func startLaunch(t *testing.T, server *Server, launchpadID uuid.UUID, body string) uuid.UUID {
	t.Helper()
	response := requestLaunchpad(server, http.MethodPost, "/launchpad/"+launchpadID.String()+"/launch", body)
	var started struct {
		LaunchID uuid.UUID `json:"launch_id"`
		Status   string    `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil || response.Code != http.StatusOK {
		t.Fatalf("\nwanted:\n200 start response\ngot:\n%d %s", response.Code, response.Body.String())
	}
	want := `{"launch_id":"` + started.LaunchID.String() + `","status":"pending"}` + "\n"
	if response.Body.String() != want {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, response.Body.String())
	}
	return started.LaunchID
}

func TestLaunchControlAPI(t *testing.T) {
	launchpadID := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")

	t.Run("should start a launch and publish started then completed", func(t *testing.T) {
		received := make(chan *http.Request, 1)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received <- r
		}))
		defer origin.Close()
		server := newLaunchServer(t, launchpadID, origin)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		id := startLaunch(t, server, launchpadID, launchBody(origin, "/login"))

		if request := <-received; request.URL.Path != "/login" || request.Header.Get("x-marasi-launch-id") != id.String() {
			t.Fatalf("\nwanted:\n/login with launch %s\ngot:\n%s with launch %q", id, request.URL.Path, request.Header.Get("x-marasi-launch-id"))
		}
		record := `{"launch_id":"` + id.String() + `","launchpad_id":"` + launchpadID.String() + `","status":"%s"}`
		for _, want := range []string{
			"launch.started " + strings.Replace(record, "%s", "pending", 1),
			"launch.completed " + strings.Replace(record, "%s", "completed", 1),
		} {
			if got := nextEvent(subscriber, 5*time.Second); got != want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
			}
		}
	})

	t.Run("should publish launch.failed with its reason", func(t *testing.T) {
		closed := httptest.NewServer(http.NotFoundHandler())
		server := newLaunchServer(t, launchpadID, closed)
		closed.Close()
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		id := startLaunch(t, server, launchpadID, launchBody(closed, "/gone"))

		nextEvent(subscriber, 5*time.Second)
		want := `launch.failed {"launch_id":"` + id.String() + `","launchpad_id":"` + launchpadID.String() + `","status":"failed","reason":"network_error"}`
		if got := nextEvent(subscriber, 5*time.Second); got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should list and read pending launches", func(t *testing.T) {
		release := make(chan struct{})
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
		}))
		defer origin.Close()
		defer close(release)
		server := newLaunchServer(t, launchpadID, origin)

		id := startLaunch(t, server, launchpadID, launchBody(origin, "/slow"))

		record := `{"launch_id":"` + id.String() + `","launchpad_id":"` + launchpadID.String() + `","status":"pending"}`
		if response := requestLaunchpad(server, http.MethodGet, "/launch", ""); response.Code != http.StatusOK || response.Body.String() != `{"items":[`+record+`]}`+"\n" {
			t.Fatalf("\nwanted:\n200 {\"items\":[%s]}\ngot:\n%d %s", record, response.Code, response.Body.String())
		}
		if response := requestLaunchpad(server, http.MethodGet, "/launch/"+id.String(), ""); response.Code != http.StatusOK || response.Body.String() != record+"\n" {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", record, response.Code, response.Body.String())
		}
	})

	t.Run("should keep an empty list stable and reject unknown or bad ids", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{}, func() {})
		if response := requestLaunchpad(server, http.MethodGet, "/launch", ""); response.Code != http.StatusOK || response.Body.String() != "{\"items\":[]}\n" {
			t.Fatalf("\nwanted:\n200 {\"items\":[]}\ngot:\n%d %s", response.Code, response.Body.String())
		}
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodGet, "/launch/0193802f-f0e7-73d9-a764-06d21e367809", ""), http.StatusNotFound, "not_found")
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodGet, "/launch/not-a-uuid", ""), http.StatusBadRequest, "bad_request")
	})

	t.Run("should cancel a pending launch and publish launch.cancelled", func(t *testing.T) {
		received := make(chan struct{}, 1)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received <- struct{}{}
			<-r.Context().Done()
		}))
		defer origin.Close()
		server := newLaunchServer(t, launchpadID, origin)
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)

		id := startLaunch(t, server, launchpadID, launchBody(origin, "/slow"))
		<-received

		record := `{"launch_id":"` + id.String() + `","launchpad_id":"` + launchpadID.String() + `","status":"cancelled"}`
		if response := requestLaunchpad(server, http.MethodPost, "/launch/"+id.String()+"/cancel", ""); response.Code != http.StatusOK || response.Body.String() != record+"\n" {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", record, response.Code, response.Body.String())
		}
		nextEvent(subscriber, 5*time.Second)
		if got, want := nextEvent(subscriber, 5*time.Second), "launch.cancelled "+record; got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should refuse to cancel a finished launch with its final status", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{
			Request: domain.ProxyRequest{ID: id},
			Metadata: map[string]any{
				"launchpad":    true,
				"launchpad_id": launchpadID.String(),
				"launch":       map[string]any{"status": "failed", "reason": "upstream_error"},
			},
		}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})

		response := requestLaunchpad(server, http.MethodPost, "/launch/"+id.String()+"/cancel", "")

		want := `{"error":"launch_finished","launch_id":"` + id.String() + `","launchpad_id":"` + launchpadID.String() + `","status":"failed","reason":"upstream_error"}` + "\n"
		if response.Code != http.StatusConflict || response.Body.String() != want {
			t.Fatalf("\nwanted:\n409 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}
	})

	t.Run("should read a finished launch from its stored pair", func(t *testing.T) {
		id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		repo := &stubTrafficRepository{row: &domain.RequestResponseRow{
			Request: domain.ProxyRequest{ID: id},
			Metadata: map[string]any{
				"launchpad":    true,
				"launchpad_id": launchpadID.String(),
				"launch":       map[string]any{"status": "cancelled"},
			},
		}}
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})

		response := requestLaunchpad(server, http.MethodGet, "/launch/"+id.String(), "")

		want := `{"launch_id":"` + id.String() + `","launchpad_id":"` + launchpadID.String() + `","status":"cancelled"}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body.String())
		}
	})

	t.Run("should interrupt a launch held at Checkpoint when the project switches", func(t *testing.T) {
		received := make(chan struct{}, 1)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received <- struct{}{}
		}))
		defer origin.Close()
		server, lifecycle, dir, current := newProjectOpenServer(t)
		proxy := lifecycle.proxy
		held := make(chan struct{}, 1)
		if err := proxy.WithOptions(
			marasi.WithLaunchStartedHandler(server.HandleLaunchStarted),
			marasi.WithLaunchFinishedHandler(server.HandleLaunchFinished),
			marasi.WithInterceptHandler(func(domain.CheckpointItem) error { held <- struct{}{}; return nil }),
			marasi.WithBasePipeline(),
			marasi.WithDefaultModifierPipeline(),
		); err != nil {
			t.Fatalf("installing pipeline: %v", err)
		}
		listener, err := proxy.GetListener("127.0.0.1", "0")
		if err != nil {
			t.Fatalf("creating proxy listener: %v", err)
		}
		serveResult := make(chan error, 1)
		go func() { serveResult <- proxy.Serve(listener) }()
		t.Cleanup(func() {
			_ = proxy.CloseTransport()
			<-serveResult
		})
		launchpad, err := proxy.LaunchpadRepo.CreateLaunchpad("Login", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		subscriber := server.events.subscribe()
		defer server.events.unsubscribe(subscriber)
		proxy.SetIntercept(true)
		id := startLaunch(t, server, launchpad, launchBody(origin, "/held"))
		select {
		case <-held:
		case <-time.After(5 * time.Second):
			t.Fatal("the launch was not held at Checkpoint")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := lifecycle.Open(ctx, canonicalProjectPath(t, filepath.Join(dir, "next.marasi"))); err != nil {
			t.Fatalf("opening the next project: %v", err)
		}

		want := `launch.failed {"launch_id":"` + id.String() + `","launchpad_id":"` + launchpad.String() + `","status":"failed","reason":"interrupted"}`
		for got := ""; got != want; {
			got = nextEvent(subscriber, 5*time.Second)
			if got == "" {
				t.Fatalf("\nwanted:\n%s\ngot:\nno more events", want)
			}
		}
		read := requestLaunchpad(server, http.MethodGet, "/launch/"+id.String(), "")
		wantRead := `{"launch_id":"` + id.String() + `","launchpad_id":"` + launchpad.String() + `","status":"failed","reason":"interrupted"}` + "\n"
		if read.Code != http.StatusOK || read.Body.String() != wantRead {
			t.Fatalf("\nwanted:\nGET /launch/{id} in the next project 200 %s\ngot:\n%d %s", wantRead, read.Code, read.Body.String())
		}
		if refused := requestLaunchpad(server, http.MethodPost, "/launch/"+id.String()+"/cancel", ""); refused.Code != http.StatusConflict {
			t.Fatalf("\nwanted:\ncancel in the next project 409\ngot:\n%d %s", refused.Code, refused.Body.String())
		}
		if err := lifecycle.Shutdown(); err != nil {
			t.Fatalf("shutting down: %v", err)
		}
		connection, err := db.New(current, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("opening the original project: %v", err)
		}
		original := db.NewProxyRepo(connection)
		defer original.Close()
		metadata, err := original.GetMetadata(id)
		if err != nil {
			t.Fatalf("\nwanted:\npair %s in the original project\ngot:\n%v", id, err)
		}
		if got, want := metadata["launch"], map[string]any{"status": "failed", "reason": "interrupted"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\nmetadata.launch %v\ngot:\n%v", want, got)
		}
		select {
		case <-received:
			t.Fatal("\nwanted:\nno request at the origin\ngot:\none")
		default:
		}
	})

	t.Run("should not cancel unknown or bad ids", func(t *testing.T) {
		server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{}}, func() {})
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodPost, "/launch/0193802f-f0e7-73d9-a764-06d21e367809/cancel", ""), http.StatusNotFound, "not_found")
		assertLaunchpadError(t, requestLaunchpad(server, http.MethodPost, "/launch/not-a-uuid/cancel", ""), http.StatusBadRequest, "bad_request")
	})
}
