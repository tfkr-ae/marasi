# Marasi owns its HTTP/1.1 wire format

Go's `net/http` cannot carry a message unchanged. `http.Header` canonicalises name case, reorders fields and trims value whitespace, and `http.Transport` swallows 1xx responses, drops trailers and refuses transfer codings other than chunked. Replacing Martian with `net/http` would keep most of the deviations the conformance harness records ([Conformance is scored against a direct baseline](2026-11-16-conformance-is-scored-against-a-direct-baseline.md)). Marasi's proxy library therefore parses and writes HTTP/1.1 itself, importing only the standard library and `golang.org/x/net`. A message's header section is an ordered list of raw lines, kept byte for byte, and that list is the source of truth. `http.Request` and `http.Response` are views of it, which modifiers, Checkpoint and extensions edit.

Edits come back into the raw lines by fixed rules:

- A Checkpoint edit arrives as raw text and replaces the lines directly.
- Through the `http.Header` view, a name left unchanged keeps its lines byte for byte. A changed name keeps its first line's position and original name case; the new values go there and any extra lines follow it. A removed name loses all its lines. A new name is appended at the end, spelled exactly as the map key, so Lua `set_header` keeps the user's spelling.
- An edit that changes nothing changes no bytes.
- An edited body is framed from its final bytes: a `Content-Length` value is rewritten in place, a chunked body is re-chunked with `Transfer-Encoding` and trailers kept, a body in a transfer coding Marasi does not decode (`gzip, chunked`, or one read until close) goes out chunked with those codings dropped from `Transfer-Encoding`, and a body added to a message with neither gets `Content-Length` appended. A body that is not changed is never re-framed. Modifiers no longer set framing headers, so `set_body` stops setting `Content-Length`.

A stored pair's raw text is the lines that were on the wire: the request as sent upstream after modifiers, and the response as received after modifiers, with case, order and whitespace kept. Pairs stored before keep their canonical form. The search index, prettify, the app and the CLI already read raw text and need no change.

This carries out [Transparency keeps header case and order](2026-11-16-transparency-keeps-header-case-and-order.md) and [The proxy follows hop-by-hop rules but sends no Via](2026-11-16-the-proxy-follows-hop-by-hop-rules-but-sends-no-via.md).

## Considered options

- **Go's `net/http` server and transport.** Rejected because the header map and the transport cause most of the recorded deviations, and neither can be configured to keep the wire form.
- **Keep Martian.** It is unmaintained since v2.1.0, sits on `net/http`, and forced shapes on Marasi's code: context looked up by request pointer, sentinel errors, a made-up blank 200.
- **A raw-line model with no `http.Request` view.** Rejected because modifiers, extensions and marasi-app are written against `http.Request` and `http.Response`; the view keeps them working while the raw lines decide the bytes.

**Consequences:** the library owns HTTP/1.1 framing, so framing bugs are Marasi's to find. The codec is tested on its own, table-driven over framing edge cases, with the property that unchanged input gives byte-identical output. HTTP/2 (Phase 5) brings its own framing under the same request/response pair, and cannot keep name case ([Transparency keeps header case and order](2026-11-16-transparency-keeps-header-case-and-order.md)).
