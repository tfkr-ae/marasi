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

To let a client that cannot open the socket, such as a GUI in a webview or a client on another machine, reach the control API, start the instance with a network address for its control listener, guarded by a control password you choose:

```sh
export MARASI_PASSWORD="$CONTROL_PASSWORD"
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service start \
  --project-name "$PROJECT" --port 0 --control-address 127.0.0.1:0 --json
```

`--control-address HOST:PORT` serves the same control API over plain HTTP at that address; port `0` picks a free port. Read the bound address from `control_listener` in `service start --json` or `service status --json` (`null` when there is none; `control listener network address:` in human output). `--password` or `MARASI_PASSWORD` on `service start` sets the control password; prefer the variable, because a flag shows in `ps` and shell history. With a password, every request to the address must send it as `Authorization: Bearer PASSWORD` (the scheme name is case-insensitive); a missing or wrong password gets 401 `unauthorized` with `WWW-Authenticate: Bearer`, and a password in the query string is never accepted. A password cannot start or end with whitespace or contain control characters. Without a password, the address answers every request with no authentication. The socket never asks for the password. The password lives only in the instance's memory: `service start` and `service restart` hand it to the instance process through its environment, never its arguments, `service restart` keeps it, and stopping the instance forgets it. Nothing is written to disk, and an old `<instance>.token` file is ignored. Status, list, events, and logs never show the password; do not print it where the user did not ask for it. A non-loopback address writes a warning in the instance log, and on stderr when stderr is a terminal, because the address has no TLS. An address already in use fails the start.

To change the network address of a running instance, use `service control start --address HOST --port PORT`, `service control update --address HOST --port PORT`, `service control stop`, and `service control status` (routes `POST /service/control-listener/start|update|stop` and `GET /service/control-listener/status`, with the bodies of the proxy `listener` routes). `start` and `update` require both flags, and port `0` picks a free port. Each prints the status (`status`, `control_listener`, `password_set`) with `--json`; `password_set` says whether a control password guards the address, and human `service control status` prints it as `password: set` or `password: none`; `control_listener` is `null` when the address is stopped, and a stopped address remembers no endpoint, so `start` must name one again. `start` on an active address is `listener_already_active`; `update` or `stop` on a stopped one is `listener_inactive`. An address in use or a host that cannot bind is `listener_unavailable` with the reason, and leaves the previous address serving. While a restart hands the address over, `update` and `stop` are `restart_in_progress`. The control password stays the same across start, update, and stop, and the socket stays available throughout. Stop and update close every connection and event stream on the old address; a request that made the change on that address gets its response first. Start and update write the non-loopback warning too. Events: `control_listener.started`, `control_listener.updated`, and `control_listener.stopped`, each with `status`, `control_listener`, and `password_set`.

For Pairing, which hands another device the network address and password to connect with, run `service control pair`. It prints a QR code of the JSON payload `{"url","password","instance"}` on stdout, then a blank line and the URL as text; `--json` prints only the payload. `password` is present only when one is set, so the human output shows the password inside the QR code: run it only when the user asks to pair a device. Locally the payload comes from `GET /service/control-listener/pairing`, which only the socket serves; on the network address it is 403 `socket_only`. With no network address running, the route is 409 `listener_inactive` and the command fails with `start one with marasi service control start --address HOST --port PORT`. On a wildcard address such as `0.0.0.0`, `url` uses the IP of the interface on the default route. `--host HOST` replaces the host in `url` and keeps the port, for an address the scanning device can reach, such as a VPN IP. A hostname in `--host` is refused when the instance has no password, because the address then accepts only an IP as the `Host`. With `--remote`, the payload is the remote URL, the password the CLI holds when the instance has one, and the instance name from `service status`.

Proceed only when status reports `running`, the intended instance and project, and a non-empty `proxy_listener`. To switch projects on a selected instance, use `project open --name NAME` or `project open --path PATH`, then confirm the new project with `service status`.

### Set flags from the environment

These environment variables set a flag when the flag is not passed. Each flag's `--help` names its variable as `(env MARASI_X)`. An empty variable counts as unset.

| Variable | Flag | Commands |
| --- | --- | --- |
| `MARASI_CONFIG_DIR` | `--config-dir` | every command |
| `MARASI_INSTANCE` | `--instance` | every command |
| `MARASI_REMOTE` | `--remote` | every command except `service start` and `service list` |
| `MARASI_PASSWORD` | `--password` | `service start` sets it on the instance; any command sends it with `--remote` |
| `MARASI_PROJECT` | `--project` | `service start` |
| `MARASI_PROJECT_NAME` | `--project-name` | `service start` |
| `MARASI_ADDRESS` | `--address` | `service start` |
| `MARASI_PORT` | `--port` | `service start` |
| `MARASI_CONTROL_ADDRESS` | `--control-address` | `service start` |

Precedence is the flag, then its variable, then the built-in default. A passed flag also ignores the variable it conflicts with: `--project-name` ignores `MARASI_PROJECT`, `--project` ignores `MARASI_PROJECT_NAME`, `--instance` ignores `MARASI_REMOTE`, and `--remote` ignores `MARASI_INSTANCE`. Two conflicting variables with no flag to settle them are a usage error that names both, such as `MARASI_PROJECT and MARASI_PROJECT_NAME cannot be used together: unset one`; the same holds for `MARASI_REMOTE` with `MARASI_INSTANCE`. Two conflicting flags stay a usage error. A value the flag would refuse fails as `invalid MARASI_X "VALUE"`. There is no `MARASI_JSON`: pass `--json` on each command that needs it.

### Drive an instance on another machine

To drive an instance whose control listener has a network address, point the CLI at that address with `--remote URL` instead of `--instance`:

```sh
export MARASI_PASSWORD="$CONTROL_PASSWORD"
marasi --remote "http://$CONTROL_ADDRESS" service status --json
marasi --remote "http://$CONTROL_ADDRESS" traffic list --json
```

`--remote` takes an `http` or `https` URL (`https` for a TLS reverse proxy in front of the address; a path in the URL prefixes every request), and `MARASI_REMOTE` sets it for the shell session; `--remote` wins over `MARASI_REMOTE`. The control password comes from `--password` or else `MARASI_PASSWORD`, and is sent as `Authorization: Bearer`; with neither, no `Authorization` header is sent, which an instance without a password accepts. `--remote` together with `--instance` is a usage error, and so is `MARASI_REMOTE` together with `MARASI_INSTANCE`; `--instance` ignores `MARASI_REMOTE`. A wrong password fails with `control password rejected by URL: check --password or MARASI_PASSWORD`, a missing one with `control password rejected by URL: the instance needs one: pass --password or set MARASI_PASSWORD`, and an address nothing answers at with `no instance answers at URL`. The local socket-path limit does not apply. Commands that only call the control API, `events`, and `service restart` behave and print exactly as they do through the socket, `--json` included. `service start` and `service list` act on this machine and refuse a remote. `service stop` sends the stop and returns once the instance stops answering at the URL: the connection is refused, or a reverse proxy answers in its place, such as with 502.

With a password, the instance accepts any `Host` header, so a hostname, a VPN name, a reverse proxy, or a container name all work. Without a password, it checks the `Host` header against the address it is bound to and answers 421 `host_mismatch` to any other name, so a TLS reverse proxy in front of it must send the bound address as `Host`, for example nginx `proxy_set_header Host 127.0.0.1:PORT;`. `service restart` hands the network address and the control password to the successor, so the URL and password keep working; a client retries a request that fails during the handover.

A browser GUI can call the network address from any origin: the instance echoes the request `Origin` and never allows credentials, so send the control password as `Authorization: Bearer PASSWORD` on every request, and read `GET /events` with a `fetch` stream rather than `EventSource`, which cannot send that header.

The instance and the CLI run on different machines, so read their output as the instance machine sees it:

- Printed addresses, such as the proxy listener and the CDP endpoint from `chrome start --cdp`, are the instance machine's. A loopback address there is not reachable from the CLI's machine.
- `listener interfaces` lists the instance machine's addresses, loopback included, to choose a `listener start` or `listener update` `--address` from (`--json`: `items` of `address`, `family` `ipv4` or `ipv6`, `interface`; IPv4 first, then by interface and address). A link-local IPv6 address carries its zone, such as `fe80::1%eth0`.
- `service status` reports the instance machine's operating system (`os:`; `os` in `--json`). Over a remote, `chrome path add` and `chrome path remove` default `--os` to it, so a path is tagged for the machine that runs Chrome. Pass `--os` to override.
- Over a remote, `service status` adds this CLI's version (`cli version:`; `cli_version` in `--json`) beside the instance's `version`. When they differ it prints `warning: this marasi is version X but the instance runs Y` on stderr, or puts that text in the `warning` field under `--json`. Commands still run; update the older side if a command or field is missing. No other command checks versions.

Over `--remote`, `wordlist add PATH` and `report template add PATH` read `PATH` on this machine, upload its contents under its base name, and delete the local file only after the instance accepts it, so add still moves the file. A refused upload (`wordlist_already_exists`, `report_template_already_exists`, an invalid name such as a hidden template name) or an instance that does not answer leaves the local file in place. The instance publishes `wordlist.added` or `report.template.added` as for a local add. Without a remote, both commands send the path and the instance moves the file itself.

Over `--remote`, project paths and names refer to the instance machine. `project list` lists the instance's `<config-dir>/projects`, with the same rows and `--json` shape as a local list there. `project open --path PATH` sends `PATH` unchanged, so it must be an absolute path on the instance machine; a relative path is refused with `invalid_project_request`. `project open --name NAME` is resolved in the instance's config directory, which creates its projects directory if needed; nothing is created on this machine. Without a remote, `project list` reads this machine's config directory and needs no running instance.

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
- `upstream_proxy` matches the name of the upstream proxy that carried the pair, with `=` or `!=` and exact case: `upstream_proxy = "burp"`. `upstream_proxy = ""` matches pairs sent directly. A pair records its route when its response is stored, including the upstream proxy a failed request tried. `traffic get` shows it as `upstream_proxy: NAME (scheme://host:port)` or `upstream_proxy: direct`, and JSON pair reads, traffic list items (`traffic list --json` and the other pair lists) and `traffic.response` events carry `upstream_proxy` as `{"name", "address"}`, or `null` for a pair with no response yet or one captured before routes were recorded. The snapshot never holds credentials and does not change when the upstream proxy is later updated or removed.
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
| Expose, move, or close the control API's network address | `service control` | `start` and `update` require both `--address` and `--port`. `pair` prints a QR code for Pairing another device. The socket keeps working whatever the network address does. See [Select the instance and project](#select-the-instance-and-project). |
| Change listener address or pause capture | `listener` | `start` and `update` require both `--address` and `--port`; `interfaces` lists the addresses the instance machine can bind. Stopping the listener leaves the service running. |
| Find named project files | `project list` | Lists projects under the selected config directory, or with `--remote` under the instance machine's. Each row has the file's modification time in UTC (`MODIFIED`, `modified_at` in RFC 3339); a symlinked project shows its target's. |
| Annotate captured traffic | `notes`, `traffic metadata` | Metadata updates replace the JSON rather than merge it. |
| Change or delete many pairs | `traffic bulk` | `highlight`, `link`, `unlink`, `metadata set`/`clear`, `note append`/`clear`, `delete`. Select with `--id` (repeatable) or `--ids-file` (`-` for stdin), or `-q` with the list page's `--as-of`. Bulk metadata sets one dotted key and leaves the rest. Nothing asks for confirmation, and `delete` also removes notes, links, and logs. |
| Count, map, or check traffic | `traffic count`, `traffic types`, `traffic sitemap`, `traffic validate` | `count --after <as_of>` counts pairs captured since a page. `count --by status_code` or `--by length` counts pairs per value, uncapped, `-` for none yet, and prints the `as_of` it stopped at on stderr. `sitemap --node <name>` lists a node's children; repeat `--node` to read several levels in one call, with matching counts under `-q`. `validate` exits 1 with the error position for a bad query, exclusion, or sort. |
| Check index and sitemap builds | `jobs` | `jobs list` shows progress; reads report incomplete results until a job is complete. `jobs rebuild sitemap` restarts one. |
| See the project's totals | `dashboard` | Prints `pairs`, `notes`, `launchpads` and `intercepted` from counts the project keeps, so it is cheap to repeat. Until the `intercepted_count` job is complete, `intercepted` can be low and a `notice:` line says so (`intercepted_count.complete` in `--json`). |
| Store client settings | `settings` | JSON values per client name and key. The client name `marasi` is reserved. |
| Launch an edited HTTP request | `launchpad` | Create a launchpad, optionally link captured traffic (`launchpad unlink` removes a link), then launch raw HTTP bytes. `launch` requires `--scheme`, either `http` or `https`. It waits for the final record, and an interrupt cancels the launch. A failed launch exits non-zero with its id in the error. `--detach` returns the pending launch id at once; end it early with `launchpad cancel`. `launchpad pending` lists pending launches; `launchpad status` reads one, pending or finished. `launchpad list` shows each launchpad's entry count and its newest entry's status code (`entry_count`, `last_status_code` in JSON); the status is `-` (`null`) when the launchpad is empty or its newest entry has no response (in flight, or cancelled or failed before any answer); a launch that cannot reach its origin is stored with the proxy's 502. The launch id is the id of the traffic pair it stores. `launchpad get -q` narrows a launchpad's entries with the same query as `traffic list -q`. `launchpad delete` removes a launchpad but keeps its entries in the traffic list; it is refused with `launch_pending` while a launch from it is pending. |
| Hold, edit, forward, or drop traffic | `checkpoint` | `forward` and `drop` take several UUIDs to resolve many items in one call; that prints `done`, `gone` (no longer held) and `failed` counts, and a bulk forward sends the original bytes (no `--file`, `--intercept-response` or stdin). HTTP edits replace the whole raw request or response, not just its body. An edit that does not parse is refused with `invalid_checkpoint_request` and the parse error, and the item stays held. WebSocket edits replace the payload. A held WebSocket item also shows `frame_count`, `compressed` (on the wire; the payload is always uncompressed) and `metadata`. Interception can leave clients waiting. Each item's `intercepted_at` (`INTERCEPTED AT` in the human `list`; `get` needs `--json`) is when it was held; a held response keeps its request's id but has its own time. `checkpoint.forwarded` and `checkpoint.dropped` events carry `request_id` (a WebSocket item's upgrade request, else `null`) and `reason`: `forwarded`, `dropped`, `abandoned` (the client left), or `shutdown`. |
| Inspect or inject WebSocket messages | `websocket`, `traffic websocket` | Injection and close require a live connection. Choose direction and opcode explicitly. |
| Redirect an upstream connection | `waypoint` | Maps an original host:port to an override host:port without changing DNS. |
| Manage the machine's upstream proxies | `upstream` | `list`, `add <name> <url> [--ca <pem-file>]`, `update <name> [--url …] [--ca …\|--no-ca]`, `remove <name>`. The list is shared by every instance on the config directory, and an edit, including a hand edit of `marasi_config.yaml`, applies to the next connection in each of them. The URL is `http`, `https` or `socks5://[user:pass@]host:port`; names follow the client-name pattern. `--ca` copies the PEM into the entry. `update` needs at least one of `--url`, `--ca` or `--no-ca`, and `--ca` and `--no-ca` exclude each other. Output shows `name`, `scheme`, `host`, `port`, `has_credentials` and `has_ca` (`CREDENTIALS` and `CA` columns, `yes`/`no`, in the human `list`), never credentials or the PEM. `add`, `update` and `remove` print a confirmation on stderr, or with `--json` the whole list as `items`. A bad name or URL is `invalid_upstream_proxy`, a duplicate `add` is refused with `upstream_proxy_already_exists`, and a missing name is `not_found`. Events, from the instance that made the edit only: `upstream_proxy.added`, `upstream_proxy.updated`, `upstream_proxy.removed`, each with the whole list as `items`. The human `list` marks the open project's selection in a `SELECTED` column and warns on stderr when the selection is not configured on this machine; `list --json` has `selected` (`""` for none) and `configured` instead. |
| Test an upstream proxy before relying on it | `upstream check` | `check <name> <target-url>` makes one test connection to the target (`http` or `https` URL) through any listed entry, selected or not, dialled exactly as traffic is; for `https` the target's certificate is verified too. Prints `upstream_proxy`, `target` (host:port), `outcome` and `message` (`--json`: `name`, `address`, `target`, `outcome`, `message`). `outcome` is `ok`, `refused`, `auth_rejected`, `tls_verification` or `timeout`, or `rejected` (the upstream proxy would not open the tunnel, e.g. the target is unreachable), `invalid` (the entry cannot be used) or `failed`. The command exits 0 whatever the outcome. A missing name is `not_found` and a bad target is `invalid_target`. It sends no request, stores no pair or proxy log, publishes no event and leaves the selection alone. |
| Send the open project's traffic through an upstream proxy | `upstream use` | `use <name>` or `use --none`. It prints a confirmation on stderr, or with `--json` `selected` and `configured`. The project stores the selection; new projects start with none, and opening a project restores its selection. Browser traffic, launches, Armory, Lua HTTP and WebSocket connections all leave through it, and new connections use it at once. `use` dials nothing, so a broken upstream proxy shows up as requests that fail with a 502 and an `ERROR` proxy log naming the pair (`context.upstream_proxy`, `context.address`, `context.cause`: `refused`, `auth_rejected`, `timeout`, `not_configured`, …). Traffic never falls back to a direct connection. A name this machine does not list is `not_found`. A project opened on a machine that lacks its selection still opens, but every request fails until you `use` a listed name or `--none`; `service status` then shows `upstream proxy: NAME (not configured on this machine; requests fail)` (`upstream_proxy`, `upstream_proxy_configured` in `--json`). Events: `upstream_proxy.selected` with `{"name": …}`. |
| Launch a proxied browser | `chrome` | Register an executable path and profile before launch. To automate it, see [Drive a browser with CDP](#drive-a-browser-with-cdp). |
| Record methodology and vulnerabilities | `test-case`, `finding` | Link captured request IDs as evidence. Score a finding with `--cvss-vector`, a CVSS 3.1 base vector such as `CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N` (the prefix is added when missing). Marasi works out `cvss_score`, so `--cvss-score` is optional and refused (`invalid_cvss_score`) when it disagrees; a malformed vector is `invalid_cvss_vector`. |
| Attach evidence files | `artifact` | Uploads require exactly one of `--finding` or `--test-case`, never both. |
| Modify traffic with Lua | `extension` | Inspect source and settings before enabling or calling an extension. See [Write Lua extensions](#write-lua-extensions) before changing source. |
| Read the scope or test a URL against it | `scope` | `list` prints the live `default_allow` and the include and exclude rules, each sorted by pattern then match type (`--json`: `default_allow`, `include` and `exclude` arrays of `pattern` and `match_type`). `check <url>` says whether one URL is in scope and names the matching rule; it sends no traffic. Neither changes rules, and there is no `scope add`: rules change only through Lua `marasi:scope()`. Both answer the rules even when Compass is disabled. A Lua run that changes the rules publishes one `scope.updated` event, with the `scope list --json` body, when it ends. |
| Manage attack inputs | `wordlist` | `add` moves the source file, uploading it over `--remote`. Copy it first if the original must remain. |
| Automate requests from templates | `armory` | Validate inputs before creating a run. A refused validate or create prints why, for example `invalid_armory_request: harpoon requires exactly one wordlist`. Creation does not start it. Starting sends traffic. A run whose instance stopped mid-run is `cancelled` once its project opens again. |
| Export assessment evidence | `report` | Template `add` moves the source file, uploading it over `--remote`. Exports default to draft and include test cases. Select `--output` explicitly. `--created-at` (RFC3339) keeps its offset, so the report prints that time in that zone; without it the instance stamps the export time in its machine's zone. `--custom-properties` takes a JSON object for the template's `.Metadata.CustomProperties`; if it holds `include_test_cases`, `--include-test-cases` replaces that value. |
| Watch live activity | `events` | Blocking stream with no replay. `--json` emits one event object per line. `traffic.changed` for a bulk link or unlink names its `target` (`kind`, `id`), and `extension.settings.updated` names the extension (`id`, `name`). |
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

Replace the source with `extension update`, which runs it at once and stores it only if the run succeeds. A failed run keeps the previous source and publishes no `extension.updated`, though any Lua that ran before the error stays in effect until the project reopens. A failed run or `extension call` is refused with `lua_error` and the Lua error, such as `lua_error: [string "..."]:3: attempt to call a nil value`. Then check `extension logs` and the affected traffic. While an extension is enabled, `events` also shows each print line as `extension.log`, and each `marasi.gui:render` and `marasi.gui:update` call as `extension.gui.rendered` and `extension.gui.updated`. `extension ui UUID` prints what the extension last drew per target (not toasts) and its last UI state per key, for a client that joins later.

## Verify and finish

After each change, read the affected record or status. After a launch, run `traffic get <launch_id>` and check `metadata.launch.status`: `completed`, `cancelled`, or `failed` with a `metadata.launch.reason`. After an Armory run, inspect the resulting captured traffic. After report export or artifact download, inspect the output file. Report the instance, project, relevant IDs, and output paths, or the failed command and its error.

Stop event subscribers you started. Cancel detached launches you started that you no longer need. Resolve held Checkpoint items and restore interception settings you changed. Stop only a service this session started, unless the user requests otherwise:

```sh
marasi --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service stop --json
```

Preserve project databases and exported evidence. Remove only temporary files created by this session.
