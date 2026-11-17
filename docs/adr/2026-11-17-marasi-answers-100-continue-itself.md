# Marasi answers 100 Continue itself

A client that sends `Expect: 100-continue` waits for a `100 Continue` before it sends the body. Checkpoint and the request chain need the whole request before anything goes upstream, so Marasi cannot wait for the origin's `100`. Marasi answers `100 Continue` to the client itself, reads the body, runs the chains, and forwards the request with `Expect` kept, so the origin sees what the client sent. A `100` from the origin is not relayed, because the client already had one. A final response the origin sends instead, such as 417, is relayed. Other 1xx responses, such as 103 Early Hints, are relayed.

## Considered options

- **Relay the origin's `100`.** Rejected because the request would have to go upstream before the body is read, so Checkpoint could not hold it whole and the request chain could not see the body.
- **Answer `100` and remove `Expect` upstream.** Rejected because it changes the request, which [The proxy adds no headers to a request](2026-10-19-the-proxy-adds-no-headers-to-a-request.md) and transparency rule out, and the origin could no longer refuse the body with 417.

**Consequences:** the client gets its `100` from Marasi, not from the origin, even when the origin would have answered 417. The conformance harness accepts that `100` difference with its reason.
