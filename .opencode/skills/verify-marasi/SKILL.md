---
name: verify-marasi
description: Verify Marasi's CLI-driven proxy service when a change needs proof through a real launched instance, a proxied HTTP request, persisted traffic inspection, event subscription, launchpad replay, test-case control, finding control, artifact control, Checkpoint control, Chrome control, waypoint control, wordlist control, Armory template and run control, or extension control.
---

# Verify Marasi

Marasi's primary user surface is the `marasi` CLI. It launches a detached HTTP/HTTPS proxy service and talks to its control API through a Unix socket. The HTTP control API and Go library are secondary surfaces.

Use a fresh config directory, instance name, project name, and port `0` for every run. This is what makes parallel runs safe. Never point verification at the default config directory or an instance you did not start.

## Launch

Run from the repository root. The parent checkout may contain a `go.work` that excludes this worktree, so keep `GOWORK=off` on Go and Make commands.

```bash
export VERIFY_RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
export VERIFY_CONFIG_DIR="$(mktemp -d /tmp/mv.XXXXXX)"
export VERIFY_INSTANCE="v$$"
export VERIFY_PROJECT="verify-${VERIFY_RUN_ID}"

GOWORK=off make build VERSION="verify-${VERIFY_RUN_ID}"
./dist/marasi \
  --config-dir "$VERIFY_CONFIG_DIR" \
  --instance "$VERIFY_INSTANCE" \
  service start \
  --project-name "$VERIFY_PROJECT" \
  --address 127.0.0.1 \
  --port 0 \
  --json
```

The start command exits after its detached child is ready. Readiness is a zero exit code and JSON containing the selected instance plus a non-empty `proxy_listener`, for example `{"instance":"verify-...","proxy_listener":"127.0.0.1:54321"}`.

Verification needs no authentication, seed data, browser, or environment variables beyond those shown above. Marasi creates the project database, CA material, and an empty `wordlists` directory under the scratch config directory. Armory runs need files in that directory. `wordlist add` moves a source file there; writing the file yourself also works.

Teardown only the selected instance:

```bash
./dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" service stop --json
```

## Doctor

Run this read-only check first whenever output, connectivity, or state looks wrong:

```bash
./dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" service status --json
```

Drive the instance only when the command exits zero and reports all of the following:

- `status` is `running`.
- `version` is `verify-$VERIFY_RUN_ID`.
- `instance` equals `$VERIFY_INSTANCE`.
- `project` is the canonical absolute path of the file `$VERIFY_CONFIG_DIR/projects/$VERIFY_PROJECT.marasi`. On macOS that path is under `/private/tmp` even when the config directory was created as `/tmp/mv.XXXXXX`.
- `proxy_listener` is `127.0.0.1` with a non-empty assigned decimal port.

This status comes from the service reached through this run's private Unix socket. The unique config directory and instance name identify the process as ours. If any identity field differs, stop. Do not drive or stop that instance.

## Drive

Use shell commands for the CLI and `curl` for the proxy. Prefer `--json` because the field names are the CLI's stable handles. Use these commands to capture a real HTTP request through the assigned proxy:

1. Get `proxy_listener` from `service status --json`.
2. Start a local HTTP origin on `127.0.0.1` with a port assigned by the OS.
3. Subscribe with `events` and wait for `: connected` on stderr. The stream has no replay.
4. Send `curl --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$ORIGIN_PORT/proof.txt"`.
5. Require `events` stdout to print `traffic.request` then `traffic.response` for that exchange.
6. Find the request with `traffic list --path /proof.txt --status-code 200 --json`.
7. Pass its `id` to `traffic get "$TRAFFIC_ID" --json`. The event `id` fields must match.

For the complete recipe, run the bundled executable helper:

```bash
.opencode/skills/verify-marasi/scripts/verify-traffic.sh
```

It builds Marasi, starts an isolated instance and local origin, checks service identity, subscribes with `events`, proxies one request, inspects the live events and the stored request/response pair through the CLI, checks the SQLite project side effect, writes evidence, and cleans up.

Read `features/README.md` before choosing coverage. A proof is incomplete if the mapped feature has another user entry point that the run ignores.

## Evidence

The helper writes each proof to:

```text
.opencode/verification-artifacts/verify-marasi/$RUN_ID/
```

Keep at least `actions.log`, `launch.json`, `doctor.json`, `events-stderr.txt`, `events-stdout.txt`, `response-headers.txt`, `response-body.txt`, `traffic-list.json`, `traffic-detail.json`, `database-state.txt`, `service.log`, `cleanup.json`, and `result.txt`. `result.txt` is valid only when it says `PASS`.

A valid proof exercises the real CLI, detached service, proxy listener, and a normal HTTP origin. It captures the request action and the returned body, then confirms the same request through `events`, `traffic list`, `traffic get`, and the persisted SQLite `request` row. Internal setters and test-only endpoints do not count. Keep both the action and resulting state. Mocks are acceptable only at an existing production boundary. The local origin used here replaces an external website, not Marasi internals.

Marasi has no dry-run mode in this path. The helper observes the project database and request row rather than inferring safety from a mode name.

## Cleanup

Interrupt the `events` subscriber first, stop the exact instance created by the run, then terminate the recorded local-origin PID and remove only that run's scratch config directory. Never kill by process name.

```bash
kill -INT "$EVENTS_PID"
wait "$EVENTS_PID" 2>/dev/null || true
./dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" service stop --json
kill "$ORIGIN_PID"
wait "$ORIGIN_PID" 2>/dev/null || true
rm -rf "$VERIFY_CONFIG_DIR"
```

The evidence directory is separate from `$VERIFY_CONFIG_DIR`; cleanup must leave it intact. Run cleanup after failed attempts too. If the environment variables no longer identify a run you created, inspect rather than guessing.

## Helpers

`scripts/verify-traffic.sh` is executable and has no third-party dependencies beyond the repo's Go toolchain, Bash, `curl`, and Python 3. Invoke it from any directory inside the checkout:

```bash
.opencode/skills/verify-marasi/scripts/verify-traffic.sh
```

Set `VERIFY_ARTIFACT_ROOT` to redirect evidence without changing scratch-state isolation:

```bash
VERIFY_ARTIFACT_ROOT="$PWD/my-proof" .opencode/skills/verify-marasi/scripts/verify-traffic.sh
```
