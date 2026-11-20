# Marasi

[![Go reference](https://pkg.go.dev/badge/github.com/tfkr-ae/marasi.svg)](https://pkg.go.dev/github.com/tfkr-ae/marasi)

<img src="images/logo.svg" width="150" alt="Marasi">

Marasi is an application security testing proxy controlled through the `marasi` CLI.
It captures HTTP, HTTPS, and WebSocket traffic, stores assessment data in SQLite
projects, and supports Lua extensions. The proxy is also available as a Go library.

[marasi.app](https://marasi.app)

## Features

- Captured traffic with complete HTTP requests and responses, metadata, and notes.
- Checkpoint to hold, forward, and drop in-flight requests, responses, and WebSocket messages.
- WebSocket inspection, frame injection, and live connection control.
- Launchpad to group captured pairs and launch modified raw HTTP requests.
- Armory request templates with payload positions and wordlist-driven runs.
- Test cases, findings, and artifacts to record assessment work and link evidence.
- Report generation with configurable templates.
- Lua extensions with settings, logs, and callable functions.
- Scope rules and waypoints for traffic filtering and host-and-port routing.
- Chrome profiles and launch with proxy settings.
- SQLite `.marasi` projects that store traffic and assessment data.

New projects include the `compass`, `checkpoint`, and `workshop` Lua extensions.
`compass` manages scope rules. `checkpoint` supplies rules for which traffic
Checkpoint holds. `workshop` provides a Lua development environment.

## Service mode and CLI

Each named service instance runs as a detached process and owns one open project.
`service start` returns after the instance is ready. The CLI sends HTTP control API
requests over a per-instance Unix-domain socket on macOS, Linux, and Windows.

The control listener and proxy listener are separate. Stopping the proxy listener
leaves the service instance available for inspection. `service stop` shuts down
the service instance. `service restart` hands the instance name, open project,
and proxy listener address to a new instance started from the installed build.
If the new instance does not become ready, the old one keeps running. A restart
ends Armory runs, held Checkpoint items, live connections, and launches. It is
refused while Checkpoint items are held unless `--force` is given.

Projects are SQLite `.marasi` files. A project can be owned by only one service
instance at a time. `project open` changes the open project without restarting
the proxy listener.

### Build and test commands

The source requires Go 1.26 or later. These commands run from the repository root.

| Command | Result |
| --- | --- |
| `make build` | Builds `dist/marasi` for this machine without CGO. |
| `make install` | Builds and installs `marasi` in `$HOME/.local/bin`. `PREFIX` overrides `$HOME/.local`. |
| `make windows-amd64` | Builds `dist/marasi-windows-amd64.exe`. |
| `make all` | Builds the targets listed by `TARGETS` in [Makefile](Makefile). |
| `make test` | Runs `go test ./...`. |
| `make conformance` | Runs the conformance suites in Docker through Marasi and scores each case against its committed direct baseline and deviations file. With no `SUITES`, it runs the HTTP suites, `h1-origin`, `h1-client`, `h2-client`, `h2-origin` and `stream-origin`, which all block a merge. It exits non-zero on a new deviation, a known deviation that went away, or a stale deviations entry. |
| `make conformance-baseline` | Runs the selected cases with no proxy and merges them into the direct baselines. For `h1-origin` it records each case twice and leaves out, with an error, a case whose two recordings differ. |
| `make conformance-update-known PHASE=<n>` | Runs the selected cases, then rewrites the known entries in `deviations.json` and the suite's `score.json`. |
| `make autobahn`, `make autobahn-baseline` | `make conformance` and `make conformance-baseline` with `SUITES=autobahn`. |

### Conformance harness

The conformance harness measures what Marasi changes in the traffic between a
client and an origin. It records every case twice: directly (the direct
baseline) and through Marasi. A case passes when the two recordings match,
apart from the entries in the suite's `deviations.json`. Today it has five
suites:

- `autobahn` runs the Autobahn WebSocket testsuite, with Marasi on the host.
- `h1-origin` sends each origin fixture case's HTTP/1.1 requests with the
  harness's own client, as an absolute-form request (`absolute`), as plain
  HTTP inside `CONNECT` (`connect-plain`) and as TLS inside `CONNECT`
  (`connect-tls`). The fixtures, in `internal/conformance/h1origin/catalogue.go`,
  cover chunked bodies and trailers, Content-Length and close-delimited bodies,
  field name case, order and whitespace, `Connection`, `Keep-Alive`, transfer
  codings, 1xx, HEAD, 204 and 304, an early close, a reset mid-body, several
  requests on one connection, and request fields that must reach the origin
  as sent. Its recording holds the responses the client received and
  the requests the origin received. Hop-by-hop fields and framing are left out
  of the comparison and checked by leg rules instead, on what Marasi sent on
  each leg: `leg:connection` (remove `Connection` and the fields it names, RFC
  9110 §7.6.1), `leg:framing` (valid framing that matches the body, RFC 9112
  §6), `leg:cl-and-te` (never both `Content-Length` and `Transfer-Encoding`, RFC
  9112 §6.1), `leg:field-order` (keep the order of same-name field lines, RFC
  9110 §5.3) and `leg:via` (send `Via`, RFC 9110 §7.6.3, accepted: Marasi does
  not announce itself). A broken MUST rule fails like a deviation unless
  `deviations.json` lists its ID as the field; a broken SHOULD rule warns.
  Marasi runs in a Linux container built by `make linux-<arch>` for this
  machine's architecture (`LINUX_ARCH`), on the
  host's network, and resolves the fixtures' name `origin.test` to loopback.
  The harness drives it through a control listener on loopback.
- `h1-client` scores Marasi's HTTP/1.1 client leg with
  [Http11Probe](https://github.com/MDA2AV/Http11Probe), a third-party tester
  built in Docker from a pinned commit, against nginx pinned by digest. Neither
  has a proxy option, so Http11Probe connects to the harness's shim, which
  splices each connection to nginx directly, or through Marasi with `CONNECT`
  (`connect-plain`), and adds TLS itself for `connect-tls`. A case is an
  Http11Probe test with the approach, and its recording is the test's verdict
  plus the shim's capture of the connection, compared like `h1-origin`'s client
  side. The leg rules run on the responses Marasi sent. Marasi runs in the same
  container as for `h1-origin`. Marasi routes a request inside `CONNECT` by its
  `Host` field, which names the shim's port, so nginx listens on that port at
  `127.0.0.2`, where Marasi resolves `nginx.test`. The first run builds the
  Http11Probe image, which takes a few minutes.
- `h2-client` scores Marasi's HTTP/2 client leg with
  [h2spec](https://github.com/summerwind/h2spec) v2.6.0, installed with
  `go install` at a pinned version so it never enters `go.mod`, against nginx's
  TLS listener that offers ALPN `h2`. h2spec does its own TLS (`-t -k`) and
  connects to the shim, which only tunnels, directly or through Marasi with
  `CONNECT` (`connect-tls`). The harness runs h2spec once per case, and a
  case's recording is h2spec's verdict from its JUnit report (`not-run`, with
  h2spec's error, when h2spec could not start the case) plus how the shim saw
  the connection end. h2spec trusts any certificate, so before h2spec runs the
  harness completes one TLS handshake through the same shim that trusts only
  Marasi's CA (the harness CA in the direct run). A run whose shim does not go
  through Marasi fails there. Each known entry names the value Marasi gave
  (`got`), so a case that fails in another way is new. It runs all four
  conformance runs (`make conformance SUITES=h2-client RUNS=proxy` for a
  quick one).
- `h2-origin` sends each h2 origin fixture case's requests as HTTP/2 streams
  inside `CONNECT` over TLS (`connect-tls`), offering ALPN `h2` then
  `http/1.1`, to fixtures that script frames and serve ALPN `h2`. Its
  recording holds, for each side, the negotiated protocol, each stream's
  decoded header blocks with pseudo-header fields, DATA bytes, trailers,
  where END_STREAM fell and the RST_STREAM code, and the connection's GOAWAY
  code and the streams it says were processed. SETTINGS, WINDOW_UPDATE, PING,
  PRIORITY, DATA splitting, padding, HPACK encoding and stream IDs are not
  recorded, so they never differ; the client and the fixtures keep to each
  other's flow-control windows, and a body over 4 KiB is recorded by its
  length and SHA-256. The catalogue covers field order, cookie crumbs, 103
  Early Hints, a mismatched `content-length`, CONTINUATION, bodies larger
  than the flow-control window, concurrent streams, trailers, `RST_STREAM`
  partway through a body, `GOAWAY`, a gzip body, and a request redirected to
  an origin that speaks only HTTP/1.1. That case's requests carry
  `x-conformance-redirect` through Marasi, and the Lua the suite installs in
  every run (`Setup.Lua`) sends them to the HTTP/1.1 origin; directly, the
  client speaks HTTP/1.1 to that origin itself. It runs all four conformance
  runs. Through Marasi the client trusts only Marasi's CA, so a recorded case
  shows Marasi in the path even if Marasi answered ALPN with `http/1.1` and
  recorded no HTTP/2 traffic. Marasi runs in the same container as for
  `h1-origin`.
- `stream-origin` is our own origin fixtures for streamed pairs
  (`internal/conformance/streamorigin/catalogue.go`). A client and an origin
  each follow a script: send a head, a stream message or trailers, or wait
  for the peer's next one. The recording is everything that reached either
  side, in order: heads, each stream message as its message framing cuts it
  (a gRPC envelope or a server-sent event, however the wire split it),
  trailers, the end, a reset, and a side that waited 5 seconds and got
  nothing. In the wait-for-answer cases each side sends its next stream
  message only after the peer's last one arrived, so through a proxy that
  reads a body whole the waiting side times out and the order shows it; a
  proxy that delivers everything at the end cannot pass. No timing is
  scored. An h2 case runs on `connect-tls` with ALPN `h2`; an HTTP/1.1 case
  runs on all three approaches. A server-sent event stream has no request
  body to answer in, so its client answers the origin in process. It runs
  all four conformance runs, with Marasi in the same container as for
  `h1-origin`.

For TLS, the harness generates a CA on each run and never writes its key to
disk. The fixtures serve certificates it issues. In Marasi's container,
`SSL_CERT_FILE` names the image's system roots plus that CA, so Marasi verifies
the fixtures with no Marasi setting changed. The direct run's client trusts the
harness CA. Through Marasi, the client trusts Marasi's CA certificate, fetched
with `marasi certificate get`.

Each suite keeps its committed files in `internal/conformance/baselines/<suite>/`:

- One direct baseline file for each target, for example `fuzzingclient-strict.json`.
- `deviations.json`. Each entry names the case IDs it covers (a glob), the
  conformance runs, the field that may differ, its kind, a reason, and an
  optional RFC. An entry may also name `targets` (globs over the suite's
  targets, such as `fuzzingserver-*`; none means every target) and `got` (the
  only value through Marasi it lists). An `accepted` entry is on the allow-list: Marasi keeps that
  difference on purpose, and only a person edits it. A `known` entry is a
  difference Marasi does not mean to keep, with the phase expected to remove it.
- `score.json`: for each conformance run, target and client approach, the cases
  that matched the baseline out of the cases run, and the leg rules that passed
  and failed (one per rule and case).

The suites need Docker with host networking. `make conformance` runs four
conformance runs: `proxy` (no interception), `checkpoint-forward` (global
Checkpoint with an auto-resolver that forwards each item unchanged),
`checkpoint-edit` (the resolver forwards each item as an edit with the same
bytes), and `extension` (a workshop hook sets each payload or body to itself:
`processWebSocketMessage` for WebSocket, `processRequest` and
`processResponse` for HTTP). A suite can also install Lua in every run:
`h2-origin` installs the rule that redirects its `redirect-to-http1` case. Autobahn runs both modes (`fuzzingclient`, `fuzzingserver`) against
two peers: Autobahn's strict testee and the harness's lenient echo peer.
Variables narrow a run:

```bash
make conformance SUITES=autobahn CASES='3.*,7.9.*' RUNS=proxy MODES=fuzzingclient PEERS=strict
make autobahn EXCLUDE='9.*,12.*,13.*' VERBOSE=1
make conformance SUITES=h1-origin APPROACHES=connect-plain
make conformance SUITES=h2-origin RUNS=proxy
make conformance SUITES=h2-client RUNS=proxy
```

Reports are written to `dist/conformance/<suite>/<run>/`: a `summary.md` with
the score, then new deviations, known deviations that went away, known
deviations, the differences on the allow-list, and leg-rule results, a JSON
diff with both recordings for each
failing case under `diffs/`, Marasi's log, and the tester's output for each
target. The run log is `dist/conformance/conformance.log`. In GitHub Actions
the summary is also added to the job summary. `RUNS=direct,proxy` refreshes the
baseline before comparing. On an arm64 Mac, where the amd64 Autobahn image runs
under emulation, the Autobahn direct baseline takes about 20 minutes. The strict
peers negotiate permessage-deflate, so 12.x and 13.x are most of a direct
strict suite.

#### Updating baselines and known entries

A deviation is "new" when no entry in `deviations.json` lists it: the run
fails. If the change meant it, or it is a difference a later phase
removes, record it as a known entry with the phase expected to remove it:

```bash
make conformance-update-known SUITES=h1-origin PHASE=2
```

A known deviation "went away" when a run covered its cases and the difference
no longer appears: Marasi now matches the direct baseline there. That also
fails the run, so the score only improves and the file stays true. It usually
means your change fixed something: remove the entry, or run
`make conformance-update-known`, which narrows or removes went-away known
entries, adds a known entry for each new deviation, and rewrites `score.json`.
It never touches `accepted` entries; only a person edits those. An entry that
matched nothing is stale and fails too, when a conformance run and target it
lists ran. With `CASES`, `EXCLUDE` or `APPROACHES` set, only an entry that
covers a case that ran can be stale. `RUNS`, `MODES` and `PEERS` do not turn
the check off: an entry for a run or target that did not run is never stale. Review the diff of
`deviations.json` and `score.json` before you commit it.

The direct baseline changes only when a fixture, a pinned tester or image, or
the harness changes. Refresh it with `make conformance-baseline SUITES=<suite>`
(or `CASES=` for the cases you touched) and review its diff before you commit.

#### Quick local runs

Each command scores one suite and one run, the same as one CI job:

```bash
make conformance SUITES=h1-origin RUNS=proxy
make conformance SUITES=h1-client RUNS=proxy CASES='SMUG-CL-TE-BOTH@*'
make conformance SUITES=h2-origin RUNS=proxy
make conformance SUITES=stream-origin RUNS=proxy
make autobahn EXCLUDE='9.*,12.*,13.*'
```

`make conformance` with no `SUITES` runs `h1-origin`, `h1-client`,
`h2-client`, `h2-origin` and `stream-origin`, the suites that block a merge. `h1-client` runs only
the Http11Probe tests that `CASES` names exactly (`ID@*` covers every
approach); with a glob in the ID it runs all 213 tests and narrows afterwards.
A narrowed run (`RUNS`, `CASES` and the like) checks only the entries its cases
cover.

### Conformance checks in CI

The `conformance` workflow (`.github/workflows/conformance.yml`) runs on every
pull request, every Monday, and when you start it manually. Every job runs on
`ubuntu-24.04`, adds its summaries to the job summary, and uploads
`dist/conformance/` as an artifact named after the job. On Linux, Marasi's
container is built for amd64 and shares the runner's network, where all of
`127.0.0.0/8` is loopback.

On a pull request, the `changed paths` job runs `git diff` against the base
and decides which suites run. The schedule and manual dispatch run every
suite. A suite runs when the pull request changes Marasi on the HTTP path (root
`*.go`, `go.mod`, `go.sum`, `Makefile`, `cmd/`, `core/`, `domain/`,
`listener/`, `rawhttp/`, `db/`, `extensions/`, `service/`), the shared harness,
the workflow, or that suite's own package and baselines. A change only to docs,
reports, Armory, Compass, wordlists or another suite skips it.

- `h1-client (<run>)`, `h1-origin (<run>)`, `h2-client (<run>)`,
  `h2-origin (<run>)` and `stream-origin (<run>)`, one job for each of `proxy`, `checkpoint-forward`,
  `checkpoint-edit` and `extension`, are the required checks. When none of a
  suite's paths changed they still run, skip their steps and pass, because a
  skipped matrix job would never report under its check name.
- `autobahn (<run>, <mode>)` takes hours, so its path list is narrower: the
  WebSocket code, the autobahn suite and the shared harness, plus `db/`,
  `proxy.go`, `modifiers.go`, `checkpoint.go` and `extensions/`. The paths
  include `db/` and `proxy.go` because a WebSocket relay waits on the database
  writer's queue: a change that slows the writer can fail a case even though
  it touches no WebSocket code. On a failure, the job's artifact holds the
  per-case reports, Marasi's log, and a resource log, and the job log shows
  kernel OOM messages.

### Autobahn checks

Run Autobahn locally on a change to its paths:

1. Before you push, run the quick check. It skips the large-message (9.x) and
   compression (12.x, 13.x) sections and takes about 7 minutes:

   ```bash
   make autobahn EXCLUDE='9.*,12.*,13.*'
   ```

2. Before you merge, run the full suite. It takes about 1.5 to 2 hours on an
   arm64 Mac:

   ```bash
   make autobahn
   ```

Both commands exit non-zero on a new deviation from the committed baseline.
The 6.4.1 to 6.4.4 cases are accepted entries in
`internal/conformance/baselines/autobahn/deviations.json` and are listed
separately. If a deviation looks unrelated to the change, run
`make autobahn-baseline` and compare the baseline diff before you commit it:
the baseline changes only with the pinned Autobahn image or the harness.

A build without Make uses `go build -o marasi ./cmd/marasi`, or
`go build -o marasi.exe ./cmd/marasi` on Windows. If a parent `go.work` excludes
the checkout, `GOWORK=off` disables that workspace for builds and tests.

### CLI flags

`marasi --help` lists commands. Each command's `--help` lists its subcommands,
arguments, and flags. The examples below use an installed `marasi` binary.

The global flags are:

| Flag | Meaning |
| --- | --- |
| `--instance NAME` | Selects a service instance. Defaults to `default`. |
| `--config-dir PATH` | Selects the machine config directory. |
| `--json` | Prints command output as JSON. `events`, `update install`, and `update reinstall` emit one JSON object per line. |

### Service commands

This start command creates or opens the named project `assessment` and starts
the service instance `assessment` with a proxy listener at `127.0.0.1:8080`:

```sh
marasi --instance assessment service start --project-name assessment --address 127.0.0.1 --port 8080 --json
```

`--port 0` requests an allocated port. The start result reports the bound address.
Without a project flag, `service start` opens the named project `scratchpad`.

Other service and project commands include:

```sh
marasi --instance assessment service status --json
marasi service list --json
marasi project list --json
marasi --instance assessment project open --name another-assessment --json
marasi --instance assessment service restart --json
marasi --instance assessment service stop --json
```

`service status` and `service list` report `restart required: yes` when the
installed build changed after the instance started. `service restart` moves the
instance to the installed build and keeps its name, project, and proxy listener.

### Update commands

A running service instance checks for and installs updates. With no running
instance, the update commands fail and tell you to start one with
`marasi service start`.

```sh
marasi update                          # installed build, channel, available update
marasi update check                    # check the channel now
marasi update install                  # install the update and follow its progress
marasi update channel                  # print the channel
marasi update channel nightly          # switch to release or nightly
marasi update reinstall --yes          # install the latest build on the channel, even if older
```

`marasi update` shows the first lines of the update's release notes and a link
to the release page. `--json` returns the full notes. `update install` writes
its steps (download, verify, install) to stderr. With `--json` it writes each
`update.progress`, `update.installed`, or `update.failed` event as one JSON
line. It exits 1 when the install fails. Ctrl-C stops following, but the
install continues on the instance. `marasi update` shows its state.

The instance replaces its own executable, with symlinks resolved. Running
instances keep their build until `service restart`. Switching channel never
installs a build. From nightly to release, the installed nightly stays until a
newer release exists. `update reinstall` installs the latest release now. It
requires `--yes`, because projects opened in a newer build may not open in it.

### Traffic and CA certificate commands

These commands inspect stored traffic, subscribe to live events, and export the
loaded CA certificate:

```sh
marasi --instance assessment traffic list --limit 20 --json
marasi --instance assessment traffic list -q 'host = "example.com" AND method = "GET" AND status_code = 200' --json
marasi --instance assessment traffic get UUID --json
marasi --instance assessment events --json
marasi --instance assessment certificate get > marasi-ca.pem
```

`traffic list -q` (or `--query`) takes an AIP-160 query, sent to the control API
as `GET /traffic?q=`. It combines exact field conditions such as
`host = "api.example.com"` with text search such as `response_body:"password"`,
`note:"idor candidate"`, `metadata:"workshop"`, or bare `"hbGci"`. Run `marasi traffic list --help` for the fields. In a
query, `OR` binds tighter than `AND`, so `a AND b OR c` means `a AND (b OR c)`.
An invalid query returns 400
`{"error":"invalid_query","message":"...","position":N}`.

`traffic list --exclude 'image/*'` (repeatable, `GET /traffic?exclude=`) hides
pairs of a content type: an exact type, or a whole major type such as `image/*`.
Pairs with no content type yet are never hidden. Each page reports
`next_cursor`, `prev_cursor` and `as_of`. Cursors are opaque: pass one back with
`--cursor`, adding `--backward` to walk toward newer pairs, and pass `as_of`
back with `--as-of` so later pages leave out pairs captured since the first.
`--order-by` (`order_by=`) sorts by one of `requested_at`, `number`, `method`,
`host`, `path`, `status_code`, `content_type` or `length`, with an optional
`desc`; the default is `requested_at desc`. Pairs with no response sort last.
Every pair has a `number`, its position in capture order from 1. Numbers never
change and are never reused, and `-q 'number = 10'` finds one pair.
A malformed cursor, or one from another sort, returns 400
`{"error":"invalid_cursor","message":"..."}`.

These commands count, check, and change traffic in bulk, and manage background
jobs and client settings:

```sh
marasi --instance assessment traffic count -q 'status_code >= 500' --json
marasi --instance assessment traffic types --json
marasi --instance assessment traffic sitemap --node 'https://api.example.com' --json
marasi --instance assessment traffic validate -q 'host = "api.example.com"'
marasi --instance assessment traffic bulk highlight 3 --id UUID --id UUID
marasi --instance assessment traffic bulk note append 'checked' -q 'path = "/api/*"' --as-of UUID
marasi --instance assessment traffic bulk delete --ids-file ids.txt
marasi --instance assessment jobs list --json
marasi --instance assessment settings list my-client --json
```

`traffic bulk` commands (`highlight`, `link`, `unlink`, `metadata set`,
`metadata clear`, `note append`, `note clear`, `delete`) take exactly one
selection: `--id` (repeatable) or `--ids-file` (`-` for stdin), or `-q` with a
required `--as-of` and optional `--exclude`. They print the number of pairs
changed and do not ask for confirmation. `traffic validate` exits non-zero
with the message and position when a query, exclusion, or sort is invalid.
`settings` manages client settings: JSON values a client stores in the project
under its own name and a key.

`UUID` denotes an ID returned by `traffic list`. Raw request and response bytes
are base64-encoded in JSON output. Events are live notifications with no replay.
Traffic events can arrive before the matching row is persisted.

HTTPS inspection requires the client to trust Marasi's CA certificate. The
exported CA certificate contains no private key. All service instances that use
the same config directory share the CA.

### Marasi's own pages

A client that uses the proxy listener can reach Marasi's own pages at the host
`marasi`, over HTTP or HTTPS. Marasi answers these requests itself. They are
never captured, held by Checkpoint, or shown to extensions.

| URL | Answer |
| --- | --- |
| `http://marasi/cert` | The CA certificate in DER form, as `marasi-cert.der`. |
| `http://marasi/view/{uuid}` | A browser view of the pair with that UUID. |

A browser view shows a pair's stored response, unchanged, at the pair's
original URL, without contacting the origin. `http://marasi/view/{uuid}`
redirects to the original URL with `Cache-Control: no-store`. The next request
to that exact URL from the same client address, with any method, gets the
stored response, including its `Set-Cookie`, HSTS and cache headers. Only that
one request is served from storage. Everything the page then loads goes to the
live site and is captured as usual. A browser view expires if its redirect is
not followed within 10 seconds, and a newer browser view of the same URL from
the same client address replaces it. Launches and Armory runs never use one.

The URL takes the pair's UUID, not its number. Clients build it themselves;
there is no control API or CLI call to make first. Marasi answers in plain text
with 404 when the open project has no pair with that UUID, and with 409 when the
pair has no response yet or is a WebSocket upgrade.

### Storage and configuration

The default config directory is `Marasi` under `os.UserConfigDir()`:

| Platform | Default location |
| --- | --- |
| macOS | `~/Library/Application Support/Marasi` |
| Linux | `$XDG_CONFIG_HOME/Marasi`, or `~/.config/Marasi` when unset |
| Windows | `%AppData%\Marasi` |

Named projects live at `<config-dir>/projects/<name>.marasi`. Project files store
traffic, notes, metadata, proxy logs, extensions, waypoints, launchpads, Armory
templates and runs, test cases, findings, and artifacts.

The config directory also contains `marasi_config.yaml`, shared CA material,
Chrome configuration, `wordlists/`, and report `templates/`. Each service instance has
an `instances/<name>.sock` control socket and an `instances/<name>.log` process log.
Proxy logs are stored in the project, not in the process log.

`marasi_config.yaml` has an `updates` section:

```yaml
updates:
  channel: nightly   # release or nightly; unset follows the installed build's channel
  auto_check: false  # unset or true checks on start and every 6 hours
```

`marasi update channel` writes `updates.channel`. Instances share the last
check of each channel in `update_check.json`, guarded by `update_check.lock`,
so instances on one config directory call GitHub once per 6 hours. A
development build never checks for updates.

The control socket path, including its terminating byte, cannot exceed 104 bytes.
Windows also requires an absolute socket path. A short absolute `--config-dir`
avoids both limits.

## Library mode

The Go library uses `marasi.New` and option functions to configure the proxy.
Callback data types live in `domain`. The `service` package provides the HTTP
control API and its Unix-socket client.

```sh
go get github.com/tfkr-ae/marasi
```

- [Go package reference](https://pkg.go.dev/github.com/tfkr-ae/marasi) documents the library API.
- [CLI startup implementation](cmd/marasi/service.go) shows how the service connects the proxy and project resources.

### Project documentation

- [Glossary](GLOSSARY.md) defines the project's terms.
- [Architecture decisions](docs/adr/) record API and lifecycle contracts.
- [Live CLI verification guide](.agents/skills/verify-marasi/SKILL.md) covers verification with isolated service instances.
- [GitHub Discussions](https://github.com/tfkr-ae/marasi/discussions) hosts questions and feedback.
