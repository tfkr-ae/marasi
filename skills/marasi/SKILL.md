---
name: marasi
description: Use the Marasi CLI to capture and inspect proxy traffic, replay requests, manage assessment evidence, and export reports.
disable-model-invocation: true
---

# Marasi

Marasi is a local HTTP/HTTPS intercepting proxy. A service instance owns a proxy listener and an open project. The project is a `.marasi` SQLite database containing captured traffic and assessment evidence.

## Select the instance and project

Use `marasi` on PATH. Read `marasi --help` for the installed version's commands and default config directory.

Carry the same `--config-dir` and `--instance` on every command in a session. The default instance is `default`. Prefer `--json` for structured output and check the exit code before consuming it. Commands that write certificate or artifact bytes need their normal output mode instead.

Inspect existing instances before starting or changing one:

```sh
marasi --config-dir "$CONFIG_DIR" service list --json
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service status --json
```

Set `CONFIG_DIR` to the intended config directory and `INSTANCE` to the intended instance name. Before making changes, confirm that status identifies the intended `instance` and absolute `project` path. Use an existing instance only when the user has selected it.

For a new session, choose an unused instance name and the intended project. A project has an exclusive lock, so concurrent instances need different projects. Keep a custom config directory short and absolute. The control socket path `<config-dir>/instances/<instance>.sock`, including its terminating byte, must fit within 104 bytes.

```sh
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service start \
  --project-name "$PROJECT" --address 127.0.0.1 --port 0 --json
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service status --json
```

Set `PROJECT` to the chosen project name. `--project-name` selects `<config-dir>/projects/<name>.marasi`. Use `--project /absolute/path/project.marasi` instead for a file elsewhere. These selectors are mutually exclusive. `--port 0` assigns a free port. Start launches a detached service with its listener already running.

Proceed only when status reports `running`, the intended instance and project, and a non-empty `proxy_listener`. To switch projects on a selected instance, use `project open --name NAME` or `project open --path PATH`, then confirm the new project with `service status`.

## Capture and inspect traffic

Set `PROXY_LISTENER` from status, not from the requested port.

For HTTP capture:

```sh
curl --noproxy '' --proxy "http://$PROXY_LISTENER" "$TARGET_URL"
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" traffic list \
  -q "host = \"$TARGET_HOST\" AND path = \"$PATH_PREFIX*\"" --limit 20 --json
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" traffic get "$REQUEST_ID" --json
```

Set `TARGET_HOST` to the captured request's exact host and `PATH_PREFIX` to its path prefix. Set `REQUEST_ID` from the matching list entry. `--noproxy ''` prevents curl's `NO_PROXY` settings from bypassing the proxy, including for localhost.

For HTTPS, fetch the running instance's loaded CA and give it to the client:

```sh
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" certificate get > "$CA_FILE"
curl --noproxy '' --proxy "http://$PROXY_LISTENER" --cacert "$CA_FILE" "$TARGET_URL"
```

Set `CA_FILE` to a session-owned file. Prefer client-specific trust over changing the system trust store.

Traffic lists are newest-first. Follow `next_cursor` with `--cursor` for older pages, keeping the same query. In JSON, `request.raw` and `response.raw` are base64. Decode them before inspection or editing.

A capture is complete when `traffic get` returns the matching request and response. A client response alone does not prove that Marasi captured the exchange.

## Search traffic

`traffic list -q` takes an AIP-160 query over a fixed set of fields. `traffic list --help` lists the fields and the operators each one accepts. Marasi supports only part of AIP-160:

- Header and body text, notes and metadata use `:` (contains, ignoring case). Text terms need at least 3 characters. Bare text searches all of them.
- `host`, `method`, `scheme`, `path` and `content_type` use `=` or `!=` with exact case. A `*` at the start or end of the value matches anything there.
- `status_code` and the timestamps take comparisons. Timestamps are quoted RFC 3339.
- `metadata.<key>` compares the JSON value at that key and is type-strict. Quote strings, but leave booleans and numbers bare.

```sh
-q 'status_code >= 500 AND host = "*.example.com"'
-q 'response_body:"password" OR response_head:"set-cookie"'
-q 'metadata.launchpad = true'
-q 'method = "POST" AND (path = "/api/*" OR path = "/graphql")'
```

OR binds tighter than AND, so `a AND b OR c` means `a AND (b OR c)`. Add parentheses whenever you mix them.

In JSON, `index.complete: false` means the search index is still being built. Text searches can miss older traffic until it reports `true`. Do not treat an empty text search as proof of absence while it is `false`.

## Task map

Use the listed command group's `--help`, then the chosen subcommand's `--help` for flags.

| Task | Command group | Guidance |
| --- | --- | --- |
| Change listener address or pause capture | `listener` | `start` and `update` require both `--address` and `--port`. Stopping the listener leaves the service running. |
| Find named project files | `project list` | Lists projects under the selected config directory. |
| Annotate captured traffic | `notes`, `traffic metadata` | Metadata updates replace the JSON rather than merge it. |
| Replay an edited HTTP request | `launchpad` | Create a launchpad, optionally link captured traffic, then launch raw HTTP bytes. `launch` requires `--scheme`, either `http` or `https`. |
| Hold, edit, forward, or drop traffic | `checkpoint` | HTTP edits replace the whole raw request or response, not just its body. WebSocket edits replace the payload. Interception can leave clients waiting. |
| Inspect or inject WebSocket messages | `websocket`, `traffic websocket` | Injection and close require a live connection. Choose direction and opcode explicitly. |
| Redirect an upstream connection | `waypoint` | Maps an original host:port to an override host:port without changing DNS. |
| Launch a proxied browser | `chrome` | Register an executable path and profile before launch. |
| Record methodology and vulnerabilities | `test-case`, `finding` | Link captured request IDs as evidence. |
| Attach evidence files | `artifact` | Uploads must link to a finding, a test case, or both. |
| Modify traffic with Lua | `extension` | Inspect source and settings before enabling or calling an extension. |
| Manage attack inputs | `wordlist` | `add` moves the source file. Copy it first if the original must remain. |
| Automate requests from templates | `armory` | Validate inputs before creating a run. Creation does not start it. Starting sends traffic. |
| Export assessment evidence | `report` | Template `add` moves the source file. Exports default to draft and include test cases. Select `--output` explicitly. |
| Watch live activity | `events` | Blocking stream with no replay. `--json` emits one event object per line. |
| Diagnose proxy activity | `logs` | Filter traffic separately with `traffic list`; logs are not captured request/response pairs. |

## Verify and finish

After each change, read the affected record or status. After replay or an Armory run, inspect the resulting captured traffic. After report export or artifact download, inspect the output file. Report the instance, project, relevant IDs, and output paths, or the failed command and its error.

Stop event subscribers you started. Resolve held Checkpoint items and restore interception settings you changed. Stop only a service this session started, unless the user requests otherwise:

```sh
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service stop --json
```

Preserve project databases and exported evidence. Remove only temporary files created by this session.
