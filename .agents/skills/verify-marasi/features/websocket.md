# WebSocket

Users list proxied WebSocket connections, open one, page its stored messages, and, while both peers stay connected, inject a message or close the connection. A stored message is the whole RFC 6455 message, reassembled from its frames, or a single ping, pong, or close (ADR-0027). The upgrade is also a traffic pair. `traffic websocket` opens the connection from that pair's id.

## Sub-features

- List saved connections with `websocket list`.
- Page that list with `--limit` and `--cursor`.
- Read one connection with `websocket get CONNECTION_ID`.
- List stored messages with `websocket messages CONNECTION_ID`.
- Page messages with `--limit` and `--cursor`.
- Open the connection for a traffic pair with `traffic websocket REQUEST_ID`.
- Inject a message as one frame with `websocket inject CONNECTION_ID --direction client|server --opcode N`, from `--file` or stdin. With permessage-deflate, `--compress never` sends it uncompressed.
- Close a live connection with `websocket close CONNECTION_ID`, optionally `--code` and `--reason`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service. Open a WebSocket through `proxy_listener` and leave both ends connected. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" websocket list`. Copy `id` into get, messages, inject, and close. Copy `request_id` into `traffic websocket`.

## Driving it with shell and curl

A normal curl GET does not create a connection. Start a local HTTP origin on `127.0.0.1` that answers `Upgrade: websocket` with `101`, `Connection: Upgrade`, `Upgrade: websocket`, and the matching `Sec-WebSocket-Accept`, then keeps reading. From a second process, write that upgrade to `proxy_listener` as an absolute-form GET and hold the socket after `101`.

Retry `websocket list --json` until one item is `open`, with `transport` `ws`, `host` `127.0.0.1:$ORIGIN_PORT` (one field, not a separate port), `path` equal to the request path plus `?query` when a query was sent, `closed_at` null, and `close_code` 0. Save `id` and `request_id`. Run `websocket get "$CONNECTION_ID" --json` and `traffic websocket "$REQUEST_ID" --json` and require the same object. The swapped id fails with `getting websocket connection: not_found` or `getting traffic websocket: not_found`. Run `websocket messages "$CONNECTION_ID" --json` and require `items` empty and `next_cursor` null.

While the client is still held, run `printf 'hello' | websocket inject "$CONNECTION_ID" --direction server --opcode 1 --json`. Require `payload` `aGVsbG8=`, `opcode` 1, `direction` `server`, and `metadata.injected` true. Retry `websocket messages "$CONNECTION_ID" --limit 1 --json` until that id is `items[0]`. Run `websocket close "$CONNECTION_ID" --json` and require `state` `closed`, `close_code` 1000, and `closed_at` set. A second close must fail with `closing websocket connection: websocket_not_open`.

## Gotchas

- `websocket get` takes the connection id. `traffic websocket` takes the upgrade request id. The other id fails with `getting websocket connection: not_found` or `getting traffic websocket: not_found`.
- The proxy forwards only `permessage-deflate` offers upstream. Other `Sec-WebSocket-Extensions` offers are removed and listed in `removed_extensions`. The client gets the upstream's accept unchanged, and `deflate` holds the negotiated parameters, or null when the upstream declined. If the upstream accepts something no offer allowed, the proxy closes the client without a `101`, stores no connection, and writes an `ERROR` log naming the upstream's response.
- With permessage-deflate, the proxy inflates each compressed message and compresses it again for the receiver with its own compressor, so the bytes differ on the two legs (ADR-0028). A stored message has the uncompressed `payload` and `compressed` true; Lua sees `message:payload()` uncompressed and `message:compressed()`. An injected text or binary message is compressed by default (`compressed` true); `--compress never` (API `"compress":"never"`) sends it uncompressed. Toward a peer whose `*_max_window_bits` is below 15, the proxy compresses Huffman-only and sends a message uncompressed when that is not smaller, so `compressed` describes the sender's leg.
- JSON list and JSON messages are newest first. Human list prints that order. Human messages reverses the page, so the oldest row of that page prints first. `next_cursor` is the oldest id on the page. The next page is older. Human `next_cursor=` is stderr. An empty human list prints nothing.
- List and messages default to `--limit 200`. The service accepts 1 through 500. Other query keys are ignored.
- Inject requires both `--direction` and `--opcode`. A TTY stdin is an empty payload, not a prompt. `--file` beats a pipe. `client` is written upstream. `server` is written to the holding client.
- Close does not validate `--code` locally. Omitted code and `--code 0` become 1000. `1005` and `1006` are rejected. The reason must be UTF-8 and at most 123 bytes. Human close prints `websocket ID closed` on stderr and hides the record.
- Inject and close need the live connection. If the client drops TCP first, state becomes `error` with `close_code` 1006. Inject then fails with `injecting websocket message: websocket_not_open`. Close fails with `closing websocket connection: websocket_not_open`.
- Storage lags the open and message events. Retry list and messages. Subscribe with `events` before the upgrade if you need `websocket.opened`, `websocket.message`, or `websocket.closed`. The stream has no replay.
- A fragmented message is stored, published as `websocket.message`, and held by Checkpoint only once its final frame arrives. `frame_count` says how many frames carried it; there is no `fin` field. Its `id` is assigned when the first frame arrives, so it can sort before a ping that arrived between its fragments. Ping, pong, and close frames between fragments are stored and forwarded at once.
- While a message's frames are still arriving, the `events` stream sends `websocket.progress` with `message_id` (the id the message gets once complete), `bytes`, `frames`, and `streamed`, at most every 100 ms. It is never stored.
- With no hook able to hold or change a message (no enabled extension defines `processWebSocketMessage`, and Checkpoint is disabled or has `websocket-intercept` off and no `interceptWebSocketMessage`), the relay forwards bytes as they arrive (`streamed` true). Otherwise nothing is forwarded until the message is complete. A message whose first frame sets RSV1 (compressed) is never streamed.
- The per-message limit is `websocket_max_message_size` in `marasi_config.yaml`, in bytes, 64 MiB when unset. Over it, the sender gets close 1009, the receiver 1001, the connection records `close_code` 1009, and a `WARN` log names the direction, size, and limit. The message is stored and announced with `metadata.too_large` true and `metadata.size_seen` (its size once the frame that went over is counted); its `payload` is the bytes buffered before the limit, empty for a streamed message or a single frame over the limit. After its own close, the proxy forwards nothing more in either direction. A compressed message counts its inflated bytes: one that inflates past the limit gets the same closes, and its log says it "inflates to over" the limit.
- A compressed message the proxy cannot inflate is not forwarded. It is stored with `compressed` true, the received compressed bytes as `payload`, and `metadata.inflate_error` ("too large: ..." for one that inflates past the limit). The sender gets close 1007 (1009 when too large), the receiver 1001, and a `WARN` log names the connection.
- This is not `checkpoint websocket-intercept`. If that flag is on, inject waits until the held frame is forwarded or dropped. Leave it off for this feature.
