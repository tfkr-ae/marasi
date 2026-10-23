# Request path

How a proxied request travels from the client socket to the origin and back, and which Marasi clients reach the origin. Names are functions and types, so search for them. For anything inside one function, read the function.

In this map, bare "upstream" means the origin, as in `GLOSSARY.md`. An **upstream proxy** is a proxy that Marasi sends traffic through on its way to the origin. It is always written in full.

## Order

1. **Listener.** `(*Proxy).GetListener` and `(*Proxy).Serve` stack the listener wrappers. The raw socket is watched beneath protocol inspection and TLS, so a client that leaves is noticed while martian is not reading.
2. **martian v2.1.0** reads the request and MITMs a `CONNECT`. It then runs every request modifier, then the round trip, then every response modifier. The whole request pipeline has run before anything goes upstream.
3. **Request side.** `WithBasePipeline` wraps `proxy.Modifiers` for admission, internal-client checks and header stripping. `WithDefaultModifierPipeline` adds the modifiers, which run in the order they are added. `proxy.Modifiers` is a martian `fifo.Group`, so the first error stops the chain, and `ErrSkipPipeline` and `ErrDropped` are how a modifier ends it early.
4. **Capture.** The request is captured by its write modifier, before the round trip, and the response by its write modifier, after it. Both queue on `DBWriteChannel`, and `(*Proxy).WriteToDB` stores them.
5. **Round trip.** martian calls `launchFailureRoundTripper`, which wraps `marasiRoundTripper`. `(*Proxy).Serve` builds the round tripper once with `newRoutedTransport`. `RoundTrip` brings the route up to date, then the transport takes an idle connection or dials a new one through the route. See [Route](#route).
6. **Response side.** `WithBasePipeline` buffers the body and admits project work again before the response modifiers run. A WebSocket upgrade is handed off by the last response modifier.

## Route

The route says how a new connection reaches the origin: directly, or through the upstream proxy that the open project selects. It is applied in the transport's dial step, and nowhere else.

- **Selection.** `Proxy.SelectUpstreamProxy` sets the selection from `ProjectResources.UpstreamProxy` when a project opens, and from `PUT /upstream-proxy-selection`. It dials nothing.
- **Resolve.** The `routeResolver` (`route_resolver.go`) combines the selection with this machine's list in `marasi_config.yaml` and returns a `Route`. It reads the YAML again only when the file's modification time or size changed. `Proxy.resolveRoute` runs it before each request takes a connection (in `marasiRoundTripper.RoundTrip`) and again for each new connection (in `dialOnCurrentRoute`). A hand edit, or a change from another instance sharing the config directory, therefore applies to the next request.
- **Swap.** When the resolved route differs from the current one, `Proxy.SetRoute` swaps the atomic pointer and closes the transport's idle connections. Every connection is a `routedConn` that carries the route that opened it. A connection that was carrying a request during the swap is closed when that request finishes (`retireStale`). A dial that a swap overtook is discarded and runs again on the new route (`dialOnCurrentRoute`). The listener keeps running.
- **Dial.** `Route.dial` (`route_dial.go`) is the only place each scheme is dialled. It dials the origin's address, which is the waypoint's destination when a waypoint rewrote the URL host:
  - direct: TCP to the origin;
  - `http`: TCP to the upstream proxy, then `CONNECT host:port` with `Proxy-Authorization: Basic` when the entry has credentials. Plain-HTTP origins are tunnelled with `CONNECT` too;
  - `https`: the same `CONNECT` inside TLS to the upstream proxy (`route_tls.go`);
  - `socks5`: no authentication, or username/password when the entry has credentials, with the hostname sent unresolved (`route_socks5.go`).
- **Origin TLS.** For a TLS origin, the utls handshake then runs inside the connection that `Route.dial` returned. Both TLS legs verify against the system roots plus the entry's CA when it has one (`Route.rootCAs`, built once per route). Direct connections and other routes never trust that CA. A failed origin handshake through an upstream proxy is a `tls_verification` `*UpstreamProxyError`.
- **Fail closed.** A route that cannot carry the connection, or a selection missing from this machine's list, returns an `*UpstreamProxyError` and never a direct connection. `launchFailureRoundTripper` writes one ERROR proxy log naming the pair, the upstream proxy and the cause, and martian answers the client with a 502.
- **Snapshot.** `SetupRequestModifier` puts a `core.RouteRecord` on the request context. The `GotConn` trace in `marasiRoundTripper.RoundTrip` records the route of the connection that carried the request. A failed dial records the route its final attempt tried. `NewProxyResponse` copies the record into `ProxyResponse.Route`, and `InsertResponse` stores it in `request.upstream_proxy_name` and `request.upstream_proxy_address`.
- **Check.** `Proxy.CheckUpstreamProxy` (`route_check.go`) resolves a named entry with a resolver of its own and dials once through a separate `newRoutedTransport` fixed to that route. It sends no request and leaves the selection alone.

## Who reaches the origin

Only the transport that `newRoutedTransport` builds connects to the origin. `newMarasiTransport` builds one whose route is always direct, for tests. `proxy.Client` sends through Marasi's own listener, so its requests run the whole path above and are captured. Launches clone it (`launchClient`), and Armory and extensions use it as is. Every other `http.Client` in the repo is not proxy traffic: it reads a local CDP endpoint, fetches updates, or calls the control API.

## Where an outbound header comes from

Only these touch the headers a request goes upstream with:

- the sending client, which is `proxy.Client`'s transport for Marasi's own requests;
- `WithBasePipeline`;
- the request modifiers;
- `marasiRoundTripper.RoundTrip`;
- Go's `http.Transport`.

An HTTP or HTTPS upstream proxy sees only the `CONNECT` and its `Proxy-Authorization`, and a SOCKS5 upstream proxy sees only its handshake. The tunnelled request carries no proxy headers, because the transport's `Proxy` field stays unset.
