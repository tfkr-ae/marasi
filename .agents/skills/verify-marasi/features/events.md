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
- `traffic.response` has `id`, `status`, `status_code`, `content_type`, `length`, `metadata`, and `responded_at`. It has no `path`.
- Checkpoint event names are `checkpoint.held`, `checkpoint.forwarded`, `checkpoint.dropped`, and `checkpoint.updated`.
- WebSocket event names are `websocket.opened`, `websocket.message`, and `websocket.closed`.
- `traffic.index_complete` with `{"project":...}` is published once when a project's background traffic index build finishes. A project whose index is already complete on open publishes nothing. A switch cancels the build without an event; the next open resumes it.
- Report template event names are `report.template.added`, `report.template.removed`, and `report.template.restored`. Export does not publish an event.
