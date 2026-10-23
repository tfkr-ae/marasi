// Package upstreamtest provides fake upstream proxies for tests. Each fake
// records what it was asked to reach, the credentials it was shown and the
// bytes it carried, and can refuse, reject credentials or hang. The HTTP
// CONNECT fake lives here; SOCKS5 and HTTPS fakes sit beside it and share
// Tunnel, Behaviour and Splice.
package upstreamtest

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Behaviour says how a fake upstream proxy answers.
type Behaviour int

const (
	// Carry carries each tunnel to its target.
	Carry Behaviour = iota
	// Refuse closes its listener, so every connection is refused.
	Refuse
	// RejectCredentials answers 407 (or the SOCKS5 equivalent) when the
	// credentials shown differ from the ones the fake wants.
	RejectCredentials
	// Hang accepts connections and never answers.
	Hang
)

// Tunnel is one tunnel a client asked a fake upstream proxy for.
type Tunnel struct {
	// Target is the host:port the client asked for, as sent.
	Target string
	// Username and Password are the credentials shown, or empty.
	Username string
	Password string
	// HasCredentials reports whether any credentials were shown.
	HasCredentials bool
	// Request is the CONNECT request head, for HTTP fakes.
	Request *http.Request
	// AddressType is the SOCKS5 address type of Target (AddressIPv4,
	// AddressDomain or AddressIPv6), for SOCKS5 fakes.
	AddressType byte
	// Methods are the SOCKS5 authentication methods the client offered,
	// for SOCKS5 fakes.
	Methods []byte

	mu   sync.Mutex
	sent []byte
}

// Sent returns the bytes the client has sent through the tunnel so far.
func (tunnel *Tunnel) Sent() []byte {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	return append([]byte(nil), tunnel.sent...)
}

func (tunnel *Tunnel) Write(p []byte) (int, error) {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	tunnel.sent = append(tunnel.sent, p...)
	return len(p), nil
}

// Proxy is a fake upstream proxy listening on a loopback port.
type Proxy struct {
	// Addr is the fake's host:port.
	Addr string

	listener  net.Listener
	behaviour Behaviour
	username  string
	password  string
	hosts     map[string]string
	wg        sync.WaitGroup

	mu      sync.Mutex
	tunnels []*Tunnel
	conns   map[net.Conn]struct{}
	closed  bool
}

// Option configures a fake upstream proxy.
type Option func(*Proxy)

// WithBehaviour sets how the fake answers. The default is Carry.
func WithBehaviour(behaviour Behaviour) Option {
	return func(proxy *Proxy) { proxy.behaviour = behaviour }
}

// WithCredentials makes the fake want username and password. With
// RejectCredentials, any other credentials are rejected.
func WithCredentials(username, password string) Option {
	return func(proxy *Proxy) { proxy.username, proxy.password = username, password }
}

// WithHost makes the fake resolve name to address itself, as an internal
// DNS name only the upstream proxy can resolve.
func WithHost(name, address string) Option {
	return func(proxy *Proxy) {
		if proxy.hosts == nil {
			proxy.hosts = map[string]string{}
		}
		proxy.hosts[name] = address
	}
}

// NewHTTP starts a fake HTTP upstream proxy that answers CONNECT and is
// closed when the test ends.
func NewHTTP(t testing.TB, options ...Option) *Proxy {
	t.Helper()
	return start(t, func(proxy *Proxy) func(net.Conn) { return proxy.serveHTTP }, options)
}

// start listens on a loopback port and serves each connection with the
// handler handle returns, unless the fake refuses.
func start(t testing.TB, handle func(*Proxy) func(net.Conn), options []Option) *Proxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the fake upstream proxy: %v", err)
	}
	proxy := &Proxy{Addr: listener.Addr().String(), listener: listener, conns: map[net.Conn]struct{}{}}
	for _, option := range options {
		option(proxy)
	}
	t.Cleanup(proxy.Close)
	if proxy.behaviour == Refuse {
		listener.Close()
		return proxy
	}
	proxy.wg.Add(1)
	go proxy.serve(handle(proxy))
	return proxy
}

// URL returns scheme://[user:pass@]addr for the fake, with credentials when
// username is not empty.
func (proxy *Proxy) URL(scheme, username, password string) string {
	if username == "" {
		return scheme + "://" + proxy.Addr
	}
	return scheme + "://" + username + ":" + password + "@" + proxy.Addr
}

// Tunnels returns the tunnels asked for so far, in order.
func (proxy *Proxy) Tunnels() []*Tunnel {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return append([]*Tunnel(nil), proxy.tunnels...)
}

// Targets returns the target of every tunnel asked for so far.
func (proxy *Proxy) Targets() []string {
	var targets []string
	for _, tunnel := range proxy.Tunnels() {
		targets = append(targets, tunnel.Target)
	}
	return targets
}

// Close stops the fake and closes every connection it holds.
func (proxy *Proxy) Close() {
	proxy.mu.Lock()
	if proxy.closed {
		proxy.mu.Unlock()
		return
	}
	proxy.closed = true
	for conn := range proxy.conns {
		conn.Close()
	}
	proxy.mu.Unlock()
	proxy.listener.Close()
	proxy.wg.Wait()
}

func (proxy *Proxy) serve(handle func(net.Conn)) {
	defer proxy.wg.Done()
	for {
		conn, err := proxy.listener.Accept()
		if err != nil {
			return
		}
		if !proxy.track(conn) {
			conn.Close()
			return
		}
		proxy.wg.Add(1)
		go func() {
			defer proxy.wg.Done()
			defer proxy.untrack(conn)
			handle(conn)
		}()
	}
}

func (proxy *Proxy) track(conn net.Conn) bool {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.closed {
		return false
	}
	proxy.conns[conn] = struct{}{}
	return true
}

func (proxy *Proxy) untrack(conn net.Conn) {
	conn.Close()
	proxy.mu.Lock()
	delete(proxy.conns, conn)
	proxy.mu.Unlock()
}

func (proxy *Proxy) record(tunnel *Tunnel) {
	proxy.mu.Lock()
	proxy.tunnels = append(proxy.tunnels, tunnel)
	proxy.mu.Unlock()
}

func (proxy *Proxy) credentialsAccepted(tunnel *Tunnel) bool {
	if proxy.behaviour != RejectCredentials {
		return true
	}
	return tunnel.HasCredentials && tunnel.Username == proxy.username && tunnel.Password == proxy.password
}

func (proxy *Proxy) serveHTTP(conn net.Conn) {
	if proxy.behaviour == Hang {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	reader := bufio.NewReader(conn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	tunnel := &Tunnel{Target: request.Host, Request: request}
	if request.Method == http.MethodConnect && request.RequestURI != "" {
		tunnel.Target = request.RequestURI
	}
	if authorization := request.Header.Get("Proxy-Authorization"); authorization != "" {
		tunnel.HasCredentials = true
		if encoded, ok := strings.CutPrefix(authorization, "Basic "); ok {
			if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
				tunnel.Username, tunnel.Password, _ = strings.Cut(string(decoded), ":")
			}
		}
	}
	proxy.record(tunnel)
	if request.Method != http.MethodConnect {
		_, _ = io.WriteString(conn, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if !proxy.credentialsAccepted(tunnel) {
		_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"fake\"\r\nContent-Length: 0\r\n\r\n")
		return
	}
	target, err := proxy.dialTarget(tunnel.Target)
	if err != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if !proxy.track(target) {
		target.Close()
		return
	}
	defer proxy.untrack(target)
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	Splice(conn, reader, target, tunnel)
}

// dialTarget connects to target, resolving names given with WithHost.
func (proxy *Proxy) dialTarget(target string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	if address, ok := proxy.hosts[host]; ok {
		host = address
	}
	return net.Dial("tcp", net.JoinHostPort(host, port))
}

// Splice copies between a client and the tunnel's target until either side
// closes, recording what the client sent. reader holds any client bytes
// already buffered.
func Splice(client net.Conn, reader io.Reader, target net.Conn, tunnel *Tunnel) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(io.MultiWriter(target, tunnel), reader)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, target)
		done <- struct{}{}
	}()
	<-done
	client.Close()
	target.Close()
	<-done
}
