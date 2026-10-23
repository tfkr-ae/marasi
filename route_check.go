package marasi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
)

// ErrInvalidUpstreamProxyTarget reports a check target that is not an http
// or https URL with a host.
var ErrInvalidUpstreamProxyTarget = errors.New("invalid check target")

// UpstreamProxyCheckOutcome is how a check went: UpstreamProxyCheckOK, or
// the UpstreamProxyFailure that stopped it (see checkOutcome).
type UpstreamProxyCheckOutcome string

// UpstreamProxyCheckOK is the outcome of a check that reached the target.
const UpstreamProxyCheckOK UpstreamProxyCheckOutcome = "ok"

// checkOutcome is the outcome of a check that failed with failure.
func checkOutcome(failure UpstreamProxyFailure) UpstreamProxyCheckOutcome {
	return UpstreamProxyCheckOutcome(failure)
}

// defaultTargetPorts is the port a check target without one connects to,
// by scheme.
var defaultTargetPorts = map[string]string{"http": "80", "https": "443"}

// UpstreamProxyCheck is the result of one test connection through an
// upstream proxy. It never holds credentials.
type UpstreamProxyCheck struct {
	// Name is the upstream proxy checked.
	Name string
	// Address is its scheme://host:port, or "" when the entry has none.
	Address string
	// Target is the host:port the check connected to.
	Target string
	// Outcome is how the check went.
	Outcome UpstreamProxyCheckOutcome
	// Message says what happened in words.
	Message string
}

// CheckUpstreamProxy makes one test connection to target through the named
// entry of this machine's list, selected or not, and reports how it went.
// It dials exactly as traffic does: the same route dial and, for an https
// target, the same TLS handshake with the origin. It sends no request, so
// it stores no pair or proxy log, publishes nothing and leaves the
// selection alone. A name missing from the list is ErrUpstreamProxyNotFound
// and a bad target is ErrInvalidUpstreamProxyTarget.
func (proxy *Proxy) CheckUpstreamProxy(ctx context.Context, name, target string) (UpstreamProxyCheck, error) {
	address, useTLS, err := parseCheckTarget(target)
	if err != nil {
		return UpstreamProxyCheck{}, err
	}
	route := proxy.routes.resolveEntry(proxy.ConfigDir, name)
	if route.failure != nil && route.failure.Failure == UpstreamProxyNotConfigured {
		return UpstreamProxyCheck{}, fmt.Errorf("%w: %s", ErrUpstreamProxyNotFound, name)
	}
	result := UpstreamProxyCheck{Name: name, Address: route.Address(), Target: address}

	ctx, cancel := context.WithTimeout(ctx, route.timeoutOrDefault())
	defer cancel()
	fixed := func() *Route { return route }
	transport := newRoutedTransport(fixed, fixed, proxy.dialTransport)
	dial := transport.dialPlain
	if useTLS {
		dial = transport.dialTLS
	}
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		failure := route.checkFailure(contextCause(ctx, err))
		result.Outcome, result.Message = checkOutcome(failure.Failure), failure.Error()
		return result, nil
	}
	conn.Close()
	result.Outcome = UpstreamProxyCheckOK
	result.Message = fmt.Sprintf("upstream proxy %q (%s) connected to %s", name, result.Address, address)
	if useTLS {
		result.Message += " and verified its certificate"
	}
	return result, nil
}

// parseCheckTarget returns target's host:port, with the scheme's default
// port when it has none, and whether it is https.
func parseCheckTarget(target string) (string, bool, error) {
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", false, fmt.Errorf("%w: the target must be an http or https URL with a host", ErrInvalidUpstreamProxyTarget)
	}
	port := parsed.Port()
	if port == "" {
		port = defaultTargetPorts[parsed.Scheme]
	}
	return net.JoinHostPort(parsed.Hostname(), port), parsed.Scheme == "https", nil
}

// checkFailure classifies an error from dialing the target through the
// route. A failure to reach the upstream proxy is already classified; one
// after the tunnel opened comes from the TLS handshake with the target.
func (route *Route) checkFailure(err error) *UpstreamProxyError {
	var proxyErr *UpstreamProxyError
	if errors.As(err, &proxyErr) {
		return proxyErr
	}
	return route.handshakeFailure("the target", err)
}
