# The proxy follows hop-by-hop rules but sends no Via

The conformance harness scores hop-by-hop fields and framing with leg rules taken from RFC 9110 and RFC 9112, not against the direct baseline. Marasi must remove `Connection` and the fields it names (RFC 9110 §7.6.1). It must send framing that is valid and matches the body (RFC 9112 §6). It must never send both `Content-Length` and `Transfer-Encoding` (RFC 9112 §6.1). It must keep the order of field lines with the same name (RFC 9110 §5.3). A comparison alone gets these wrong: a correct proxy that strips a field named in `Connection` differs from the direct run, and a proxy that forwards it matches.

RFC 9110 §7.6.3 also says a proxy MUST send `Via`. Marasi does not, and this is on the allow-list on purpose: a testing tool should not announce itself to the origin or add a header the user did not send.

**Consequences:** this contradicts [The proxy adds no headers to a request](2026-10-19-the-proxy-adds-no-headers-to-a-request.md), which forwards the client's `Connection` lines as written. Today that breaks a leg rule and is recorded as a known deviation. The HTTP/1.1 library is expected to fix it and supersede that ADR's `Connection` rule. Its rule against adding headers still stands, and is the reason for the `Via` exception.

**Amended 2026-11-17:** the HTTP/1.1 library removes `Connection` and the fields it names on each leg, and the `Connection` rule of the 2026-10-19 ADR is superseded. See [Marasi owns its HTTP/1.1 wire format](2026-11-17-marasi-owns-its-http1-wire-format.md).
