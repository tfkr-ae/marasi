# Request path

How a proxied request travels from the client socket to the origin and back, and which Marasi clients reach the origin. Names are functions and types, so search for them. For anything inside one function, read the function.

## Order

1. **Listener.** `(*Proxy).GetListener` and `(*Proxy).Serve` stack the listener wrappers. The raw socket is watched beneath protocol inspection and TLS, so a client that leaves is noticed while martian is not reading.
2. **martian v2.1.0** reads the request and MITMs a `CONNECT`. It then runs every request modifier, then the round trip, then every response modifier. The whole request pipeline has run before anything goes upstream.
3. **Request side.** `WithBasePipeline` wraps `proxy.Modifiers` for admission, internal-client checks and header stripping. `WithDefaultModifierPipeline` adds the modifiers, which run in the order they are added. `proxy.Modifiers` is a martian `fifo.Group`, so the first error stops the chain, and `ErrSkipPipeline` and `ErrDropped` are how a modifier ends it early.
4. **Capture.** The request is captured by its write modifier, before the round trip, and the response by its write modifier, after it. Both queue on `DBWriteChannel`, and `(*Proxy).WriteToDB` stores them.
5. **Upstream.** `launchFailureRoundTripper` wraps `marasiRoundTripper`, built once per `Serve` by `newRoutedTransport`. `RoundTrip` asks `Proxy.resolveRoute` before the transport takes a connection, so a changed selection or entry (a hand edit, or another instance sharing the config directory) closes idle connections before one is reused, and its dial step asks again for each new connection. The `routeResolver` (`route_resolver.go`) turns the open project's upstream proxy selection (`Proxy.SelectUpstreamProxy`, set from `ProjectResources.UpstreamProxy` and by `PUT /upstream-proxy-selection`) and this machine's list in `marasi_config.yaml` into a `Route`, reading the YAML again only when its modification time or size changed, and swaps the current route when the result differs. `Route.dial` (`route_dial.go`) is the one place each scheme is dialled: direct, or a tunnel through the upstream proxy (HTTP: `CONNECT host:port`, for plain-HTTP origins too) to the origin's address after any waypoint rewrite. The utls handshake for TLS origins then runs inside it. A route that cannot carry the connection returns an `*UpstreamProxyError`, never a direct connection, and `launchFailureRoundTripper` writes one proxy log for the pair; martian answers the client with its 502. Every connection is a `routedConn` carrying the route that opened it. `Proxy.SetRoute` swaps the route and closes idle connections, and a connection still carrying a request when the route changed is closed once that request finishes (`retireStale`). A dial the swap overtook is discarded and runs again on the new route (`dialOnCurrentRoute`).
6. **Response side.** `WithBasePipeline` buffers the body and admits project work again before the response modifiers run. A WebSocket upgrade is handed off by the last response modifier.

## Who reaches the origin

Only `newMarasiTransport` connects to the origin. `proxy.Client` sends through Marasi's own listener, so its requests run the whole path above and are captured. Launches clone it (`launchClient`), and Armory and extensions use it as is. Every other `http.Client` in the repo is not proxy traffic: it reads a local CDP endpoint, fetches updates, or calls the control API.

## Where an outbound header comes from

Only these touch the headers a request goes upstream with:

- the sending client, which is `proxy.Client`'s transport for Marasi's own requests;
- `WithBasePipeline`;
- the request modifiers;
- `marasiRoundTripper.RoundTrip`;
- Go's `http.Transport`.

The upstream proxy sees only the `CONNECT` and its `Proxy-Authorization`. The tunnelled request carries no proxy headers, because the transport's `Proxy` field stays unset.
