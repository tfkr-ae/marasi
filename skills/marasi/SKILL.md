---
name: marasi
description: Use the Marasi CLI to capture and inspect proxy traffic, launch edited requests, manage assessment evidence, and export reports.
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

## Show a stored response in a browser

To let the user see how a captured response renders, give them `http://marasi/view/$REQUEST_ID` to open in a browser that uses the proxy listener. Marasi redirects to the pair's original URL, and the browser's next request there, within 10 seconds, gets the stored response unchanged, without reaching the origin. Everything the page then loads goes to the live site and is captured. The URL takes the pair UUID, not its number. A plain-text 404 means no pair has that UUID in the open project; 409 means the pair has no response yet or is a WebSocket upgrade. A browser view can replace the browser's cookies with the stored `Set-Cookie`. `http://marasi/cert` serves the CA certificate in DER form through the same listener.

`traffic list` returns the newest page of traffic, oldest first within the page, so the newest pair is the last row. Follow `next_cursor` with `--cursor` for older pages, keeping the same query and passing the first page's `as_of` with `--as-of`. Cursors are opaque tokens; `--backward` with `prev_cursor` walks back toward newer pairs. `--exclude 'image/*'` (repeatable) hides content types; pairs with no content type yet always stay. Each pair has a `number`, its `#` in capture order; `-q 'number = 10'` finds one. `--order-by` sorts by one of `requested_at`, `number`, `method`, `host`, `path`, `status_code`, `content_type` or `length`, with optional `desc`, for example `--order-by 'length desc'`; pairs with no response sort last, and every page needs the same `--order-by`. `launchpad get --order-by` sorts entries the same way. In JSON, `request.raw` and `response.raw` are base64. Decode them before inspection or editing.

A capture is complete when `traffic get` returns the matching request and response. A client response alone does not prove that Marasi captured the exchange.

## Drive a browser with CDP

To automate a browser, attach to the one Marasi starts:

```sh
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" chrome start --profile "$CHROME_PROFILE" --cdp --json
```

Set `CHROME_PROFILE` to the chrome profile, or omit `--profile` to use `default-profile`; any other chrome profile must be registered first (`chrome --help`). The JSON holds `profile` and a `cdp` object with `http`, `ws` and `reused`. Pass `cdp.http` to Playwright's `chromium.connectOverCDP` or Puppeteer's `connect({ browserURL })`; use `cdp.ws` for a raw CDP client. `reused: true` means the chrome profile was already running with that CDP endpoint, so it can carry another session's tabs and cookies.

Run the same command again whenever you need the CDP endpoint later: it returns the live CDP endpoint for that chrome profile, marked `reused`, and opens a new window in it.

Error `chrome_running_without_cdp` means the chrome profile is already open without a CDP endpoint, and CDP cannot be turned on in a running Chrome. Ask the user to close that chrome profile, then run `chrome start --cdp` again, or start a different chrome profile with `--cdp`.

Marasi's proxy switches, quiet switches and certificate trust reach Chrome only through `marasi chrome start`. Start every browser you drive with it, never by running Chrome yourself with copied switches.

## Search traffic

`traffic list -q` takes an AIP-160 query over a fixed set of fields. `traffic list --help` lists the fields and the operators each one accepts. Marasi supports only part of AIP-160:

- Header and body text, notes and metadata use `:` (contains, ignoring case). Text terms need at least 3 characters. Bare text searches all of them.
- `host`, `method`, `scheme`, `path` and `content_type` use `=` or `!=` with exact case. A `*` at the start or end of the value matches anything there.
- `upstream_proxy` matches the name of the upstream proxy that carried the pair, with `=` or `!=` and exact case: `upstream_proxy = "burp"`. `upstream_proxy = ""` matches pairs sent directly. A pair records its route when its response is stored, including the upstream proxy a failed request tried. `traffic get` shows it as `upstream_proxy: NAME (scheme://host:port)` or `upstream_proxy: direct`, and JSON pair reads and `traffic.response` events carry `upstream_proxy` as `{"name", "address"}`, or `null` for a pair with no response yet or one captured before routes were recorded. The snapshot never holds credentials and does not change when the upstream proxy is later updated or removed.
- `status_code`, `length` (the response's Content-Length) and the timestamps take comparisons. Timestamps are quoted RFC 3339.
- `metadata.<key>` compares the JSON value at that key and is type-strict. Quote strings, but leave booleans and numbers bare.
- `launchpad` is launchpad membership. `launchpad = "<launchpad id>"` matches pairs in that launchpad, `launchpad != "<launchpad id>"` matches pairs not in it, including pairs in no launchpad, and `launchpad:*` matches pairs in any launchpad. It finds linked captured traffic as well as launched pairs, which `metadata.launchpad_id` does not.

```sh
-q 'status_code >= 500 AND host = "*.example.com"'
-q 'response_body:"password" OR response_head:"set-cookie"'
-q 'launchpad:* AND response_body:"token"'
-q 'metadata.launch.status = "cancelled"'
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
| Change or delete many pairs | `traffic bulk` | `highlight`, `link`, `unlink`, `metadata set`/`clear`, `note append`/`clear`, `delete`. Select with `--id` (repeatable) or `--ids-file` (`-` for stdin), or `-q` with the list page's `--as-of`. Bulk metadata sets one dotted key and leaves the rest. Nothing asks for confirmation, and `delete` also removes notes, links, and logs. |
| Count, map, or check traffic | `traffic count`, `traffic types`, `traffic sitemap`, `traffic validate` | `count --after <as_of>` counts pairs captured since a page. `sitemap --node <name>` lists a node's children; repeat `--node` to read several levels in one call, with matching counts under `-q`. `validate` exits 1 with the error position for a bad query, exclusion, or sort. |
| Check index and sitemap builds | `jobs` | `jobs list` shows progress; reads report incomplete results until a job is complete. `jobs rebuild sitemap` restarts one. |
| Store client settings | `settings` | JSON values per client name and key. The client name `marasi` is reserved. |
| Launch an edited HTTP request | `launchpad` | Create a launchpad, optionally link captured traffic (`launchpad unlink` removes a link), then launch raw HTTP bytes. `launch` requires `--scheme`, either `http` or `https`. It waits for the final record, and an interrupt cancels the launch. A failed launch exits non-zero with its id in the error. `--detach` returns the pending launch id at once; end it early with `launchpad cancel`. `launchpad pending` lists pending launches; `launchpad status` reads one, pending or finished. `launchpad list` shows each launchpad's entry count and its newest entry's status code (`entry_count`, `last_status_code` in JSON); the status is `-` (`null`) when the launchpad is empty or its newest entry has no response (in flight, or cancelled or failed before any answer); a launch that cannot reach its origin is stored with the proxy's 502. The launch id is the id of the traffic pair it stores. `launchpad get -q` narrows a launchpad's entries with the same query as `traffic list -q`. |
| Hold, edit, forward, or drop traffic | `checkpoint` | HTTP edits replace the whole raw request or response, not just its body. WebSocket edits replace the payload. Interception can leave clients waiting. |
| Inspect or inject WebSocket messages | `websocket`, `traffic websocket` | Injection and close require a live connection. Choose direction and opcode explicitly. |
| Redirect an upstream connection | `waypoint` | Maps an original host:port to an override host:port without changing DNS. |
| Manage the machine's upstream proxies | `upstream` | `list`, `add <name> <url> [--ca <pem-file>]`, `update <name> [--url …] [--ca …\|--no-ca]`, `remove <name>`. The list is shared by every instance on the config directory. The URL is `http`, `https` or `socks5://[user:pass@]host:port`; names follow the client-name pattern. `--ca` copies the PEM into the entry. Output shows `name`, `scheme`, `host`, `port`, `has_credentials` and `has_ca`, never credentials or the PEM. A duplicate `add` is refused with `upstream_proxy_already_exists`, and a missing name is `not_found`. `list --json` also has `selected` (the open project's selection, `""` for none) and `configured`. |
| Send the open project's traffic through an upstream proxy | `upstream use` | `use <name>` or `use --none`. The project stores the selection; new projects start with none. Browser traffic, launches, Armory, Lua HTTP and WebSocket connections all leave through it, and new connections use it at once. `use` dials nothing, so a broken upstream proxy shows up as requests that fail with a 502 and an `ERROR` proxy log naming the pair (`context.upstream_proxy`, `context.address`, `context.cause`: `refused`, `auth_rejected`, `timeout`, `not_configured`, …). Traffic never falls back to a direct connection. A name this machine does not list is `not_found`. A project opened on a machine that lacks its selection still opens, but every request fails until you `use` a listed name or `--none`; `service status` then shows `upstream proxy: NAME (not configured on this machine; requests fail)` (`upstream_proxy`, `upstream_proxy_configured` in `--json`). Events: `upstream_proxy.selected` with `{"name": …}`. |
| Launch a proxied browser | `chrome` | Register an executable path and profile before launch. To automate it, see [Drive a browser with CDP](#drive-a-browser-with-cdp). |
| Record methodology and vulnerabilities | `test-case`, `finding` | Link captured request IDs as evidence. |
| Attach evidence files | `artifact` | Uploads require exactly one of `--finding` or `--test-case`, never both. |
| Modify traffic with Lua | `extension` | Inspect source and settings before enabling or calling an extension. See [Write Lua extensions](#write-lua-extensions) before changing source. |
| Manage attack inputs | `wordlist` | `add` moves the source file. Copy it first if the original must remain. |
| Automate requests from templates | `armory` | Validate inputs before creating a run. Creation does not start it. Starting sends traffic. |
| Export assessment evidence | `report` | Template `add` moves the source file. Exports default to draft and include test cases. Select `--output` explicitly. |
| Watch live activity | `events` | Blocking stream with no replay. `--json` emits one event object per line. |
| Update Marasi | `update`, `service restart` | Install only when the user asks. A running instance checks and installs: `update` shows the available build and its notes, `update install` streams progress (one event object per line under `--json`) and exits 1 on `update.failed`. Running instances keep their build until `service restart`, which ends Armory runs and launches and refuses while Checkpoint items are held. `update reinstall` can install an older build and needs `--yes`, because newer projects may not open in it. |
| Diagnose proxy activity | `logs` | Filter traffic separately with `traffic list`; logs are not captured request/response pairs. |

## Write Lua extensions

An extension is Lua source stored in the project. The proxy calls the hooks the source defines: `startup` when it loads, and `processRequest`, `processResponse`, and `processWebSocketMessage` for traffic. Only the `checkpoint` extension's `intercept*` hooks decide what Checkpoint holds. `extension call` runs any global function. Read the API for the installed version before you write Lua, then read each library or type you use:

```sh
marasi extension api
marasi extension api marasi.repo
marasi extension api req
```

Call library functions and object methods with `:`, for example `marasi.strings:upper(s)` and `request:method()`. A call with `.` passes the wrong arguments. The sandbox has no `string`, `io`, or `os` library. `drop()` stops a request, response, or message from being sent on. A Lua error in a hook is logged and the item continues. A hook interrupted by a project switch, service stop, or client disconnect drops the item.

Replace the source with `extension update`, which runs it at once, then check `extension logs` and the affected traffic.

## Verify and finish

After each change, read the affected record or status. After a launch, run `traffic get <launch_id>` and check `metadata.launch.status`: `completed`, `cancelled`, or `failed` with a `metadata.launch.reason`. After an Armory run, inspect the resulting captured traffic. After report export or artifact download, inspect the output file. Report the instance, project, relevant IDs, and output paths, or the failed command and its error.

Stop event subscribers you started. Cancel detached launches you started that you no longer need. Resolve held Checkpoint items and restore interception settings you changed. Stop only a service this session started, unless the user requests otherwise:

```sh
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service stop --json
```

Preserve project databases and exported evidence. Remove only temporary files created by this session.
