# HTTP traffic capture

Marasi accepts an HTTP client's connection on its assigned proxy listener, forwards the request to the origin, returns the origin response, and records both halves in the selected project.

## Sub-features

- Forward a plain HTTP request through the proxy.
- Return status, headers, and body from the origin.
- Persist request method, host, path, raw request, response status, headers, body, and timestamps.
- Capture HTTPS through Marasi's generated CA and TLS interception path.
- Speak HTTP/2 when the client and origin do: over TLS through `CONNECT` a client offering `h2` to an origin that speaks it gets `h2` on both legs; a client that opens with the HTTP/2 preface (prior knowledge), straight to the proxy listener or inside a plain `CONNECT` tunnel, gets h2c to the origin. Each pair records `client_protocol` and `origin_protocol`.
- Forward a request as written: header case and order are kept on both legs, the origin gets the origin-form request line, and Marasi adds no header (no `Via`). A request inside a `CONNECT` tunnel goes to the tunnel's origin whatever its `Host` says; the stored `host` column still shows the `Host` field.
- Pass a `gzip`, `x-gzip`, `deflate`, `br` or `zstd` response (any case, stacked codings such as `gzip, br` included) to the client as the origin sent it, encoded and with `Content-Encoding`, while the stored pair holds the decoded body without `Content-Encoding` and `metadata.decoded_content_encoding` and `metadata.wire_content_length` record what was undone.
- Answer `Expect: 100-continue` with Marasi's own `100 Continue`, then forward the request with `Expect` kept.
- Answer with a plain-text **proxy error response** when no origin response is possible: 400 for ambiguous request framing (for example both `Content-Length` and `Transfer-Encoding`), which also closes the connection and never reaches the origin; 502 for a dial or route failure, naming the cause (`connection refused`); 502 `the request loops back to the proxy listener` for a request to the listener's own address. It carries no `Warning` header. Each of the three is stored with `metadata.proxy_error` naming the cause and writes an `ERROR` proxy log `Proxy error response (request GET …): …` with the pair's `request_id`.
- Store what arrived of a response body the origin cut short, with `metadata.response_body_ending` `cut short`.
- Forward an ordinary body that passes the **body threshold** as it arrives instead of reading it whole ([Streamed pairs are recognised from the head, and other pairs stay buffered up to a threshold](../../../../docs/adr/2026-11-20-streamed-pairs-are-recognised-from-the-head-and-other-pairs-stay-buffered.md)). It passes when its head declares a length over the threshold, when the buffered body grows past it, or when it stalls with no new bytes for 2 seconds after some arrived. The threshold is `body_threshold` in `marasi_config.yaml`, in bytes, 64 MiB when unset or 0; a negative value fails `service start` with `body_threshold is -1: use 0 for the 64 MiB default or a positive number of bytes`. The client gets the whole body; the pair stores only what arrived before it passed, at most the threshold, with `metadata.<direction>_passed_through` true and `metadata.<direction>_body_ending` `too large` (`request` or `response`). Such a body cannot be held or edited: a Checkpoint hold it skips writes a `WARN` proxy log `A Checkpoint hold was skipped for the request to POST http://…: its body passed the body threshold and is forwarded as it arrives`.
- Emit live `traffic.request` and `traffic.response` events for each captured exchange. Subscribe with `events`.

## How to get to it (user POV)

Start a service and read `proxy_listener` from `service status --json`. Without `--json` the label is `proxy listener:`. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" events` and wait for `: connected` on stderr before sending traffic. Configure the HTTP client's proxy setting to `http://$PROXY_LISTENER`, then make a request normally. For `curl` requests to localhost, clear proxy bypass with `--noproxy ''`.

## Driving it with shell and curl

Start a local HTTP origin that returns a unique body. Start `events` first and wait until stderr is `: connected`. Run `curl --fail --show-error --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$ORIGIN_PORT/proof.txt"`, saving request command, response headers, and response body. Require the unique body and HTTP 200. Require events stdout to contain `traffic.request` then `traffic.response` whose JSON `id` matches. Then use `traffic list -q 'path = "/proof.txt" AND status_code = 200' --json` and `traffic get "$TRAFFIC_ID" --json` to prove Marasi recorded the same exchange.

For HTTPS, trust `$VERIFY_CONFIG_DIR/marasi_cert.pem` with `curl --cacert` and request `https://example.com/` through the same proxy with `--noproxy ''`. Require `traffic list -q 'host = "example.com"' --json` to show `scheme` `https`, path `/`, and status 200. A self-signed local origin is not this proof.

For HTTP/2 over TLS, add `--http2` to that curl and require `-w '%{http_version}'` `2` and a pair with `client_protocol` and `origin_protocol` `h2`. For h2c, serve a local Go `http.Server` with `Protocols.SetUnencryptedHTTP2(true)` that echoes `r.Proto`, and run `curl --http2-prior-knowledge -p --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$H2C_PORT/h2c-proof"` (tunnel) and `curl --http2-prior-knowledge --noproxy '*' --connect-to "127.0.0.1:$H2C_PORT:127.0.0.1:$PROXY_PORT" "http://127.0.0.1:$H2C_PORT/h2c-direct"` (preface straight to the listener); each body must show `HTTP/2.0` and each pair `h2`/`h2`.

For wire transparency, use a raw-socket origin that records the bytes it reads, and send `GET http://127.0.0.1:$RAW_PORT/case HTTP/1.1` with mixed-case headers in a chosen order over a raw socket to the proxy listener. The origin must read `GET /case HTTP/1.1` and the same header lines, byte for byte and nothing added. For `gzip`, a plain `curl` (no `--compressed`) must receive bytes starting `\x1f\x8b` with `Content-Encoding: gzip`, while `traffic get` shows the decoded body. For proxy error responses, send a raw request with both `Content-Length: 3` and `Transfer-Encoding: chunked` (400, the origin logs nothing), curl a closed local port (502), and curl `http://$PROXY_LISTENER/loop` through the proxy (502 naming the loop); `traffic list -q 'metadata.proxy_error:*' --json` lists all three, and `logs --json` has an `ERROR` line for each whose `request_id` is that pair's id.

For the body threshold, use a second scratch config directory and instance: start it once, stop it, append `body_threshold: 1000` to its `marasi_config.yaml`, and start it again. A 5000-byte response with `Content-Length` must reach curl whole and be stored with an empty body, `length` 0 and `metadata.response_passed_through` true with `response_body_ending` `too large`; a chunked 5000-byte response must be stored with its first 1000 bytes and `length` 1000. With `checkpoint intercept on`, a 5000-byte POST is not held (the first held item is its response) and the `WARN` log above names it, with the pair's id as the log's `request_id` and `context` `{}`. A chunked response that sends a first chunk and then pauses 3.5 s must deliver that chunk to the client before the pause ends. A small response stays buffered, without `response_passed_through`. Stop that instance and remove its directory.

## Gotchas

- `NO_PROXY` commonly contains localhost. `--proxy` alone may bypass Marasi, so include `--noproxy ''`.
- The origin certificate is checked by the system trust store on the outbound hop. A self-signed local origin returns 502. There is no flag to skip that check. Use a publicly trusted host for the HTTPS drive.
- `events` replays nothing on a fresh subscription. Subscribe before the request. In human mode stderr is `: connected`; stdout is `event-name id json`.
- A successful origin response alone does not prove capture. The events CLI and traffic CLI are the evidence. Events follow queueing, not database commit; retry traffic reads briefly until the matching pair and response are persisted.
- `body_threshold` is read when the instance starts; change it with the instance stopped. curl sends `Expect: 100-continue` for a large POST body; Marasi answers it itself.
- `traffic get --json` base64-encodes `request.raw` and `response.raw`. Headers and body are inside those raw messages, not separate fields.
- The local origin is a real production boundary. Do not replace Marasi's proxy or repository with test doubles.
