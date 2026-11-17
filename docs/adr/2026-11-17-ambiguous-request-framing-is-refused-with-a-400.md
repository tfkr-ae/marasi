# Ambiguous request framing is refused with a 400

When a request's body length cannot be decided one way, a proxy and an origin can disagree on where it ends, and the remainder becomes a second request smuggled into the next request/response pair. Marasi refuses such a request with a 400 **Proxy error response** giving the cause ([Go modifier errors fail closed, and proxy error responses never go upstream](2026-11-17-go-modifier-errors-fail-closed-and-proxy-error-responses-never-go-upstream.md)), stores the pair with `metadata.proxy_error`, and closes the connection. Ambiguous framing means both `Content-Length` and `Transfer-Encoding`, several differing `Content-Length` values, an invalid chunk size, or any other case where RFC 9112 §6.3 gives no single length. A request that is odd but can be framed, such as unusual case or whitespace, is forwarded as written.

## Considered options

- **Forward as written.** Rejected because Marasi cannot tell where the next request starts, and the origin might decide differently. A tester studying smuggling can still send these bytes directly to the origin.
- **Normalise the framing, for example drop `Content-Length` when `Transfer-Encoding` is present, as RFC 9112 §6.1 allows.** Rejected because Marasi would send a request the client did not write while hiding that it changed the framing. Refusing is visible.

**Consequences:** the 400 body differs from the one nginx sends; the conformance harness accepts that difference with its reason. Closing the connection discards any bytes that followed, so nothing pipelined behind the request is read as a request.
