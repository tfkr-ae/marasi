package marasi

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/extensions"
)

type launchHarness struct {
	proxy       *Proxy
	repository  *db.Repository
	launchpadID uuid.UUID
	finished    chan domain.Launch
	requests    chan domain.ProxyRequest
}

// startLaunchHarness serves a real proxy over an in-memory project with one launchpad.
// Extra options are applied after the harness's own, before the pipelines.
func startLaunchHarness(t *testing.T, extra ...func(*Proxy) error) *launchHarness {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbConnection, err := db.New(":memory:", logger)
	if err != nil {
		t.Fatalf("creating in-memory database: %v", err)
	}
	repository := db.NewProxyRepo(dbConnection)
	defaultExtensions, err := repository.GetExtensions()
	if err != nil {
		repository.Close()
		t.Fatalf("getting default extensions: %v", err)
	}
	launchpadID, err := repository.CreateLaunchpad("launches", "")
	if err != nil {
		repository.Close()
		t.Fatalf("creating launchpad: %v", err)
	}
	finished := make(chan domain.Launch, 16)
	requests := make(chan domain.ProxyRequest, 16)
	options := []func(*Proxy) error{
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithExtensions(defaultExtensions),
		WithRequestHandler(func(req domain.ProxyRequest) error { requests <- req; return nil }),
		WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithLaunchFinishedHandler(func(launch domain.Launch) { finished <- launch }),
	}
	options = append(options, extra...)
	options = append(options, WithBasePipeline(), WithDefaultModifierPipeline())
	proxy, err := New(options...)
	if err != nil {
		repository.Close()
		t.Fatalf("creating proxy: %v", err)
	}
	proxyListener, err := proxy.GetListener("127.0.0.1", "0")
	if err != nil {
		proxy.Close()
		t.Fatalf("creating proxy listener: %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- proxy.Serve(proxyListener) }()
	t.Cleanup(func() {
		proxyListener.Close()
		proxy.Close()
		select {
		case <-serveResult:
		case <-time.After(5 * time.Second):
		}
	})
	return &launchHarness{proxy: proxy, repository: repository, launchpadID: launchpadID, finished: finished, requests: requests}
}

func (h *launchHarness) waitFinished(t *testing.T) domain.Launch {
	t.Helper()
	select {
	case launch := <-h.finished:
		return launch
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the launch to finish")
		return domain.Launch{}
	}
}

func (h *launchHarness) launchpadMembers(t *testing.T) []uuid.UUID {
	t.Helper()
	members, err := h.repository.GetLaunchpadRequests(h.launchpadID, "", nil, "")
	if err != nil {
		t.Fatalf("getting launchpad members: %v", err)
	}
	ids := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.ID)
	}
	return ids
}

func rawGet(origin *httptest.Server, path string) string {
	return "GET " + path + " HTTP/1.1\r\nHost: " + strings.TrimPrefix(origin.URL, "http://") + "\r\n\r\n"
}

func TestProxy_Launch(t *testing.T) {
	t.Run("should store the pair under the launch id, link it, and complete", func(t *testing.T) {
		seen := make(chan http.Header, 1)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- r.Header.Clone()
			_, _ = io.WriteString(w, "ok")
		}))
		defer origin.Close()
		h := startLaunchHarness(t)

		id, err := h.proxy.Launch(rawGet(origin, "/done"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		if got := h.launchpadMembers(t); !reflect.DeepEqual(got, []uuid.UUID{id}) {
			t.Fatalf("\nwanted:\nlaunchpad members [%s]\ngot:\n%v", id, got)
		}
		metadata, err := h.repository.GetMetadata(id)
		if err != nil {
			t.Fatalf("getting pair metadata: %v", err)
		}
		if got, want := metadata["launch"], map[string]any{"status": "completed"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\nmetadata.launch %v\ngot:\n%v", want, got)
		}
		if metadata["launchpad"] != true || metadata["launchpad_id"] != h.launchpadID.String() {
			t.Fatalf("\nwanted:\nlaunchpad metadata for %s\ngot:\n%v", h.launchpadID, metadata)
		}
		page, err := h.repository.ListTraffic(domain.TrafficQuery{Text: `metadata.launch.status = "completed"`, Limit: 10})
		found := page.Items
		if err != nil || len(found) != 1 || found[0].ID != id {
			t.Fatalf("\nwanted:\nquery finds launch %s\ngot:\n%v, %v", id, found, err)
		}
		header := <-seen
		if header.Get("x-marasi-launch-id") != "" || header.Get("x-launchpad-id") != "" {
			t.Fatalf("\nwanted:\nno launch headers at the origin\ngot:\n%v", header)
		}
		if len(h.proxy.Launches()) != 0 {
			t.Fatalf("\nwanted:\nno pending launches\ngot:\n%v", h.proxy.Launches())
		}
	})

	t.Run("should list and read a pending launch", func(t *testing.T) {
		release := make(chan struct{})
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
		}))
		defer origin.Close()
		h := startLaunchHarness(t)

		id, err := h.proxy.Launch(rawGet(origin, "/slow"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchPending}
		if got := h.proxy.Launches(); !reflect.DeepEqual(got, []domain.Launch{want}) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", []domain.Launch{want}, got)
		}
		if got, err := h.proxy.GetLaunch(id); err != nil || got != want {
			t.Fatalf("\nwanted:\n%+v, nil\ngot:\n%+v, %v", want, got, err)
		}
		if _, err := h.proxy.GetLaunch(uuid.New()); err != domain.ErrLaunchNotFound {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrLaunchNotFound, err)
		}
		close(release)
		if got := h.waitFinished(t); got.Status != domain.LaunchCompleted {
			t.Fatalf("\nwanted:\ncompleted\ngot:\n%+v", got)
		}
	})

	t.Run("should reject malformed requests and a missing Host without creating a launch", func(t *testing.T) {
		h := startLaunchHarness(t)
		for _, raw := range []string{"not an HTTP request", "GET / HTTP/1.1\r\n\r\n"} {
			if _, err := h.proxy.Launch(raw, h.launchpadID.String(), false); err == nil {
				t.Fatalf("\nwanted:\nerror for %q\ngot:\nnil", raw)
			}
		}
		if len(h.proxy.Launches()) != 0 {
			t.Fatalf("\nwanted:\nno launches\ngot:\n%v", h.proxy.Launches())
		}
	})
}

func (h *launchHarness) waitRequest(t *testing.T, path string) domain.ProxyRequest {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case req := <-h.requests:
			if req.Path == path {
				return req
			}
		case <-deadline:
			t.Fatalf("timed out waiting for stored request %s", path)
			return domain.ProxyRequest{}
		}
	}
}

// outsideClient sends through the proxy without the proxy client's credentials.
func (h *launchHarness) outsideClient(t *testing.T) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse("http://" + net.JoinHostPort(h.proxy.Addr, h.proxy.Port))
	if err != nil {
		t.Fatalf("parsing proxy url: %v", err)
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
}

// launchOrigin answers every path at once except /slow, which waits for release.
type launchOrigin struct {
	*httptest.Server
	release     chan struct{}
	releaseOnce sync.Once
	seen        chan *http.Request
}

// unblock lets /slow answer. It is safe to call more than once.
func (origin *launchOrigin) unblock() {
	origin.releaseOnce.Do(func() { close(origin.release) })
}

func startLaunchOrigin(t *testing.T) *launchOrigin {
	t.Helper()
	origin := &launchOrigin{release: make(chan struct{}), seen: make(chan *http.Request, 16)}
	origin.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin.seen <- r
		if r.URL.Path == "/slow" {
			<-origin.release
		}
	}))
	t.Cleanup(func() {
		origin.unblock()
		origin.Close()
	})
	return origin
}

func (origin *launchOrigin) waitPath(t *testing.T, path string) *http.Request {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case req := <-origin.seen:
			if req.URL.Path == path {
				return req
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the origin to see %s", path)
			return nil
		}
	}
}

func sendThrough(t *testing.T, client *http.Client, target, launchID string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("x-marasi-launch-id", launchID)
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
}

func TestProxy_LaunchFailure(t *testing.T) {
	t.Run("should fail with upstream_error when the upstream cannot be reached", func(t *testing.T) {
		closed := httptest.NewServer(http.NotFoundHandler())
		closed.Close()
		h := startLaunchHarness(t)

		id, err := h.proxy.Launch(rawGet(closed, "/gone"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchFailed, Reason: domain.LaunchReasonUpstreamError}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		metadata, err := h.repository.GetMetadata(id)
		if err != nil {
			t.Fatalf("getting pair metadata: %v", err)
		}
		if got, want := metadata["launch"], map[string]any{"status": "failed", "reason": "upstream_error"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\nmetadata.launch %v\ngot:\n%v", want, got)
		}
		if got := h.launchpadMembers(t); !reflect.DeepEqual(got, []uuid.UUID{id}) {
			t.Fatalf("\nwanted:\nlaunchpad members [%s]\ngot:\n%v", id, got)
		}
	})

	t.Run("should fail with duplicate_id without overwriting the existing pair", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		taken := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
		existing := map[string]any{"source": "earlier"}
		if err := h.repository.InsertRequest(&domain.ProxyRequest{
			ID: taken, Scheme: "http", Method: "GET", Host: "earlier.test", Path: "/earlier",
			Raw: []byte("GET /earlier HTTP/1.1\r\nHost: earlier.test\r\n\r\n"), Metadata: existing, RequestedAt: time.Now(),
		}); err != nil {
			t.Fatalf("inserting existing pair: %v", err)
		}
		h.proxy.newLaunchID = func() (uuid.UUID, error) { return taken, nil }

		id, err := h.proxy.Launch(rawGet(origin.Server, "/again"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: taken, LaunchpadID: h.launchpadID, Status: domain.LaunchFailed, Reason: domain.LaunchReasonDuplicateID}
		if got := h.waitFinished(t); id != taken || got != want {
			t.Fatalf("\nwanted:\n%s %+v\ngot:\n%s %+v", taken, want, id, got)
		}
		row, err := h.repository.GetRequestResponseRow(taken)
		if err != nil {
			t.Fatalf("getting existing pair: %v", err)
		}
		if row.Request.Path != "/earlier" || row.Response.StatusCode > 0 || !reflect.DeepEqual(row.Metadata, existing) {
			t.Fatalf("\nwanted:\nexisting pair unchanged\ngot:\n%s %d %v", row.Request.Path, row.Response.StatusCode, row.Metadata)
		}
		if got := h.launchpadMembers(t); len(got) != 0 {
			t.Fatalf("\nwanted:\nno launchpad members\ngot:\n%v", got)
		}
	})
}

func TestProxy_LaunchWebSocketCompletesAtHandshake(t *testing.T) {
	h := startLaunchHarness(t)
	closed := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
		buffered.Flush()
		// Hold the connection open until the proxy closes it.
		_, _ = io.Copy(io.Discard, buffered)
		close(closed)
	}))
	defer origin.Close()
	raw := "GET /socket HTTP/1.1\r\nHost: " + strings.TrimPrefix(origin.URL, "http://") +
		"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"

	id, err := h.proxy.Launch(raw, h.launchpadID.String(), false)
	if err != nil {
		t.Fatalf("Launch() returned unexpected error: %v", err)
	}

	want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
	if got := h.waitFinished(t); got != want {
		t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
	}
	select {
	case <-closed:
		t.Fatal("wanted the WebSocket to stay open after the launch completed")
	default:
	}
	metadata, err := h.repository.GetMetadata(id)
	if err != nil {
		t.Fatalf("getting pair metadata: %v", err)
	}
	if got, want := metadata["launch"], map[string]any{"status": "completed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("\nwanted:\nmetadata.launch %v\ngot:\n%v", want, got)
	}
	if got := h.launchpadMembers(t); !reflect.DeepEqual(got, []uuid.UUID{id}) {
		t.Fatalf("\nwanted:\nlaunchpad members [%s]\ngot:\n%v", id, got)
	}
}

func TestProxy_LaunchIDHeader(t *testing.T) {
	t.Run("should treat a forged header from an outside client as ordinary traffic", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		origin.waitPath(t, "/slow")
		h.waitRequest(t, "/slow")

		sendThrough(t, h.outsideClient(t), origin.URL+"/forged", id.String())

		if seen := origin.waitPath(t, "/forged"); seen.Header.Get("x-marasi-launch-id") != "" {
			t.Fatalf("\nwanted:\nno launch header at the origin\ngot:\n%v", seen.Header)
		}
		forged := h.waitRequest(t, "/forged")
		if forged.ID == id || forged.Metadata["launchpad_id"] != nil {
			t.Fatalf("\nwanted:\nfresh unlinked pair\ngot:\n%s %v", forged.ID, forged.Metadata)
		}
		origin.unblock()
		if got := h.waitFinished(t); got.ID != id || got.Status != domain.LaunchCompleted {
			t.Fatalf("\nwanted:\nlaunch %s completed\ngot:\n%+v", id, got)
		}
		if got := h.launchpadMembers(t); !reflect.DeepEqual(got, []uuid.UUID{id}) {
			t.Fatalf("\nwanted:\nlaunchpad members [%s]\ngot:\n%v", id, got)
		}
	})

	t.Run("should treat a stale launch id as ordinary traffic", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		stale := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")

		sendThrough(t, h.proxy.Client, origin.URL+"/stale", stale.String())

		if seen := origin.waitPath(t, "/stale"); seen.Header.Get("x-marasi-launch-id") != "" {
			t.Fatalf("\nwanted:\nno launch header at the origin\ngot:\n%v", seen.Header)
		}
		stored := h.waitRequest(t, "/stale")
		if stored.ID == stale || stored.Metadata["launchpad_id"] != nil {
			t.Fatalf("\nwanted:\nfresh unlinked pair\ngot:\n%s %v", stored.ID, stored.Metadata)
		}
		if got := h.launchpadMembers(t); len(got) != 0 {
			t.Fatalf("\nwanted:\nno launchpad members\ngot:\n%v", got)
		}
	})

	t.Run("should drop a second claim of a claimed launch before the origin", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		origin.waitPath(t, "/slow")

		// The origin records a request before answering it, so once the
		// second claim has its response the origin would already show it.
		sendThrough(t, h.proxy.Client, origin.URL+"/second", id.String())

		select {
		case seen := <-origin.seen:
			t.Fatalf("\nwanted:\nno second claim at the origin\ngot:\n%s", seen.URL.Path)
		default:
		}
		origin.unblock()
		if got := h.waitFinished(t); got.ID != id || got.Status != domain.LaunchCompleted {
			t.Fatalf("\nwanted:\nlaunch %s completed\ngot:\n%+v", id, got)
		}
	})
}

func TestProxy_CancelLaunch(t *testing.T) {
	t.Run("should cancel a launch waiting on a slow upstream and abandon the round trip", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		upstream := origin.waitPath(t, "/slow")
		h.waitRequest(t, "/slow")

		cancelled, err := h.proxy.CancelLaunch(id)

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCancelled}
		if err != nil || cancelled != want {
			t.Fatalf("\nwanted:\n%+v, nil\ngot:\n%+v, %v", want, cancelled, err)
		}
		select {
		case <-upstream.Context().Done():
		case <-time.After(5 * time.Second):
			t.Fatal("wanted the upstream round trip to be abandoned")
		}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		row, err := h.repository.GetRequestResponseRow(id)
		if err != nil {
			t.Fatalf("getting pair: %v", err)
		}
		if got, want := row.Metadata["launch"], map[string]any{"status": "cancelled"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\nmetadata.launch %v\ngot:\n%v", want, got)
		}
		if row.Response.StatusCode > 0 {
			t.Fatalf("\nwanted:\nno stored response\ngot:\n%d", row.Response.StatusCode)
		}
		if got := h.launchpadMembers(t); !reflect.DeepEqual(got, []uuid.UUID{id}) {
			t.Fatalf("\nwanted:\nlaunchpad members [%s]\ngot:\n%v", id, got)
		}
	})

	t.Run("should report the final state of a finished launch", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/done"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		h.waitFinished(t)

		got, err := h.proxy.CancelLaunch(id)

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
		if !errors.Is(err, domain.ErrLaunchFinished) || got != want {
			t.Fatalf("\nwanted:\n%+v, %v\ngot:\n%+v, %v", want, domain.ErrLaunchFinished, got, err)
		}
		metadata, err := h.repository.GetMetadata(id)
		if err != nil {
			t.Fatalf("getting pair metadata: %v", err)
		}
		if got, want := metadata["launch"], map[string]any{"status": "completed"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\nmetadata.launch %v\ngot:\n%v", want, got)
		}
	})

	t.Run("should not find an unknown id or ordinary traffic", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		sendThrough(t, h.outsideClient(t), origin.URL+"/ordinary", "")
		ordinary := h.waitRequest(t, "/ordinary")

		for _, id := range []uuid.UUID{uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809"), ordinary.ID} {
			if _, err := h.proxy.CancelLaunch(id); !errors.Is(err, domain.ErrLaunchNotFound) {
				t.Fatalf("\nwanted:\n%v for %s\ngot:\n%v", domain.ErrLaunchNotFound, id, err)
			}
		}
	})
}

// assertStoredAsLaunched checks that the launch's pair is stored under the
// launch id with the given outcome, carries the launched request, and is
// linked to the launchpad.
func (h *launchHarness) assertStoredAsLaunched(t *testing.T, id uuid.UUID, path string, outcome map[string]any) {
	t.Helper()
	row, err := h.repository.GetRequestResponseRow(id)
	if err != nil {
		t.Fatalf("getting pair %s: %v", id, err)
	}
	if got := row.Metadata["launch"]; !reflect.DeepEqual(got, outcome) {
		t.Fatalf("\nwanted:\nmetadata.launch %v\ngot:\n%v", outcome, got)
	}
	if row.Metadata["launchpad"] != true || row.Metadata["launchpad_id"] != h.launchpadID.String() {
		t.Fatalf("\nwanted:\nlaunchpad metadata for %s\ngot:\n%v", h.launchpadID, row.Metadata)
	}
	if row.Request.Path != path || !strings.HasPrefix(string(row.Request.Raw), "GET ") || !strings.Contains(string(row.Request.Raw), path+" HTTP/1.1\r\n") {
		t.Fatalf("\nwanted:\nlaunched request GET %s\ngot:\n%s %q", path, row.Request.Path, row.Request.Raw)
	}
	if strings.Contains(strings.ToLower(string(row.Request.Raw)), "x-marasi-") {
		t.Fatalf("\nwanted:\nno internal headers in the stored request\ngot:\n%q", row.Request.Raw)
	}
	if got := h.launchpadMembers(t); !reflect.DeepEqual(got, []uuid.UUID{id}) {
		t.Fatalf("\nwanted:\nlaunchpad members [%s]\ngot:\n%v", id, got)
	}
}

func TestProxy_LaunchStoresItsRequest(t *testing.T) {
	t.Run("should store a launch cancelled while held at Checkpoint as launched", func(t *testing.T) {
		held := make(chan domain.CheckpointItem, 1)
		abandoned := make(chan domain.CheckpointItem, 1)
		h := startLaunchHarness(t,
			WithInterceptHandler(func(item domain.CheckpointItem) error { held <- item; return nil }),
			WithCheckpointAbandonedHandler(func(item domain.CheckpointItem) error { abandoned <- item; return nil }),
		)
		origin := startLaunchOrigin(t)
		h.proxy.SetIntercept(true)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/held"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		item := receiveCheckpointItem(t, held, "the launch to be held")

		if _, err := h.proxy.CancelLaunch(id); err != nil {
			t.Fatalf("CancelLaunch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCancelled}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		if got := receiveCheckpointItem(t, abandoned, "the hold to be dropped"); got.ID != item.ID {
			t.Fatalf("\nwanted:\ndropped item %s\ngot:\n%s", item.ID, got.ID)
		}
		if items := h.proxy.CheckpointItems(); len(items) != 0 {
			t.Fatalf("\nwanted:\nno Checkpoint items\ngot:\n%v", items)
		}
		h.assertStoredAsLaunched(t, id, "/held", map[string]any{"status": "cancelled"})
		select {
		case seen := <-origin.seen:
			t.Fatalf("\nwanted:\nno request at the origin\ngot:\n%s", seen.URL.Path)
		default:
		}
	})
	t.Run("should store a launch cancelled inside a request hook as launched", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		workshop := h.blockingWorkshop(t, `function processRequest(request) print("entered") while true do end end`)
		entered := make(chan struct{})
		var once sync.Once
		workshop.OnLog = func(extensions.ExtensionLog) error {
			once.Do(func() { close(entered) })
			return nil
		}
		id, err := h.proxy.Launch(rawGet(origin.Server, "/hooked"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("processRequest did not enter Lua")
		}

		if _, err := h.proxy.CancelLaunch(id); err != nil {
			t.Fatalf("CancelLaunch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCancelled}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		h.assertStoredAsLaunched(t, id, "/hooked", map[string]any{"status": "cancelled"})
		// Running Lua again proves the hook has returned, so it can no longer send.
		if err := workshop.ExecuteLua("probe = true"); err != nil {
			t.Fatalf("running Lua after the hook: %v", err)
		}
		select {
		case seen := <-origin.seen:
			t.Fatalf("\nwanted:\nno request at the origin\ngot:\n%s", seen.URL.Path)
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("should fail a launch dropped by an extension with dropped and store it as launched", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		h.blockingWorkshop(t, `function processRequest(request) request:drop() end`)

		id, err := h.proxy.Launch(rawGet(origin.Server, "/dropped"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchFailed, Reason: domain.LaunchReasonDropped}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		h.assertStoredAsLaunched(t, id, "/dropped", map[string]any{"status": "failed", "reason": "dropped"})
		select {
		case seen := <-origin.seen:
			t.Fatalf("\nwanted:\nno request at the origin\ngot:\n%s", seen.URL.Path)
		default:
		}
	})

	t.Run("should run Compass on a launch out of scope and ignore only its skip decision", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		h.compass(t, `function processRequest(request) request:headers():set("X-Compass", "request") request:skip() end
function processResponse(response) response:headers():set("X-Compass", "response") response:skip() end`)
		marker, ok := h.proxy.GetExtension("workshop")
		if !ok {
			t.Fatal("getting workshop extension")
		}
		if err := marker.ExecuteLua(`function processRequest(request) request:set_metadata({pipeline = "ran"}) end`); err != nil {
			t.Fatalf("updating workshop: %v", err)
		}

		id, err := h.proxy.Launch(rawGet(origin.Server, "/outside"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		h.assertStoredAsLaunched(t, id, "/outside", map[string]any{"status": "completed"})
		row, err := h.repository.GetRequestResponseRow(id)
		if err != nil {
			t.Fatalf("getting pair: %v", err)
		}
		if got := row.Metadata["workshop"]; !reflect.DeepEqual(got, map[string]any{"pipeline": "ran"}) || row.Response.StatusCode != http.StatusOK {
			t.Fatalf("\nwanted:\npipeline ran and response stored\ngot:\n%v %d", row.Metadata, row.Response.StatusCode)
		}
		if !strings.Contains(string(row.Request.Raw), "X-Compass: request\r\n") || !strings.Contains(string(row.Response.Raw), "X-Compass: response\r\n") {
			t.Fatalf("\nwanted:\nCompass ran on the request and the response\ngot:\n%q\n%q", row.Request.Raw, row.Response.Raw)
		}
	})

	t.Run("should fail a launch Compass drops with dropped and store it as launched", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		h.compass(t, `function processRequest(request) request:drop() end`)

		id, err := h.proxy.Launch(rawGet(origin.Server, "/compass-dropped"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchFailed, Reason: domain.LaunchReasonDropped}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		h.assertStoredAsLaunched(t, id, "/compass-dropped", map[string]any{"status": "failed", "reason": "dropped"})
		select {
		case seen := <-origin.seen:
			t.Fatalf("\nwanted:\nno request at the origin\ngot:\n%s", seen.URL.Path)
		default:
		}
	})

	t.Run("should fail a launch whose response Compass drops with dropped", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		h.compass(t, `function processResponse(response) response:drop() end`)

		id, err := h.proxy.Launch(rawGet(origin.Server, "/response-dropped"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchFailed, Reason: domain.LaunchReasonDropped}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		h.assertStoredAsLaunched(t, id, "/response-dropped", map[string]any{"status": "failed", "reason": "dropped"})
	})
}

// compass replaces the Compass extension's script.
func (h *launchHarness) compass(t *testing.T, script string) {
	t.Helper()
	compass, ok := h.proxy.GetExtension("compass")
	if !ok {
		t.Fatal("getting compass extension")
	}
	if err := compass.ExecuteLua(script); err != nil {
		t.Fatalf("updating compass: %v", err)
	}
}

func TestProxy_GetLaunchAfterItFinished(t *testing.T) {
	t.Run("should read a completed launch once it has finished", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/done"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		h.waitFinished(t)

		got, err := h.proxy.GetLaunch(id)

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
		if err != nil || got != want {
			t.Fatalf("\nwanted:\n%+v, nil\ngot:\n%+v, %v", want, got, err)
		}
	})

	t.Run("should read a cancelled launch once it has finished", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		origin.waitPath(t, "/slow")
		h.waitRequest(t, "/slow")
		if _, err := h.proxy.CancelLaunch(id); err != nil {
			t.Fatalf("CancelLaunch() returned unexpected error: %v", err)
		}
		h.waitFinished(t)

		got, err := h.proxy.GetLaunch(id)

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCancelled}
		if err != nil || got != want {
			t.Fatalf("\nwanted:\n%+v, nil\ngot:\n%+v, %v", want, got, err)
		}
	})

	t.Run("should not find an id that is neither live nor a launch's pair", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		sendThrough(t, h.outsideClient(t), origin.URL+"/ordinary", "")
		ordinary := h.waitRequest(t, "/ordinary")

		for _, id := range []uuid.UUID{uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809"), ordinary.ID} {
			if _, err := h.proxy.GetLaunch(id); !errors.Is(err, domain.ErrLaunchNotFound) {
				t.Fatalf("\nwanted:\n%v for %s\ngot:\n%v", domain.ErrLaunchNotFound, id, err)
			}
		}
	})
}

// outcomeGate holds the database writer while it stores a launch's outcome.
type outcomeGate struct {
	domain.TrafficRepository
	entered chan struct{}
	release chan struct{}
}

func (gate *outcomeGate) UpdateMetadata(metadata map[string]any, ids ...uuid.UUID) error {
	if _, ok := metadata["launch"]; ok {
		close(gate.entered)
		<-gate.release
	}
	return gate.TrafficRepository.UpdateMetadata(metadata, ids...)
}

func TestProxy_LaunchReadableWhileItsOutcomeIsStored(t *testing.T) {
	t.Run("should read and refuse to cancel a finished launch before its outcome is stored", func(t *testing.T) {
		gate := &outcomeGate{entered: make(chan struct{}), release: make(chan struct{})}
		var release sync.Once
		h := startLaunchHarness(t, func(proxy *Proxy) error {
			gate.TrafficRepository = proxy.TrafficRepo
			proxy.TrafficRepo = gate
			return nil
		})
		t.Cleanup(func() { release.Do(func() { close(gate.release) }) })
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/done"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		select {
		case <-gate.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the launch outcome to be stored")
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
		if got, err := h.proxy.GetLaunch(id); err != nil || got != want {
			t.Fatalf("\nwanted:\nGetLaunch %+v, nil\ngot:\n%+v, %v", want, got, err)
		}
		if got, err := h.proxy.CancelLaunch(id); !errors.Is(err, domain.ErrLaunchFinished) || got != want {
			t.Fatalf("\nwanted:\nCancelLaunch %+v, %v\ngot:\n%+v, %v", want, domain.ErrLaunchFinished, got, err)
		}
		if got := h.proxy.Launches(); len(got) != 0 {
			t.Fatalf("\nwanted:\nno pending launches\ngot:\n%v", got)
		}

		release.Do(func() { close(gate.release) })
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		if got, err := h.proxy.GetLaunch(id); err != nil || got != want {
			t.Fatalf("\nwanted:\nGetLaunch %+v, nil\ngot:\n%+v, %v", want, got, err)
		}
	})
}

// blockingWorkshop loads script into the workshop extension. Cleanup cancels
// the runtime, so a hook that never returns cannot block proxy.Close.
func (h *launchHarness) blockingWorkshop(t *testing.T, script string) *extensions.Runtime {
	t.Helper()
	workshop, ok := h.proxy.GetExtension("workshop")
	if !ok {
		t.Fatal("getting workshop extension")
	}
	if err := workshop.ExecuteLua(script); err != nil {
		t.Fatalf("updating workshop: %v", err)
	}
	workshop.ExecutionContext, workshop.CancelExecution = context.WithCancel(context.Background())
	t.Cleanup(workshop.CancelExecution)
	return workshop
}

func TestProxy_LaunchSync(t *testing.T) {
	t.Run("should wait for the launch to complete and return it", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)

		got, err := h.proxy.LaunchSync(context.Background(), rawGet(origin.Server, "/done"), h.launchpadID.String(), false)

		if err != nil {
			t.Fatalf("LaunchSync() returned unexpected error: %v", err)
		}
		want := domain.Launch{ID: got.ID, LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
		if got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		if got := h.launchpadMembers(t); !reflect.DeepEqual(got, []uuid.UUID{want.ID}) {
			t.Fatalf("\nwanted:\nlaunchpad members [%s]\ngot:\n%v", want.ID, got)
		}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\nOnLaunchFinished %+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should cancel the launch when its context is cancelled", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Cancel once the launch is held at the origin.
		go func() {
			<-origin.seen
			cancel()
		}()

		got, err := h.proxy.LaunchSync(ctx, rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)

		if err != nil {
			t.Fatalf("LaunchSync() returned unexpected error: %v", err)
		}
		want := domain.Launch{ID: got.ID, LaunchpadID: h.launchpadID, Status: domain.LaunchCancelled}
		if got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		stored, err := h.proxy.GetLaunch(got.ID)
		if err != nil || stored != want {
			t.Fatalf("\nwanted:\nstored %+v, nil\ngot:\n%+v, %v", want, stored, err)
		}
	})

	t.Run("should fail at once for a malformed request without creating a launch", func(t *testing.T) {
		h := startLaunchHarness(t)

		if _, err := h.proxy.LaunchSync(context.Background(), "not an HTTP request", h.launchpadID.String(), false); err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if len(h.proxy.Launches()) != 0 {
			t.Fatalf("\nwanted:\nno launches\ngot:\n%v", h.proxy.Launches())
		}
	})
}

func TestProxy_InterruptedLaunches(t *testing.T) {
	t.Run("should end a launch pending during a project switch as interrupted and store it in the original project", func(t *testing.T) {
		held := make(chan domain.CheckpointItem, 1)
		h := startLaunchHarness(t,
			WithInterceptHandler(func(item domain.CheckpointItem) error { held <- item; return nil }),
		)
		original := h.repository
		t.Cleanup(func() { original.Close() })
		origin := startLaunchOrigin(t)
		h.proxy.SetIntercept(true)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/held"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		receiveCheckpointItem(t, held, "the launch to be held")
		dbConnection, err := db.New(":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("creating the next project's database: %v", err)
		}
		next := db.NewProxyRepo(dbConnection)

		h.proxy.SetProjectResources(ProjectResources{Repository: next, Extensions: h.proxy.Extensions, Scope: h.proxy.Scope})

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchFailed, Reason: domain.LaunchReasonInterrupted}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		h.assertStoredAsLaunched(t, id, "/held", map[string]any{"status": "failed", "reason": "interrupted"})
		if _, err := next.GetMetadata(id); err == nil {
			t.Fatalf("\nwanted:\nno pair %s in the newly opened project\ngot:\none", id)
		}
		select {
		case seen := <-origin.seen:
			t.Fatalf("\nwanted:\nno request at the origin\ngot:\n%s", seen.URL.Path)
		default:
		}
	})

	t.Run("should end a launch pending when the service stops as interrupted", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		upstream := origin.waitPath(t, "/slow")
		h.waitRequest(t, "/slow")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.proxy.CloseTransportContext(ctx); err != nil {
			t.Fatalf("CloseTransportContext() returned unexpected error: %v", err)
		}

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchFailed, Reason: domain.LaunchReasonInterrupted}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		select {
		case <-upstream.Context().Done():
		default:
			t.Fatal("wanted the upstream round trip to be abandoned")
		}
		h.assertStoredAsLaunched(t, id, "/slow", map[string]any{"status": "failed", "reason": "interrupted"})
	})

	t.Run("should keep a launch a person cancelled as cancelled", func(t *testing.T) {
		h := startLaunchHarness(t)
		origin := startLaunchOrigin(t)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/slow"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		origin.waitPath(t, "/slow")
		if _, err := h.proxy.CancelLaunch(id); err != nil {
			t.Fatalf("CancelLaunch() returned unexpected error: %v", err)
		}

		h.proxy.InterruptLaunches()

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCancelled}
		if got := h.waitFinished(t); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})
}

// emptyProject returns an in-memory project the launch harness has never written to.
func emptyProject(t *testing.T) *db.Repository {
	t.Helper()
	dbConnection, err := db.New(":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("creating another project's database: %v", err)
	}
	repository := db.NewProxyRepo(dbConnection)
	t.Cleanup(func() { repository.Close() })
	return repository
}

func TestProxy_LaunchReadableAfterProjectSwitch(t *testing.T) {
	t.Run("should read and refuse to cancel a launch interrupted by a project switch from either project", func(t *testing.T) {
		held := make(chan domain.CheckpointItem, 1)
		h := startLaunchHarness(t,
			WithInterceptHandler(func(item domain.CheckpointItem) error { held <- item; return nil }),
		)
		original := h.repository
		t.Cleanup(func() { original.Close() })
		origin := startLaunchOrigin(t)
		h.proxy.SetIntercept(true)
		id, err := h.proxy.Launch(rawGet(origin.Server, "/held"), h.launchpadID.String(), false)
		if err != nil {
			t.Fatalf("Launch() returned unexpected error: %v", err)
		}
		receiveCheckpointItem(t, held, "the launch to be held")
		next := emptyProject(t)

		h.proxy.SetProjectResources(ProjectResources{Repository: next, Extensions: h.proxy.Extensions, Scope: h.proxy.Scope})
		h.waitFinished(t)

		want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchFailed, Reason: domain.LaunchReasonInterrupted}
		if got, err := h.proxy.GetLaunch(id); err != nil || got != want {
			t.Fatalf("\nwanted:\nGetLaunch in the new project %+v, nil\ngot:\n%+v, %v", want, got, err)
		}
		if got, err := h.proxy.CancelLaunch(id); !errors.Is(err, domain.ErrLaunchFinished) || got != want {
			t.Fatalf("\nwanted:\nCancelLaunch in the new project %+v, %v\ngot:\n%+v, %v", want, domain.ErrLaunchFinished, got, err)
		}

		h.proxy.SetProjectResources(ProjectResources{Repository: original, Extensions: h.proxy.Extensions, Scope: h.proxy.Scope})

		if got, err := h.proxy.GetLaunch(id); err != nil || got != want {
			t.Fatalf("\nwanted:\nGetLaunch back in the original project %+v, nil\ngot:\n%+v, %v", want, got, err)
		}
	})

	t.Run("should keep only the most recently finished launches in memory", func(t *testing.T) {
		h := startLaunchHarness(t)
		original := h.repository
		t.Cleanup(func() { original.Close() })
		h.proxy.finishedLaunchLimit = 2
		origin := startLaunchOrigin(t)
		ids := make([]uuid.UUID, 0, 3)
		for _, path := range []string{"/first", "/second", "/third"} {
			id, err := h.proxy.Launch(rawGet(origin.Server, path), h.launchpadID.String(), false)
			if err != nil {
				t.Fatalf("Launch() returned unexpected error: %v", err)
			}
			h.waitFinished(t)
			ids = append(ids, id)
		}

		h.proxy.SetProjectResources(ProjectResources{Repository: emptyProject(t), Extensions: h.proxy.Extensions, Scope: h.proxy.Scope})

		if _, err := h.proxy.GetLaunch(ids[0]); !errors.Is(err, domain.ErrLaunchNotFound) {
			t.Fatalf("\nwanted:\nGetLaunch oldest %v\ngot:\n%v", domain.ErrLaunchNotFound, err)
		}
		for _, id := range ids[1:] {
			want := domain.Launch{ID: id, LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
			if got, err := h.proxy.GetLaunch(id); err != nil || got != want {
				t.Fatalf("\nwanted:\nGetLaunch %+v, nil\ngot:\n%+v, %v", want, got, err)
			}
		}

		h.proxy.SetProjectResources(ProjectResources{Repository: original, Extensions: h.proxy.Extensions, Scope: h.proxy.Scope})

		want := domain.Launch{ID: ids[0], LaunchpadID: h.launchpadID, Status: domain.LaunchCompleted}
		if got, err := h.proxy.GetLaunch(ids[0]); err != nil || got != want {
			t.Fatalf("\nwanted:\nGetLaunch oldest from its stored pair %+v, nil\ngot:\n%+v, %v", want, got, err)
		}
	})
}
