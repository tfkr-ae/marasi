# Marasi gives the client the origin's protocol

A client speaks HTTP/2 to Marasi exactly when it would speak HTTP/2 going direct. When a client's TLS ClientHello arrives, Marasi first dials the origin: the `CONNECT` authority, or the SNI for TLS sent straight to the proxy listener, through the current route and any waypoint. It offers the origin the client's ALPN list in the client's order, then completes the client's handshake with the protocol the origin chose. In cleartext there is no ALPN, so Marasi copies the client: a client that opens with the HTTP/2 preface (prior knowledge), on the proxy listener or inside a plain `CONNECT` tunnel, gets prior-knowledge HTTP/2 to the origin. `Upgrade: h2c` stays an opaque relay.

Each client connection still owns one origin connection ([One origin connection per client connection](2026-11-17-one-origin-connection-per-client-connection.md)). Each client stream maps to one origin stream and runs through the chains on its own goroutine, so a Checkpoint hold blocks only its own stream. The client is told the origin's `SETTINGS_MAX_CONCURRENT_STREAMS`, and Marasi has no setting of its own for it.

- **The origin cannot be reached during the handshake.** Marasi still completes the client's handshake, choosing `http/1.1` if the client offered it and `h2` otherwise, and answers each request with a 502 proxy error response naming the cause. The failure then shows in the traffic, as an origin certificate that does not verify does today.
- **A request a user redirects on purpose.** A waypoint, a modifier that changes the URL's host, or a Checkpoint edit whose request line names a target gets an origin connection of its own, chosen origin first. When that origin speaks only HTTP/1.1, Marasi translates that request/response pair between versions. Editing `:authority` or `host` redirects nothing ([Requests inside a tunnel go to the tunnel's origin](2026-11-19-requests-inside-a-tunnel-go-to-the-tunnels-origin.md)).
- **A route swap.** The client gets a graceful `GOAWAY`, in-flight streams finish on the old route, and both legs close. The client's next connection runs origin first on the new route.
- **Upstream proxies.** The upstream proxy's `CONNECT` stays HTTP/1.1, and the TLS of an HTTPS upstream proxy is offered `http/1.1` only. The client's protocol runs inside that tunnel.

Every pair records its client protocol and origin protocol.

## Considered options

- **Always accept HTTP/2 from the client and translate when the origin lacks it.** This avoids dialling during the handshake. It was rejected because a client of an origin that speaks only HTTP/1.1 would see HTTP/2 behaviour it never sees going direct, and every pair would lose header case.
- **Offer the client what the origin chose last time, kept per host.** Rejected because the cache can be stale and the first connection to a host would still have to guess.
- **Refuse a redirected request with a 502.** Rejected because it would break waypoints and Checkpoint edits for HTTP/2.

**Consequences:** the origin is dialled before any request exists and before any modifier runs, so the route and waypoints apply at handshake time. Launches and Armory requests choose their protocol from the version on their request line. A request written as `HTTP/2` never falls back to HTTP/1.1, and an origin that will not speak HTTP/2 gets the launch a 502 proxy error response.
