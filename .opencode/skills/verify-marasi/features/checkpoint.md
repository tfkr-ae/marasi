# Checkpoint

Users hold proxied HTTP messages (and optionally WebSocket frames) on a running instance, inspect the pending bytes, then forward or drop each item.

## Sub-features

- List pending items with `checkpoint list`.
- Filter the list with `--kind http` or `--kind websocket`.
- Read one item with `checkpoint get "$CHECKPOINT_ID"`.
- Forward an item with `checkpoint forward "$CHECKPOINT_ID"`.
- Send edited bytes with `--file` or piped stdin.
- Hold the matching HTTP response with `--intercept-response` on a request forward.
- Drop an item with `checkpoint drop "$CHECKPOINT_ID"`.
- Toggle HTTP intercept with `checkpoint intercept on` or `off`.
- Toggle WebSocket intercept with `checkpoint websocket-intercept on` or `off`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" checkpoint intercept on`. Send a request through `proxy_listener`. The client waits until you forward or drop the held item. Copy an id from `checkpoint list` into get, forward, or drop.

## Driving it with shell and curl

Run `checkpoint list --json` and require `items` `[]` with `intercept` and `websocket_intercept` false. Run `checkpoint intercept on --json` and require `intercept` true. Start `curl --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$ORIGIN_PORT/held.txt"` in the background. Retry `checkpoint list --kind http --json` until it contains a `request` item and save `id`. Run `checkpoint get "$CHECKPOINT_ID" --json` and require that id, `type` `request`, and `/held.txt` in the decoded `raw`. Run `checkpoint forward "$CHECKPOINT_ID" --json`. Retry list until a `response` item appears, then forward that id too. Require the background curl to exit 0 with the origin body. Start a second curl to `/drop.txt`, drop its request id, and require list empty of that id. Run `checkpoint intercept off --json`, then `checkpoint websocket-intercept on --json` and `checkpoint websocket-intercept off --json`.

## Gotchas

- `checkpoint intercept on` holds HTTP requests and the matching responses. After a request forward, a `response` item appears until you forward or drop it. `--intercept-response` is for holding the response when global intercept is off.
- This CLI is not the seeded Lua extension named `checkpoint`. That extension's `interceptRequest` / `interceptResponse` return false by default.
- `--kind` must be `http` or `websocket`. `http` includes `request` and `response` items and skips `websocket`.
- Human list prints `id type` lines and is silent when empty. Human get writes decoded HTTP bytes or the WebSocket payload. Human intercept/forward/drop confirm on stderr. `--json` list, forward, drop, and flag commands print the full list plus both flags.
- Forward with a TTY and no `--file` sends the original bytes. `--file` or a non-TTY stdin replaces them. A TTY stdin is not an edit.
- Pending items make `project open` fail with `project_busy`. Clear the queue before switching projects.
- Held traffic publishes `checkpoint.held`. Forward and drop publish `checkpoint.forwarded` and `checkpoint.dropped`. Flag commands publish `checkpoint.updated` only when the value changes. Subscribe with `events` before the action; the stream has no replay.
