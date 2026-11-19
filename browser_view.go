package marasi

import (
	"bufio"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
)

// marasiHost is the host of Marasi's own pages.
const marasiHost = "marasi"

// defaultBrowserViewExpiry is how long a pending browser view waits for the
// browser to follow its redirect (docs/adr/2026-10-20-a-browser-view-serves-the-stored-response-once-at-its-original-url.md).
const defaultBrowserViewExpiry = 10 * time.Second

// MarasiPagesModifier answers requests for Marasi's own pages and requests
// that use a pending browser view through Respond. It runs first in the
// request pipeline, and a response from Respond skips the response chain, so
// neither is ever captured, held by Checkpoint, or seen by an extension
// (docs/adr/2026-10-20-a-browser-view-serves-the-stored-response-once-at-its-original-url.md).
func MarasiPagesModifier(proxy *Proxy, pair *Pair) error {
	req := pair.Request
	if res := proxy.marasiPage(req); res != nil {
		pair.Respond(res)
		return nil
	}
	if isSelfClient(pair) {
		return nil
	}
	if res, wire := proxy.browserViews.take(clientHost(req.RemoteAddr), requestURLKey(req)); res != nil {
		res.Request = req
		pair.Respond(res)
		pair.storedWire = wire
		return nil
	}
	return nil
}

// marasiPage returns the answer to a request for one of Marasi's own pages,
// or nil when req is for another host.
func (proxy *Proxy) marasiPage(req *http.Request) *http.Response {
	host := req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !strings.EqualFold(host, marasiHost) {
		return nil
	}
	if req.URL.Path == "/cert" {
		return proxy.certificatePage(req)
	}
	if id, ok := strings.CutPrefix(req.URL.Path, "/view/"); ok {
		return proxy.browserViewPage(req, id)
	}
	return plainTextResponse(req, http.StatusNotFound, "Marasi has no page at "+req.URL.Path)
}

// certificatePage serves the CA certificate in DER form.
func (proxy *Proxy) certificatePage(req *http.Request) *http.Response {
	if proxy.Cert == nil {
		return plainTextResponse(req, http.StatusServiceUnavailable, "this service instance has no CA certificate loaded")
	}
	res := newResponse(req, http.StatusOK, proxy.Cert.Raw)
	res.Header.Set("Content-Type", "application/x-x509-ca-cert")
	res.Header.Set("Content-Disposition", "attachment; filename=\"marasi-cert.der\"")
	return res
}

// browserViewPage registers a pending browser view of the pair with the given
// UUID and redirects the browser to the pair's original URL. Errors are plain
// text and register nothing.
func (proxy *Proxy) browserViewPage(req *http.Request, rawID string) *http.Response {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		res := plainTextResponse(req, http.StatusMethodNotAllowed, "a browser view is opened with GET")
		res.Header.Set("Allow", "GET, HEAD")
		return res
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return plainTextResponse(req, http.StatusNotFound, fmt.Sprintf("%q is not a pair UUID", rawID))
	}
	if proxy.TrafficRepo == nil {
		return plainTextResponse(req, http.StatusServiceUnavailable, "no project is open")
	}
	row, err := proxy.TrafficRepo.GetRequestResponseRow(id)
	if errors.Is(err, sql.ErrNoRows) {
		return plainTextResponse(req, http.StatusNotFound, fmt.Sprintf("no pair %s in the open project", id))
	}
	if err != nil {
		return plainTextResponse(req, http.StatusInternalServerError, fmt.Sprintf("reading pair %s: %v", id, err))
	}
	if len(row.Response.Raw) == 0 {
		return plainTextResponse(req, http.StatusConflict, fmt.Sprintf("pair %s has no response: it may be held in Checkpoint, dropped, cancelled, or still in flight", id))
	}
	if row.Response.StatusCode == http.StatusSwitchingProtocols {
		return plainTextResponse(req, http.StatusConflict, fmt.Sprintf("pair %s is a WebSocket upgrade, which a browser cannot be shown", id))
	}

	// A copy of the stored response, so a use never reads the project.
	stored := bytes.Clone(row.Response.Raw)
	if _, _, err := readStoredResponse(stored); err != nil {
		return plainTextResponse(req, http.StatusInternalServerError, fmt.Sprintf("reading the stored response of pair %s: %v", id, err))
	}
	target := pairURL(&row.Request)
	proxy.browserViews.register(clientHost(req.RemoteAddr), urlKey(target), stored)

	res := newResponse(req, http.StatusFound, nil)
	res.Header.Set("Location", target.String())
	res.Header.Set("Cache-Control", "no-store")
	return res
}

// pairURL returns a pair's original URL. The path and query come from the
// stored request line, which keeps their escaping, and fall back to the
// stored path.
func pairURL(request *domain.ProxyRequest) *url.URL {
	target := &url.URL{Scheme: request.Scheme, Host: request.Host}
	requestURI := request.Path
	if line, _, ok := bytes.Cut(request.Raw, []byte("\r\n")); ok {
		if fields := strings.Fields(string(line)); len(fields) == 3 {
			if parsed, err := url.ParseRequestURI(fields[1]); err == nil {
				requestURI = parsed.RequestURI()
			}
		}
	}
	path, query, _ := strings.Cut(requestURI, "?")
	if unescaped, err := url.PathUnescape(path); err == nil {
		target.Path = unescaped
		target.RawPath = path
	} else {
		target.Path = path
	}
	target.RawQuery = query
	return target
}

// requestURLKey returns the browser view key of the URL req asks for.
func requestURLKey(req *http.Request) string {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	return urlKey(&url.URL{Scheme: req.URL.Scheme, Host: host, Path: req.URL.Path, RawPath: req.URL.RawPath, RawQuery: req.URL.RawQuery})
}

// urlKey returns u as scheme://host:port/path?query, with the scheme and host
// in lower case and the default port written out.
func urlKey(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
		port = "80"
		if scheme == "https" {
			port = "443"
		}
	}
	requestURI := u.EscapedPath()
	if requestURI == "" {
		requestURI = "/"
	}
	if u.RawQuery != "" {
		requestURI += "?" + u.RawQuery
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(host), port) + requestURI
}

// clientHost returns the host part of a client address.
func clientHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// isSelfClient reports whether pair came from the proxy's own client, as
// launches and Armory runs do.
func isSelfClient(pair *Pair) bool {
	return pair.admission != nil && pair.admission.internal
}

func newResponse(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func plainTextResponse(req *http.Request, status int, message string) *http.Response {
	res := newResponse(req, status, []byte(message+"\n"))
	res.Header.Set("Content-Type", "text/plain; charset=utf-8")
	return res
}

// browserViewKey names a pending browser view: the host part of the client
// address that opened it and the key of the pair's original URL.
type browserViewKey struct {
	client string
	url    string
}

type pendingBrowserView struct {
	response []byte // the stored raw response
	expires  time.Time
}

// browserViews holds the pending browser views of a service instance. Each
// one is used by the first matching request and expires unused after expiry.
// It is not stored in the project. Expired entries are swept on each use.
type browserViews struct {
	mu      sync.Mutex
	expiry  time.Duration // 0 means defaultBrowserViewExpiry
	pending map[browserViewKey]pendingBrowserView
}

// register adds a pending browser view, replacing one with the same key.
func (views *browserViews) register(client, url string, response []byte) {
	views.mu.Lock()
	defer views.mu.Unlock()
	now := time.Now()
	views.sweep(now)
	if views.pending == nil {
		views.pending = make(map[browserViewKey]pendingBrowserView)
	}
	expiry := views.expiry
	if expiry == 0 {
		expiry = defaultBrowserViewExpiry
	}
	views.pending[browserViewKey{client: client, url: url}] = pendingBrowserView{response: response, expires: now.Add(expiry)}
}

// take uses up the pending browser view for client and url and returns its
// stored response, or nil when there is none.
func (views *browserViews) take(client, url string) (*http.Response, *wireResponse) {
	views.mu.Lock()
	defer views.mu.Unlock()
	if len(views.pending) == 0 {
		return nil, nil
	}
	views.sweep(time.Now())
	key := browserViewKey{client: client, url: url}
	view, ok := views.pending[key]
	if !ok {
		return nil, nil
	}
	delete(views.pending, key)
	// register's caller has already read this response once.
	res, wire, _ := readStoredResponse(view.response)
	return res, wire
}

// readStoredResponse reads a stored raw response with the codec: its head
// as stored, and the rest as the payload, which storage keeps with any
// chunked coding removed. The engine writes the head as stored and frames
// the payload by it (outboundResponse).
func readStoredResponse(raw []byte) (*http.Response, *wireResponse, error) {
	br := bufio.NewReader(bytes.NewReader(storedAsHTTP1(raw)))
	head, err := h1.ReadResponseHead(br)
	if err != nil {
		return nil, nil, err
	}
	payload, err := io.ReadAll(br)
	if err != nil {
		return nil, nil, err
	}
	res := head.Response()
	if len(payload) > 0 {
		res.Body = io.NopCloser(bytes.NewReader(payload))
	}
	return res, &wireResponse{head: head}, nil
}

// storedAsHTTP1 turns a response stored in the HTTP/2 text form
// (docs/adr/2026-11-19-marasi-owns-its-http2-framing.md) into one the HTTP/1.1
// codec reads: HTTP/2 <status> becomes an HTTP/1.1 status line, and lines
// that start with ":" are skipped. Any other response is returned as it is.
func storedAsHTTP1(raw []byte) []byte {
	status, rest, ok := bytes.Cut(raw, []byte("\r\n"))
	code, isH2 := bytes.CutPrefix(status, []byte("HTTP/2 "))
	if !ok || !isH2 {
		return raw
	}
	head, body, _ := bytes.Cut(rest, []byte("\r\n\r\n"))
	n, _ := strconv.Atoi(string(code))
	out := []byte("HTTP/1.1 " + string(code) + " " + http.StatusText(n) + "\r\n")
	for line := range bytes.SplitSeq(head, []byte("\r\n")) {
		if len(line) > 0 && line[0] != ':' {
			out = append(append(out, line...), "\r\n"...)
		}
	}
	out = append(out, "\r\n"...)
	return append(out, body...)
}

// clear drops every pending browser view.
func (views *browserViews) clear() {
	views.mu.Lock()
	defer views.mu.Unlock()
	views.pending = nil
}

// sweep drops expired views. The caller holds mu.
func (views *browserViews) sweep(now time.Time) {
	for key, view := range views.pending {
		if !now.Before(view.expires) {
			delete(views.pending, key)
		}
	}
}
