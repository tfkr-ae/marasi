package marasi

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/martian"
	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// marasiHost is the host of Marasi's own pages.
const marasiHost = "marasi"

// defaultBrowserViewExpiry is how long a pending browser view waits for the
// browser to follow its redirect (ADR-0055).
const defaultBrowserViewExpiry = 10 * time.Second

// ownResponseKey holds the response the proxy answers a request with itself.
type ownResponseKey struct{}

// respondFromProxy makes res the answer to req. The round trip is skipped,
// and the base pipeline sends res in place of martian's blank response without
// running the response modifiers, so nothing is captured, held, or shown to an
// extension. It returns ErrSkipPipeline for the request modifier to return.
func respondFromProxy(req *http.Request, res *http.Response) error {
	*req = *req.WithContext(context.WithValue(req.Context(), ownResponseKey{}, res))
	martian.NewContext(req).SkipRoundTrip()
	return ErrSkipPipeline
}

// ownResponse returns the response a request modifier answered req with.
func ownResponse(req *http.Request) (*http.Response, bool) {
	res, ok := req.Context().Value(ownResponseKey{}).(*http.Response)
	return res, ok
}

// MarasiPagesModifier answers requests for Marasi's own pages and requests
// that use a pending browser view. It runs first in the request pipeline, so
// neither is ever captured, held by Checkpoint, or seen by an extension
// (ADR-0055).
func MarasiPagesModifier(proxy *Proxy, req *http.Request) error {
	if req.Method == http.MethodConnect {
		return nil
	}
	if res := proxy.marasiPage(req); res != nil {
		return respondFromProxy(req, res)
	}
	if isSelfClient(req) {
		return nil
	}
	if res := proxy.browserViews.take(clientHost(req.RemoteAddr), requestURLKey(req)); res != nil {
		res.Request = req
		return respondFromProxy(req, res)
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
	if _, err := readStoredResponse(stored); err != nil {
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

// isSelfClient reports whether req came from the proxy's own client, as
// launches and Armory runs do.
func isSelfClient(req *http.Request) bool {
	admission, _ := req.Context().Value(projectAdmissionKey{}).(projectAdmission)
	return admission.internal
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
func (views *browserViews) take(client, url string) *http.Response {
	views.mu.Lock()
	defer views.mu.Unlock()
	if len(views.pending) == 0 {
		return nil
	}
	views.sweep(time.Now())
	key := browserViewKey{client: client, url: url}
	view, ok := views.pending[key]
	if !ok {
		return nil
	}
	delete(views.pending, key)
	// register's caller has already read this response once.
	res, _ := readStoredResponse(view.response)
	return res
}

// readStoredResponse parses a stored raw response.
func readStoredResponse(raw []byte) (*http.Response, error) {
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
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
