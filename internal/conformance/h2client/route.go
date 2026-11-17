package h2client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// routeTimeout is how long the route check waits for its TLS handshake.
const routeTimeout = 5 * time.Second

// checkRoute shows that the shim at address reaches TLS from the job's
// side of the run before h2spec runs. h2spec trusts any certificate (-k),
// so its verdicts cannot show the proxy was in the path: through a proxy
// the handshake must verify against the proxy's CA alone, and in the
// direct run against the harness CA that signs nginx's certificate. The
// check offers http/1.1 as well as h2, so a proxy that does not speak h2
// still completes the handshake.
func checkRoute(ctx context.Context, address string, job conformance.Job) error {
	config := job.CA.ClientTLS(TargetHost)
	if job.Proxy != "" {
		if len(job.ProxyCA) == 0 {
			return fmt.Errorf("%w: the proxy gave no CA certificate to check the TLS inside CONNECT against", conformance.ErrNotProxied)
		}
		var err error
		if config, err = conformance.ClientTLSFromPEM(job.ProxyCA, TargetHost); err != nil {
			return err
		}
	}
	config.NextProtos = []string{"h2", "http/1.1"}
	ctx, cancel := context.WithTimeout(ctx, routeTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("dialing the shim to check the route: %w", err)
	}
	defer conn.Close()
	err = tls.Client(conn, config).HandshakeContext(ctx)
	if err != nil && job.Proxy != "" {
		return fmt.Errorf("%w: TLS through the shim does not verify against the proxy's CA: %v", conformance.ErrNotProxied, err)
	}
	if err != nil {
		return fmt.Errorf("checking TLS through the shim to nginx: %w", err)
	}
	return nil
}
