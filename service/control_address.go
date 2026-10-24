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
type ControlListenerStatus struct {
	Status          ListenerState `json:"status"`
	ControlListener *string       `json:"control_listener"`
}

// ControlAddress is the control listener's network address: one TCP
// listener that serves the same control API as the socket, behind the
// control token (docs/adr/2026-10-24-the-control-listener-network-address-is-guarded-by-a-token-alone.md).
// Lifecycle changes are serialized by mu.
type ControlAddress struct {
	api       http.Handler
	token     *ControlToken
	logWriter io.Writer
	mu        sync.Mutex
	run       *controlAddressRun
	statusMu  sync.RWMutex
	status    ControlListenerStatus
}

// controlAddressRun is one bound network address and the server on it.
type controlAddressRun struct {
	listener net.Listener
	server   *http.Server
	served   chan error
}

// arrival says which way a control request reached the control listener.
type arrival uint8

const (
	arrivedOnSocket arrival = iota
	arrivedOnNetworkAddress
)

type arrivalKey struct{}

// requestArrival returns which way r reached the control listener. A
// request with no mark arrived on the socket.
func requestArrival(r *http.Request) arrival {
	if value, ok := r.Context().Value(arrivalKey{}).(arrival); ok {
		return value
	}
	return arrivedOnSocket
}

// controlRefusalBody is the standard JSON error body with a message.
type controlRefusalBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// EnableControlAddress gives the instance a network address for its control
// listener, inactive until Start, and serves the socket-only control token
// routes. Call it once, before serving.
func (s *Server) EnableControlAddress(token *ControlToken, logWriter io.Writer) *ControlAddress {
	if logWriter == nil {
		logWriter = io.Discard
	}
	control := &ControlAddress{
		api:       s,
		token:     token,
		logWriter: logWriter,
		status:    ControlListenerStatus{Status: ListenerInactive},
	}
	s.control = control
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("GET /token", s.serveControlToken)
	s.mux.Handle("/service/control-listener/", http.StripPrefix("/service/control-listener", controlMux))
	return control
}

// serveControlToken returns the control token, only to a request that
// arrived on the socket, so a stolen token cannot read itself back.
func (s *Server) serveControlToken(w http.ResponseWriter, r *http.Request) {
	if requestArrival(r) == arrivedOnNetworkAddress {
		writeJSON(w, r, http.StatusForbidden, controlRefusalBody{Error: "socket_only", Message: "the control token is available only through the socket"})
		return
	}
	value, err := s.control.token.Value()
	if err != nil {
		fmt.Fprintf(s.control.logWriter, "reading control token: %v\n", err)
		writeJSON(w, r, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{Error: "internal_server_error"})
		return
	}
	writeJSON(w, r, http.StatusOK, struct {
		Token string `json:"token"`
	}{Token: value})
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
	return status
}

func (c *ControlAddress) setStatus(status ControlListenerStatus) {
	c.statusMu.Lock()
	c.status = status
	c.statusMu.Unlock()
}

// Start binds the network address in settings and serves the control API
// there. Port 0 binds a free port, and the status reports the bound one. The
// control token is generated now if its file is absent.
func (c *ControlAddress) Start(ctx context.Context, settings ListenerSettings) (ControlListenerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil {
		return ControlListenerStatus{}, ErrListenerAlreadyActive
	}
	endpoint, err := listenerEndpoint(settings)
	if err != nil {
		return ControlListenerStatus{}, err
	}
	if _, err := c.token.Value(); err != nil {
		return ControlListenerStatus{}, err
	}
	run, err := c.bind(ctx, endpoint)
	if err != nil {
		return ControlListenerStatus{}, err
	}
	c.run = run
	address := run.listener.Addr().String()
	status := ControlListenerStatus{Status: ListenerActive, ControlListener: &address}
	c.setStatus(status)
	return status, nil
}

// Shutdown closes the network address when the instance stops. Requests in
// flight get until ctx ends to finish, and are then cut off.
func (c *ControlAddress) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return nil
	}
	run := c.run
	c.run = nil
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
		// Every request on this server is marked as arriving on the
		// network address, so socket-only routes can refuse it.
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, arrivalKey{}, arrivedOnNetworkAddress)
		},
	}
	run := &controlAddressRun{listener: listener, server: server, served: make(chan error, 1)}
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
// next: CORS and the Host check belong in front of the control token, so a
// preflight and a rebound name are answered before authentication.
func (c *ControlAddress) networkHandler(bound string) http.Handler {
	return allowAnyOrigin(requireBoundHost(bound, c.requireControlToken(c.api)))
}

// defaultHTTPPort is the port a Host header without one names.
const defaultHTTPPort = "80"

// requireBoundHost refuses a request whose Host header does not name bound,
// the address the network address listens on, so a hostile page whose name
// was rebound to this machine cannot reach the instance. On a wildcard
// address any Host naming the bound port is accepted, because the operator
// chose every interface. On loopback, localhost is accepted too: a rebound
// name never arrives as localhost.
func requireBoundHost(bound string, next http.Handler) http.Handler {
	boundHost, boundPort, _ := net.SplitHostPort(bound)
	boundIP := net.ParseIP(boundHost)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
// because the control token alone decides access. It echoes the request
// Origin and never allows credentials, so a browser attaches nothing
// ambient to a request. A preflight is answered here, without the token,
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

// requireControlToken refuses a request that does not carry the control
// token as Authorization: Bearer. The token is never read from the query
// string, because URLs end up in logs and history.
func (c *ControlAddress) requireControlToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || !c.token.matches(presented) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, r, http.StatusUnauthorized, controlRefusalBody{Error: "unauthorized", Message: "send the control token as Authorization: Bearer"})
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
// the control token on the wire.
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
