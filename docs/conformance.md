# Conformance harness

The conformance harness measures what Marasi changes in traffic between a client and an origin. It records every case twice: directly (the direct baseline) and through Marasi. A case passes when both recordings match, apart from entries listed in the suite's `deviations.json`.

All suites require Docker with host networking.

## Make targets

| Command | Result |
| --- | --- |
| `make conformance` | Runs the required suites (`h1-origin`, `h1-client`, `h2-client`, `h2-origin`, `stream-origin`, `grpc-client`, `connect-client`) through Marasi and scores them against committed baselines and `deviations.json`. Exits non-zero on a new deviation, a known deviation that went away, or a stale entry. |
| `make conformance-baseline` | Runs the selected cases with no proxy and merges them into the direct baselines. For `h1-origin`, each case is recorded twice and omitted with an error if the two recordings differ. |
| `make conformance-update-known` | Runs the selected cases, then rewrites `known` entries in `deviations.json` and updates `score.json`. Never modifies `accepted` entries. |
| `make autobahn`, `make autobahn-baseline` | Shortcuts for `make conformance` and `make conformance-baseline` with `SUITES=autobahn`. |

## Suites

There are eight suites:

- **`autobahn`**: Runs the Autobahn WebSocket testsuite with Marasi on the host, in both `fuzzingclient` and `fuzzingserver` modes against Autobahn's strict testee and the harness's lenient echo peer.
- **`h1-origin`**: Sends HTTP/1.1 origin fixture cases (`internal/conformance/h1origin/catalogue.go`) using the harness's own client as absolute-form (`absolute`), plain HTTP inside `CONNECT` (`connect-plain`), and TLS inside `CONNECT` (`connect-tls`). Covers chunked bodies and trailers, `Content-Length` and close-delimited bodies, field name case/order/whitespace, `Connection`, `Keep-Alive`, transfer codings, 1xx, HEAD, 204/304, early closes, mid-body resets, persistent connections, and preserved request fields. Hop-by-hop fields and framing are excluded from baseline comparison and checked by leg rules instead: `leg:connection` (RFC 9110 §7.6.1), `leg:framing` (RFC 9112 §6), `leg:cl-and-te` (RFC 9112 §6.1), `leg:field-order` (RFC 9110 §5.3), and `leg:via` (RFC 9110 §7.6.3, accepted because Marasi does not announce itself). A broken MUST rule fails unless listed in `deviations.json`; a broken SHOULD rule warns. Marasi runs in a Linux container built by `make linux-<arch>` (`LINUX_ARCH`) on host networking, resolving `origin.test` to loopback.
- **`h1-client`**: Scores Marasi's HTTP/1.1 client leg with [Http11Probe](https://github.com/MDA2AV/Http11Probe) (built in Docker from a pinned commit) against nginx pinned by digest. A harness shim splices each connection directly or through Marasi (`connect-plain`, and adds TLS for `connect-tls`). Recordings hold each test verdict plus the shim's connection capture, with leg rules checked on Marasi's responses. Marasi runs in the same Linux container as `h1-origin`, resolving `nginx.test` to `127.0.0.2`. The first run builds the Http11Probe image (a few minutes).
- **`h2-client`**: Scores Marasi's HTTP/2 client leg with [h2spec](https://github.com/summerwind/h2spec) v2.6.0 (installed with `go install` at a pinned version) against nginx's TLS listener offering ALPN `h2`. h2spec performs its own TLS (`-t -k`) through the tunnelling shim (`connect-tls`). Because h2spec trusts any certificate, the harness first verifies one TLS handshake through the shim trusting only Marasi's CA (or the harness CA directly). Each recording captures h2spec's JUnit verdict (`not-run` if it could not start) and connection ending. Known entries pin `got`, so a different failure is flagged as new.
- **`h2-origin`**: Sends HTTP/2 origin fixture cases over `connect-tls` (offering ALPN `h2` then `http/1.1`). Records negotiated protocol, decoded header blocks with pseudo-headers, `DATA` bytes (length and SHA-256 above 4 KiB), trailers, `END_STREAM`, `RST_STREAM`, and `GOAWAY` while ignoring hop-by-hop framing details (`SETTINGS`, `WINDOW_UPDATE`, `PING`, `PRIORITY`, `DATA` splitting, padding, HPACK encoding, stream IDs). Covers field order, cookie crumbs, 103 Early Hints, mismatched `content-length`, `CONTINUATION`, bodies exceeding flow-control windows, concurrent streams, trailers, mid-body `RST_STREAM`, `GOAWAY`, gzip bodies, and redirection to an HTTP/1.1-only origin via Lua (`Setup.Lua`). Marasi runs in the same Linux container as `h1-origin`.
- **`stream-origin`**: Origin fixtures for streamed pairs (`internal/conformance/streamorigin/catalogue.go`). Client and origin follow scripted steps (head, stream message, trailers, wait) and record everything received in order, including a 5-second timeout when a buffering proxy stalls a wait-for-answer exchange. HTTP/2 cases use `connect-tls` with ALPN `h2`; HTTP/1.1 cases run on all three approaches. Runs in the same Linux container as `h1-origin`.
- **`grpc-client`**: Scores gRPC with grpc-go's interop client (`google.golang.org/grpc/interop/client`, installed at a pinned version) against grpc-java's interop server (`io.grpc:grpc-interop-testing` in a pinned Docker image) so quirks shared via `x/net/http2` do not cancel out; grpc-java's client runs the compressed cases. The client speaks h2c to the shim, which opens `connect-tls` (ALPN `h2`) trusting only Marasi's CA. Records client verdict (`timed-out` after 30s) plus decoded streams from the shim's plaintext capture (without `GOAWAY`). Client compressed cases fail directly too because grpc-java's server lacks `expect_compressed`. The first run builds the grpc-java image (1-2 minutes).
- **`connect-client`**: Runs [connectrpc/conformance](https://github.com/connectrpc/conformance) v1.0.7 (installed at a pinned version) in client mode covering gRPC, gRPC-Web, and Connect over HTTP/1.1 and h2c against the runner's reference servers. A wrapper (`connectclient/cmd/connectclient-wrapper`) rewrites host and port to a shim reaching the reference server directly or via `CONNECT` (`connect-plain`), answering stalled cases at 15s (`timed-out`) before the runner's 20s kill. TLS cases are skipped because the reference client trusts only its own certificate. Marasi runs on the host.

### TLS setup

Each run generates an ephemeral harness CA in memory without writing its key to disk. Fixtures serve certificates signed by it. Inside Marasi's container, `SSL_CERT_FILE` combines system roots with that CA so Marasi verifies fixtures unchanged. Direct clients trust the harness CA; proxied clients trust Marasi's CA fetched via `marasi certificate get`.

## Conformance runs and filtering

`make conformance` executes four conformance runs for each suite:

- `proxy`: plain proxying, no interception.
- `checkpoint-forward`: global Checkpoint with an auto-resolver forwarding each item unchanged.
- `checkpoint-edit`: global Checkpoint forwarding each item as an edit with the same bytes.
- `extension`: a workshop hook setting each payload or body to itself (`processWebSocketMessage`, `processRequest`/`processResponse`, `processStreamMessage`).

Make variables narrow a run:

```sh
make conformance SUITES=autobahn CASES='3.*,7.9.*' RUNS=proxy MODES=fuzzingclient PEERS=strict
make autobahn EXCLUDE='9.*,12.*,13.*' VERBOSE=1
make conformance SUITES=h1-origin APPROACHES=connect-plain
make conformance SUITES=h2-origin RUNS=proxy
make conformance SUITES=h2-client RUNS=proxy
```

Quick single-suite runs (matching individual CI jobs):

```sh
make conformance SUITES=h1-origin RUNS=proxy
make conformance SUITES=h1-client RUNS=proxy CASES='SMUG-CL-TE-BOTH@*'
make conformance SUITES=h2-origin RUNS=proxy
make conformance SUITES=stream-origin RUNS=proxy
make autobahn EXCLUDE='9.*,12.*,13.*'
```

For `h1-client`, naming exact probe IDs in `CASES` (`ID@*` for all approaches) runs only those probes; using a glob in the ID runs all 213 tests and filters afterwards.

Reports land in `dist/conformance/<suite>/<run>/` (`summary.md`, per-case JSON diffs under `diffs/`, Marasi's log, and tester output). The overall log is `dist/conformance/conformance.log`. In GitHub Actions, summaries are also appended to `$GITHUB_STEP_SUMMARY`. Passing `RUNS=direct,proxy` refreshes the direct baseline before comparing.

## Baselines and deviations

Committed suite files live in `internal/conformance/baselines/<suite>/`:

- One direct baseline JSON file per target (for example `fuzzingclient-strict.json`).
- `deviations.json`: entries matching case ID globs (`cases`), conformance runs (`runs`), recording field or leg-rule ID (`field`), kind (`accepted` or `known`), `reason`, and optional `targets`, `got`, and `rfc`.
  - `accepted`: intentional allow-listed proxy differences, edited only by hand.
  - `known`: temporary differences Marasi does not intend to keep.
- `score.json`: pass counts and leg-rule totals per run, target, and approach.

### Updating baselines and known entries

A run fails when:

- A **new** deviation appears that no entry in `deviations.json` covers.
- A **known** deviation **went away** (its cases ran and now match the baseline), ensuring scores only improve and `deviations.json` stays accurate.
- An entry is **stale** (matched nothing in a run and target it lists; narrowed runs with `CASES`, `EXCLUDE`, or `APPROACHES` check only entries covering cases that ran).

To update `known` entries and `score.json` from a run:

```sh
make conformance-update-known SUITES=h1-origin
```

This narrows or removes went-away `known` entries, adds `known` entries for new deviations, and rewrites `score.json` without touching `accepted` entries. Review the diff of `deviations.json` and `score.json` before committing.

Refresh direct baselines only when a fixture, pinned tester/image, or harness changes:

```sh
make conformance-baseline SUITES=<suite>
```

## CI workflow

`.github/workflows/conformance.yml` runs on pull requests, weekly on Mondays, and on manual dispatch (`ubuntu-24.04` runners, amd64 Marasi container on host networking where `127.0.0.0/8` is loopback). Each job appends its summary to the job summary and uploads `dist/conformance/` as an artifact.

On pull requests, the `paths` job inspects `git diff` against the base branch:

- **Required checks** (`h1-client`, `h1-origin`, `h2-client`, `h2-origin`, `stream-origin`, `grpc-client`, and `connect-client` across all four runs) run when a PR touches HTTP proxy paths (`*.go`, `go.mod`, `go.sum`, `Makefile`, `cmd/`, `core/`, `domain/`, `listener/`, `internal/h1/`, `rawhttp/`, `stream/`, `protoscope/`, `compass/`, `db/`, `extensions/`, `service/`), the shared harness, the workflow, or that suite's package and baselines. When untouched, the matrix jobs still report and skip their steps so required check names always report.
- **Autobahn** (`autobahn (<run>, <mode>)`) runs on changes to WebSocket paths (`websocket/`, `websocket*.go`), HTTP/1 upgrade and codec paths (`internal/h1/`, `h1engine*.go`, `upgrade.go`, `pair.go`, `options.go`), `db/`, `proxy.go`, `modifiers.go`, `checkpoint.go`, `extensions/`, the Autobahn suite, shared harness, or workflow (`db/` and `proxy.go` are included because WebSocket relays wait on the database writer queue). Artifacts include per-case reports, Marasi's log, and a resource log, with kernel OOM checks in the job log.

## Local Autobahn checks

When changing paths covered by Autobahn:

1. Before pushing, run the quick check (skips large-message `9.x` and compression `12.x`/`13.x`, ~7 minutes):

   ```sh
   make autobahn EXCLUDE='9.*,12.*,13.*'
   ```

2. Before merging, run the full suite (~1.5-2 hours on an arm64 Mac under amd64 emulation; the direct baseline takes ~20 minutes because strict peers negotiate permessage-deflate for `12.x`/`13.x`):

   ```sh
   make autobahn
   ```

Cases `6.4.1`-`6.4.4` are `accepted` entries in `internal/conformance/baselines/autobahn/deviations.json`.
