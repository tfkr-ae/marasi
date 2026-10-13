package marasi

import (
	"crypto/sha1"
	"encoding/base64"
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
)

type launchHarness struct {
	proxy       *Proxy
	repository  *db.Repository
	launchpadID uuid.UUID
	finished    chan domain.Launch
	requests    chan domain.ProxyRequest
}

// startLaunchHarness serves a real proxy over an in-memory project with one launchpad.
func startLaunchHarness(t *testing.T) *launchHarness {
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
	proxy, err := New(
		WithConfigDir(t.TempDir()),
		WithDefaultRepositories(repository),
		WithExtensions(defaultExtensions),
		WithRequestHandler(func(req domain.ProxyRequest) error { requests <- req; return nil }),
		WithResponseHandler(func(domain.ProxyResponse) error { return nil }),
		WithLogHandler(func(domain.Log) error { return nil }),
		WithLaunchFinishedHandler(func(launch domain.Launch) { finished <- launch }),
		WithBasePipeline(),
		WithDefaultModifierPipeline(),
	)
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
	members, err := h.repository.GetLaunchpadRequests(h.launchpadID)
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
		found, _, _, err := h.repository.ListTraffic(nil, 10, `metadata.launch.status = "completed"`)
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
