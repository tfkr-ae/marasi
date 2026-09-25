# WebSocket

Users list proxied WebSocket connections, open one, page its stored frames, and, while both peers stay connected, inject a frame or close the connection. The upgrade is also a traffic pair. `traffic websocket` opens the connection from that pair's id.

## Sub-features

- List saved connections with `websocket list`.
- Page that list with `--limit` and `--cursor`.
- Read one connection with `websocket get CONNECTION_ID`.
- List stored frames with `websocket messages CONNECTION_ID`.
- Page frames with `--limit` and `--cursor`.
- Open the connection for a traffic pair with `traffic websocket REQUEST_ID`.
- Inject a frame with `websocket inject CONNECTION_ID --direction client|server --opcode N`, from `--file` or stdin.
- Close a live connection with `websocket close CONNECTION_ID`, optionally `--code` and `--reason`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service. Open a WebSocket through `proxy_listener` and leave both ends connected. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" websocket list`. Copy `id` into get, messages, inject, and close. Copy `request_id` into `traffic websocket`.

## Driving it with shell and curl

A normal curl GET does not create a connection. Start a local HTTP origin on `127.0.0.1` that answers `Upgrade: websocket` with `101`, `Connection: Upgrade`, `Upgrade: websocket`, and the matching `Sec-WebSocket-Accept`, then keeps reading. From a second process, write that upgrade to `proxy_listener` as an absolute-form GET and hold the socket after `101`.

Retry `websocket list --json` until one item is `open`, with `transport` `ws`, the origin host and port, the request path, `closed_at` null, and `close_code` 0. Save `id` and `request_id`. Run `websocket get "$CONNECTION_ID" --json` and `traffic websocket "$REQUEST_ID" --json` and require the same object. Run `websocket messages "$CONNECTION_ID" --json` and require `items` empty and `next_cursor` null.

While the client is still held, run `printf 'hello' | websocket inject "$CONNECTION_ID" --direction server --opcode 1 --json`. Require `payload` `aGVsbG8=`, `opcode` 1, `direction` `server`, and `metadata.injected` true. Retry `websocket messages "$CONNECTION_ID" --limit 1 --json` until that id is `items[0]`. Run `websocket close "$CONNECTION_ID" --json` and require `state` `closed`, `close_code` 1000, and `closed_at` set. A second close must fail with `closing websocket connection: websocket_not_open`.

## Gotchas

- `websocket get` takes the connection id. `traffic websocket` takes the upgrade request id. The other id is `not_found`.
- JSON list and JSON messages are newest first. Human list prints that order. Human messages reverses the page, so the oldest row of that page prints first. `next_cursor` is the oldest id on the page. The next page is older. Human `next_cursor=` is stderr. An empty human list prints nothing.
- List and messages default to `--limit 200`. The service accepts 1 through 500. Other query keys are ignored.
- Inject requires both `--direction` and `--opcode`. A TTY stdin is an empty payload, not a prompt. `--file` beats a pipe. `client` is written upstream. `server` is written to the holding client.
- Close does not validate `--code` locally. Omitted code and `--code 0` become 1000. `1005` and `1006` are rejected. The reason must be UTF-8 and at most 123 bytes. Human close prints `websocket ID closed` on stderr and hides the record.
- Inject and close need the live connection. If the client drops TCP first, state becomes `error` with `close_code` 1006, and both commands return `websocket_not_open`.
- Storage lags the open and message events. Retry list and messages. Subscribe with `events` before the upgrade if you need `websocket.opened`, `websocket.message`, or `websocket.closed`. The stream has no replay.
- This is not `checkpoint websocket-intercept`. If that flag is on, inject waits until the held frame is forwarded or dropped. Leave it off for this feature.
