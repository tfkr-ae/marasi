package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// ControlListenerStatus is the last completed state of the control
// listener's network address. ControlListener is nil when it is inactive.
// PasswordSet says whether a control password guards the address; the
// password itself is never part of the status.
type ControlListenerStatus struct {
	Status          ListenerState `json:"status"`
	ControlListener *string       `json:"control_listener"`
	PasswordSet     bool          `json:"password_set"`
}

// ControlAddress is the control listener's network address: one TCP
// listener that serves the same control API as the socket, behind the
// optional control password
// (docs/adr/2026-11-03-the-control-listener-network-address-is-guarded-by-an-optional-password.md).
// Lifecycle changes are serialized by mu.
type ControlAddress struct {
	api       http.Handler
	password  *ControlPassword
	logWriter io.Writer
	events    *eventBroadcaster
	mu        sync.Mutex
	run       *controlAddressRun
	closed    bool // Shutdown ran, so Start must not bind again
	// retiring counts runs that Stop or Update closed and that are still
	// finishing the request that closed them.
	retiring sync.WaitGroup
	statusMu sync.RWMutex
	status   ControlListenerStatus
	// connsMu guards conns, every open connection on the network address
	// with the control password generation it last authenticated with.
	connsMu sync.Mutex
	conns   map[net.Conn]*networkConn
}

// networkConn is one open connection on the network address. authenticated
// is false until a request on it passes the password check.
type networkConn struct {
	conn          net.Conn
	run           *controlAddressRun
	authenticated bool
	generation    uint64
}

type networkConnKey struct{}

// controlAddressRun is one bound network address and the server on it.
type controlAddressRun struct {
	listener  net.Listener
	server    *http.Server
	served    chan error
	suspended bool // listener closed for a restart handover
}

// controlRefusalBody is the standard JSON error body with a message.
type controlRefusalBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// writeRefusal writes a refusal whose message names its cause, so a client
// can show why the command failed rather than only the code.
func writeRefusal(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, r, status, controlRefusalBody{Error: code, Message: message})
}

// EnableControlAddress gives the instance a network address for its control
// listener, inactive until Start, guarded by password when it is not empty.
// Call it once, before serving.
func (s *Server) EnableControlAddress(password string, logWriter io.Writer) *ControlAddress {
	if logWriter == nil {
		logWriter = io.Discard
	}
	control := &ControlAddress{
		api:       s,
		password:  NewControlPassword(password),
		logWriter: logWriter,
		events:    s.events,
		status:    ControlListenerStatus{Status: ListenerInactive},
		conns:     map[net.Conn]*networkConn{},
	}
	s.control = control
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("GET /status", s.serveControlListenerStatus)
	controlMux.HandleFunc("POST /start", s.serveControlListenerStart)
	controlMux.HandleFunc("POST /update", s.serveControlListenerUpdate)
	controlMux.HandleFunc("POST /stop", s.serveControlListenerStop)
	controlMux.HandleFunc("GET /pairing", s.servePairing)
	s.mux.Handle("/service/control-listener/", http.StripPrefix("/service/control-listener", controlMux))
	return control
}

// Password returns the control password, "" when none is set.
func (c *ControlAddress) Password() string {
	return c.password.Value()
}

// setPassword replaces the control password, or removes it when password is
// empty, and reports whether it changed. A change closes every connection on
// the network address that authenticated under the old password, which also
// ends its event streams. The connection of the request with context ctx is
// spared so it gets its response, and closes after it.
func (c *ControlAddress) setPassword(ctx context.Context, password string) bool {
	generation, changed := c.password.set(password)
	if !changed {
		return false
	}
	trigger, _ := ctx.Value(networkConnKey{}).(*networkConn)
	c.connsMu.Lock()
	defer c.connsMu.Unlock()
	for _, tracked := range c.conns {
		if tracked != trigger && tracked.authenticated && tracked.generation < generation {
			_ = tracked.conn.Close()
		}
	}
	return true
}

// trackConn records a new connection on the network address and returns
// its context, which carries the tracked connection.
func (c *ControlAddress) trackConn(ctx context.Context, conn net.Conn, run *controlAddressRun) context.Context {
	tracked := &networkConn{conn: conn, run: run}
	c.connsMu.Lock()
	c.conns[conn] = tracked
	c.connsMu.Unlock()
	return context.WithValue(ctx, networkConnKey{}, tracked)
}

// forgetConn stops tracking a connection once the server is done with it.
func (c *ControlAddress) forgetConn(conn net.Conn, state http.ConnState) {
	if state != http.StateClosed && state != http.StateHijacked {
		return
	}
	c.connsMu.Lock()
	delete(c.conns, conn)
	c.connsMu.Unlock()
}

// authenticateConn records that r's connection passed the check against the
// password of generation. It reports false, and closes the connection, when
// the password changed after the check, so a request cannot slip past a
// change's sweep.
func (c *ControlAddress) authenticateConn(r *http.Request, generation uint64) bool {
	tracked, ok := r.Context().Value(networkConnKey{}).(*networkConn)
	if !ok {
		return true
	}
	c.connsMu.Lock()
	defer c.connsMu.Unlock()
	tracked.authenticated = true
	tracked.generation = generation
	if generation != c.password.currentGeneration() {
		_ = tracked.conn.Close()
		return false
	}
	return true
}

// controlListenerAddress returns the network address of the control
// listener, or nil when it has none.
func (s *Server) controlListenerAddress() *string {
	if s.control == nil {
		return nil
	}
	return s.control.Status().ControlListener
}

// Status returns the last completed state of the network address.
func (c *ControlAddress) Status() ControlListenerStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	status := c.status
	if status.ControlListener != nil {
		address := *status.ControlListener
		status.ControlListener = &address
	}
	return c.withPassword(status)
}

// withPassword fills in whether a password is set, which can change without
// the address changing.
func (c *ControlAddress) withPassword(status ControlListenerStatus) ControlListenerStatus {
	status.PasswordSet = c.password.IsSet()
	return status
}

func (c *ControlAddress) setStatus(status ControlListenerStatus) {
	c.statusMu.Lock()
	c.status = status
	c.statusMu.Unlock()
}

// Start binds the network address in settings and serves the control API
// there. Port 0 binds a free port, and the status reports the bound one.
// password replaces the control password, "" removes it, and nil keeps it.
func (c *ControlAddress) Start(ctx context.Context, settings ListenerSettings, password *string) (ControlListenerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ControlListenerStatus{}, listenerFailure{kind: ErrListenerUnavailable, err: errControlAddressClosed}
	}
	if c.run != nil {
		return ControlListenerStatus{}, ErrListenerAlreadyActive
	}
	endpoint, err := listenerEndpoint(settings)
	if err != nil {
		return ControlListenerStatus{}, err
	}
	run, err := c.bind(ctx, endpoint)
	if err != nil {
		return ControlListenerStatus{}, err
	}
	c.run = run
	if password != nil {
		c.setPassword(ctx, *password)
	}
	status := c.activeStatus(run)
	c.setStatus(status)
	c.events.publish("control_listener.started", status)
	return status, nil
}

// Shutdown closes the network address when the instance stops. Requests in
// flight get until ctx ends to finish, and are then cut off.
func (c *ControlAddress) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	run := c.run
	c.run = nil
	c.closed = true
	c.mu.Unlock()
	// Wait with mu released, since a request in flight may need it to finish
	// (CODING_STANDARDS.md, "Release before waiting"). With run gone and
	// closed set, nothing adds to retiring any more.
	defer c.retiring.Wait()
	if run == nil {
		return nil
	}
	c.setStatus(ControlListenerStatus{Status: ListenerInactive})
	shutdownErr := run.server.Shutdown(ctx)
	var closeErr error
	if shutdownErr != nil {
		closeErr = run.server.Close()
	}
	serveErr := <-run.served
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(
		wrapError("shutting down control listener network address", shutdownErr),
		wrapError("closing control listener network address", closeErr),
		wrapError("serving control listener network address", serveErr),
	)
}

// suspendForHandover stops accepting on the network address, freeing it for
// a successor to bind, and returns the address it was on, or nil when it is
// inactive. Connections already accepted keep being served, so a restart
// requested over the network address still gets its response
// (docs/adr/2026-10-18-restart-hands-over-and-rolls-back.md).
func (c *ControlAddress) suspendForHandover() (*string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil || c.run.suspended {
		return nil, nil
	}
	run := c.run
	address := run.listener.Addr().String()
	closeErr := run.listener.Close()
	// Serve returns once its listener closes, without touching the
	// connections it accepted.
	serveErr := <-run.served
	run.served <- nil
	run.suspended = true
	if errors.Is(serveErr, net.ErrClosed) {
		serveErr = nil
	}
	return &address, errors.Join(
		wrapError("closing control listener network address", closeErr),
		wrapError("serving control listener network address", serveErr),
	)
}

// resumeAfterHandover accepts on the network address again after a failed
// handover, at the address it was suspended on. If that address cannot be
// bound, the network address becomes inactive.
func (c *ControlAddress) resumeAfterHandover(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil || !c.run.suspended {
		return nil
	}
	run := c.run
	endpoint := run.listener.Addr().String()
	listener, err := (&net.ListenConfig{}).Listen(ctx, controlAddressNetwork(endpoint), endpoint)
	if err != nil {
		fmt.Fprintf(c.logWriter, "binding control listener network address on %s: %v\n", endpoint, err)
		c.run = nil
		c.setStatus(ControlListenerStatus{Status: ListenerInactive})
		// Shut down in the background: the request that asked for the
		// restart may be in flight on this server and must still get its
		// response. Shutdown waits for it through retiring.
		c.retiring.Add(1)
		go func() {
			defer c.retiring.Done()
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlRetireTimeout)
			defer cancel()
			if err := run.server.Shutdown(shutdownCtx); err != nil {
				_ = run.server.Close()
			}
		}()
		return listenerFailure{kind: ErrListenerUnavailable, err: err}
	}
	<-run.served
	run.listener = listener
	run.suspended = false
	go func() { run.served <- run.server.Serve(listener) }()
	return nil
}

// bind listens on endpoint and starts serving the control API there.
func (c *ControlAddress) bind(ctx context.Context, endpoint string) (*controlAddressRun, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, controlAddressNetwork(endpoint), endpoint)
	if err != nil {
		fmt.Fprintf(c.logWriter, "binding control listener network address on %s: %v\n", endpoint, err)
		return nil, listenerFailure{kind: ErrListenerUnavailable, err: err}
	}
	address := listener.Addr().String()
	if warning := ControlAddressWarning(address); warning != "" {
		fmt.Fprintln(c.logWriter, warning)
	}
	server := &http.Server{
		Handler: c.networkHandler(address),
		// Every connection is tracked with its run, so a password change
		// can drop it and so can a Stop or Update that closes its run.
		ConnState: c.forgetConn,
	}
	run := &controlAddressRun{listener: listener, server: server, served: make(chan error, 1)}
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		return c.trackConn(ctx, conn, run)
	}
	go func() { run.served <- server.Serve(listener) }()
	return run, nil
}

// controlAddressNetwork keeps an IPv4 host on IPv4, so 0.0.0.0 binds every
// IPv4 interface and is reported as asked rather than as [::].
func controlAddressNetwork(endpoint string) string {
	host, _, err := net.SplitHostPort(endpoint)
	if err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			return "tcp4"
		}
	}
	return "tcp"
}

// networkHandler is the control API as the network address serves it. The
// socket serves c.api with none of these checks. Each check runs before the
// next: CORS and the Host check belong in front of the control password, so
// a preflight and a rebound name are answered before authentication.
func (c *ControlAddress) networkHandler(bound string) http.Handler {
	return allowAnyOrigin(c.requireBoundHost(bound, c.requireControlPassword(c.api)))
}

// defaultHTTPPort is the port a Host header without one names.
const defaultHTTPPort = "80"

// requireBoundHost refuses a request whose Host header does not name bound,
// the address the network address listens on, so a hostile page whose name
// was rebound to the instance machine cannot reach the instance. On a wildcard
// address any Host naming the bound port is accepted, because the operator
// chose every interface. On loopback, localhost is accepted too: a rebound
// name never arrives as localhost. With a password set, any Host is
// accepted, because a rebound page without the password gets nothing.
func (c *ControlAddress) requireBoundHost(bound string, next http.Handler) http.Handler {
	boundHost, boundPort, _ := net.SplitHostPort(bound)
	boundIP := net.ParseIP(boundHost)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.password.IsSet() {
			next.ServeHTTP(w, r)
			return
		}
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil {
			host, port = r.Host, defaultHTTPPort
		}
		if port != boundPort || !hostNamesBound(host, boundIP) {
			writeJSON(w, r, http.StatusMisdirectedRequest, controlRefusalBody{Error: "host_mismatch", Message: "the Host header does not name the control listener network address"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostNamesBound reports whether host, from a Host header without its port,
// names the bound IP address.
func hostNamesBound(host string, bound net.IP) bool {
	if bound == nil {
		return false
	}
	if bound.IsUnspecified() {
		return true
	}
	if bound.IsLoopback() && strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.Equal(bound)
}

// The CORS preflight answer: the methods the control API uses, and the
// request headers an authenticated JSON call sends.
const (
	controlAllowedMethods = "GET, POST, PUT, DELETE"
	controlAllowedHeaders = "Authorization, Content-Type"
)

// allowAnyOrigin lets a page from any origin call the network address,
// because the control password alone decides access. It echoes the request
// Origin and never allows credentials, so a browser attaches nothing
// ambient to a request. A preflight is answered here, without the password,
// because a browser never sends one on a preflight.
func allowAnyOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", controlAllowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", controlAllowedHeaders)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireControlPassword refuses a request that does not carry the control
// password as Authorization: Bearer, when one is set. The password is never
// read from the query string, because URLs end up in logs and history.
func (c *ControlAddress) requireControlPassword(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		generation, admitted := c.password.admits(bearerToken(r.Header.Get("Authorization")))
		if !admitted {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, r, http.StatusUnauthorized, controlRefusalBody{Error: "unauthorized", Message: "send the control password as Authorization: Bearer"})
			return
		}
		if !c.authenticateConn(r, generation) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken returns the credentials of an Authorization header that uses
// the Bearer scheme, whose name is case-insensitive.
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// ControlAddressWarning returns the warning line for a control listener
// network address bound at address, or "" when address is loopback. The
// network address serves plain HTTP, so anything beyond loopback can read
// the control password on the wire.
func ControlAddressWarning(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return fmt.Sprintf("warning: control listener network address %s is reachable beyond loopback and serves plain HTTP without TLS; protect it with an SSH tunnel, a VPN, or a TLS reverse proxy", address)
}
