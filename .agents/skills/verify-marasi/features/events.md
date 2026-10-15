# Events

Users subscribe to a running instance and print live named events until the stream ends or they interrupt.

## Sub-features

- Subscribe with `events`.
- Print `traffic.request` and `traffic.response` for proxied exchanges.
- Print listener, project, launchpad, test-case, finding, artifact, note, metadata, checkpoint, Chrome path/profile, waypoint, wordlist, Armory template/run, extension, WebSocket, report-template, and log (`log.added`) events from those CLI operations.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" events` and leave it attached. Make requests through `proxy_listener` or run other instance commands in a second terminal. Stop the subscriber with Ctrl-C.

## Driving it with shell and curl

Start `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" events` as its own process, redirecting stdout and stderr. Wait until stderr is `: connected`. Proxy one HTTP request whose path you control. Require stdout to contain `traffic.request` then `traffic.response` with the same `id`. Require `path` on `traffic.request` only. Send SIGINT to the subscriber; after a successful connect that interrupt is a zero exit.

## Gotchas

- The stream is live only. Subscribe before the action; there is no replay.
- Human mode writes `: connected` to stderr and `name <json>` lines to stdout. `--json` writes NDJSON `{"event":...,"data":...}` to stdout and omits the connected comment.
- Heartbeat comments are dropped. Only named SSE events are printed.
- If the instance is not running, the command fails with `instance $VERIFY_INSTANCE is not running`.
- After `: connected`, SIGINT or SIGTERM is a zero exit. Closing before that comment fails. `events` installs its own SIGINT handler, so `kill -INT` of that PID is a zero exit even when the job inherited SIGINT ignored. Windows cannot deliver `os.Interrupt` to another process. Foreground Ctrl-C is the equivalent.
- `traffic.request` has `sitemap_node`, the name of the pair's leaf in the sitemap, such as `http://127.0.0.1:8080/users/{int}`.
- `sitemap.changed` with `{"reason":"rebuild"}` follows `POST /jobs/sitemap/rebuild`, and `{"reason":"delete"}` follows the `traffic.changed` of a successful `POST /traffic/bulk/delete`, even when `count` is 0. Clients re-read the sitemap nodes they have expanded.
- `traffic.response` has `id`, `status`, `status_code`, `content_type`, `length`, `metadata`, and `responded_at`. It has no `path`.
- Checkpoint event names are `checkpoint.held`, `checkpoint.forwarded`, `checkpoint.dropped`, and `checkpoint.updated`.
- Launch event names are `launch.started`, `launch.completed`, `launch.cancelled`, and `launch.failed`. Each carries the launch record: `launch_id`, `launchpad_id`, `status`, and `reason` when present.
- WebSocket event names are `websocket.opened`, `websocket.message`, `websocket.closed`, and `websocket.progress`. `websocket.progress` is printed while a fragmented message's frames are still arriving and is never stored; see [WebSocket](websocket.md).
- Background jobs publish `job.progress` with `{"name","project","done","total"}` at most about once a second while they run, and `job.complete` with `{"name","project"}` when they finish. `project` is the project's path, as in `traffic.index_complete`. The jobs are `traffic_index`, `annotation_index`, `prettified_move`, then `sitemap`, run one after the other. `traffic.index_complete` with `{"project":...}` follows the `job.complete` of whichever search index job, `traffic_index` or `annotation_index`, finishes last, when `index.complete` turns `true`; it is kept for one release. On an opened project the order is `job.complete` `traffic_index`, `job.complete` `annotation_index`, then `traffic.index_complete`. A project whose jobs are complete on open publishes nothing. A switch cancels the jobs without an event; the next open resumes them from their saved `done`.
- Every bulk route under `/traffic/bulk/` publishes exactly one `traffic.changed` with `{"change","count","selection"}` on success, even when `count` is 0, and nothing on failure. `change` is `highlight`, `link`, `unlink`, `note`, `metadata` or `delete`. `POST /traffic/bulk/highlight` takes `{"selection","highlight":1..8}`, or `"highlight":null` to clear; a missing or out-of-range `highlight` is 400 `invalid_bulk_request`. `count` is the number of pairs whose highlight changed, so repeating the same request returns 0. `selection` is the request's selection JSON, compacted. There is no CLI for bulk routes; drive them with curl over the instance socket.
- `DELETE /launchpad/{id}/link/{request_id}` publishes `launchpad.unlinked` with `{"launchpad_id","request_id"}`, the same shape as `launchpad.linked`.
- Report template event names are `report.template.added`, `report.template.removed`, and `report.template.restored`. Export does not publish an event.
