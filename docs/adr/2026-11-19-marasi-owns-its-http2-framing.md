# Marasi owns its HTTP/2 framing

Marasi proxies HTTP/2 with its own engine built on `golang.org/x/net/http2.Framer` and `golang.org/x/net/http2/hpack`, not on `http2.Server` or `http2.Transport`. Those two put every message's fields into an `http.Header` map, which loses field order, the order of the pseudo-header fields and the split between `:authority` and `host`. That is the reason [Marasi owns its HTTP/1.1 wire format](2026-11-17-marasi-owns-its-http1-wire-format.md) gave for not using `net/http`. As in HTTP/1.1, each message's ordered field list, pseudo-header fields included, is the source of truth. `http.Request` and `http.Response` are views of it that modifiers, Checkpoint and extensions edit. Edits fold back into the list by the HTTP/1.1 rules. Pseudo-header fields are not entries in the view's `Header`: `:method`, `:path`, `:scheme` and `:authority` map to `Method`, the URL and `Host`.

An HTTP/2 request/response pair is stored and shown in an HTTP/1 text form that keeps everything the wire carried. The request line `METHOD path HTTP/2` carries `:method` and `:path`. Each other pseudo-header field follows as its own line (`:authority: example.com`) in its wire position, then the regular fields in wire order. A response is `HTTP/2 <status>` and its fields. Readers that do not know the form skip lines that start with `:`. Pseudo-header order is kept because the order a client sends them in is a known fingerprint, and [Transparency keeps header case and order](2026-11-16-transparency-keeps-header-case-and-order.md) keeps a header's shape because it is evidence.

Text a user writes is sent as typed wherever HPACK can encode it, even when it breaks RFC 9113: uppercase names, `transfer-encoding`, `connection`, or a `content-length` that does not match the body. These are the inputs of HTTP/2 desync testing. A message no one edited is never normalised.

## Considered options

- **`x/net/http2.Server` and `Transport`.** Rejected because the `http.Header` map loses field order and Marasi could not keep the wire form.
- **A Burp-style text form** that folds `:authority` into a `host` line. Rejected because it loses the pseudo-header order and the difference between `:authority` and `host`.

**Consequences:** Marasi owns HTTP/2 framing, flow control, SETTINGS and HPACK state on both legs, so faults there are Marasi's to find. Bodies are read whole before the chains run, as in HTTP/1.1. Streaming bodies, which gRPC streaming needs, is later work.
