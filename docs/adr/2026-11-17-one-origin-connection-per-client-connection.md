# One origin connection per client connection

Martian sent requests through `http.Transport`, which pools origin connections and lets requests from different clients share one. The connection the origin saw was then not the one the client opened, and an origin closing a connection did not close the client's. Each client connection now gets its own origin connection, opened on its first request. When the origin closes, Marasi finishes forwarding the current response and then closes the client connection. When the client closes, Marasi closes the origin connection. A request on the same client connection for a different origin, as absolute form allows, opens a new origin connection and closes the old one. Pipelined requests on a client connection are processed in order.

The library's own pool replaces `http.Transport` and keeps the route semantics of [An upstream proxy fails closed](2026-10-23-an-upstream-proxy-fails-closed.md): the route is resolved before each request and each dial, a swap closes connections belonging to the old route, a dial overtaken by a swap is retried on the new route, and a selection that is missing or broken fails closed. Launches still use a new connection each, closed when the launch ends ([Launches are tracked and cancellable](2026-10-13-launches-are-tracked-and-cancellable.md)).

Martian's 5-minute deadline on each request goes away, so long downloads, slow uploads and long Checkpoint holds are not cut off. Reading request headers times out after 60 seconds, and an idle keep-alive client connection closes after 5 minutes. Dial and TLS handshake timeouts are unchanged.

## Considered options

- **A shared pool, as `http.Transport` does.** Rejected because requests from different clients share an origin connection, and connection behaviour, such as where a close happens, no longer matches a direct connection.
- **A new origin connection per request.** Rejected because the origin would see a new connection for every request on a keep-alive client connection, which a direct client never causes.

**Consequences:** a client that opens many connections makes as many origin connections, as it would directly. A swap of upstream proxy still applies to the next request without restarting the listener.
