# Checkpoint

Users hold proxied HTTP messages (and optionally WebSocket messages) on a running instance, inspect the pending bytes, then forward or drop each item.

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

- `checkpoint intercept on` holds plain HTTP requests and, while intercept stays on, the matching responses. `CONNECT` itself is skipped. After a trusted MITM handshake, the decrypted inner request is held. An untrusted client fails the handshake and never reaches Checkpoint. Use `http://` for this proof.
- `--intercept-response` on a request forward holds that response even when global intercept is off. With the seeded script, a request is held only while `checkpoint intercept` is on, or after you change `interceptRequest`. While intercept is on, the matching response is held anyway.
- This CLI is not the seeded Lua extension named `checkpoint`. That extension's `interceptRequest` / `interceptResponse` return false by default. If that extension is disabled, `checkpoint intercept on` can report `intercept` true and hold nothing. Leave it enabled.
- Dropping a held request does not fail the client. The proxy writes HTTP 200 with an empty body, so curl exits 0. Dropping a held response closes the client connection; curl exits non-zero and does not see that 200. The proof is that the id is gone from `checkpoint list`.
- `--kind` must be `http` or `websocket`. `http` includes `request` and `response` items and skips `websocket`. `list --kind --json` returns that filtered list. Forward, drop, and flag JSON are unfiltered.
- Human list prints an `ID  TYPE` table with the column names above and below the rows, and is silent when empty. Human get writes decoded HTTP bytes or the WebSocket payload. On a terminal it adds a trailing newline when the bytes lack one, so the prompt cannot overwrite a short payload; piped or redirected output stays byte-exact for get, edit, forward. Human intercept/forward/drop confirm on stderr. `--json` list prints the items for the requested `--kind`, plus both flags. `--json` forward, drop, and flag commands print the unfiltered list plus both flags.
- Forward with a TTY and no `--file` sends the original bytes. `--file` or a non-TTY stdin replaces them. A TTY stdin is not an edit.
- Pending items make `project open` of a different project fail. With `--json` the error is `opening project: project_busy`. Human output is `opening project: 409 Conflict` and does not include that code. Reopening the current project path succeeds. Clear the queue before switching projects.
- A held item that belongs to a pending launch does not block the switch. `project open` interrupts the launch, drops its hold, and succeeds. The launch ends `failed` with reason `interrupted` in the original project. An ordinary held request still gives `project_busy`.
- Held traffic publishes `checkpoint.held`. Forward and drop publish `checkpoint.forwarded` and `checkpoint.dropped`. Flag commands publish `checkpoint.updated` only when the value changes. Subscribe with `events` before the action; the stream has no replay.
- A hold ends when its client leaves (ADR-0030). If the HTTP client disconnects while its request or response is held, or the connection of a held WebSocket message closes, the item leaves `checkpoint list` and `checkpoint.dropped` is published with its `id` and `type`, the same as a user drop. Keep the background curl running until you forward or drop; killing it removes the item.
- With `checkpoint websocket-intercept on`, a fragmented WebSocket message is held as one `websocket` item once its final frame arrives, with the whole reassembled `payload`. Nothing of it reaches the receiver while it is held.
