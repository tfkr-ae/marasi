---
name: verify-marasi
description: Verify Marasi's CLI-driven proxy service when a change needs proof through a real launched instance, service instance listing, a proxied HTTP request, persisted traffic inspection, traffic metadata and note control, event subscription, launchpad replay, test-case control, finding control, artifact control, Checkpoint control, Chrome control, waypoint control, wordlist control, Armory template and run control, extension control, report template and export control, proxy log inspection, CA certificate fetch, project listing, WebSocket connection control, or scope check.
---

# Verify Marasi

Marasi's primary user surface is the `marasi` CLI. It launches a detached HTTP/HTTPS proxy and talks to that process over a Unix-domain socket on every OS, including Windows. The HTTP control API and Go library are secondary. Drive with the `marasi` commands in this file and in `features/`. Do not wrap them.

## Isolate

Every run gets its own config directory, instance name, project name, and proxy port `0`. Parallel runs are safe only when those four differ. Never use the default config directory or an instance you did not start.

The default config directory is `os.UserConfigDir()/Marasi`: `$XDG_CONFIG_HOME/Marasi` or `$HOME/.config/Marasi` on Linux, `~/Library/Application Support/Marasi` on macOS, `%AppData%\Marasi` on Windows.

The control socket is `<config-dir>/instances/<instance>.sock`. Marasi rejects that path when its byte length plus one exceeds 104. Use a short absolute config directory. A relative `--config-dir` can work on Linux and fail on Windows, because Windows AF_UNIX requires an absolute path. A long Windows temp path trips the 104-byte check sooner than `/tmp`.

`--project-name` selects `$configDir/projects/<name>.marasi`. A project has an exclusive lock. Unique project names avoid colliding with another instance.

A parent `go.work` can exclude this worktree. Build with `GOWORK=off`.

## Launch

Run from the repository root.

Linux and macOS:

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

`make build` writes `dist/marasi` with no `.exe`. `make windows-amd64` writes `dist/marasi-windows-amd64.exe`. On Windows, invoke a binary built on that machine or copied in from another machine. Building there is not required. The subcommands do not change. Doctor still requires `version` to be `verify-$VERIFY_RUN_ID`, so the binary you invoke must have been built with that `VERSION`.

Windows scratch directory: create a short absolute directory yourself, for example under `C:\mv`, and set `VERIFY_CONFIG_DIR`, `VERIFY_INSTANCE`, and `VERIFY_PROJECT` to unique values. Do not use `/tmp` or a path that makes the socket longer than 104 bytes. `%TEMP%` is often too long.

The start command exits after its detached child is ready. Readiness is a zero exit code and JSON `{"instance":"<VERIFY_INSTANCE>","proxy_listener":"127.0.0.1:<port>"}`. `instance` is the `--instance` flag, not the version and not the project name. The port is the OS-assigned port, not `0`.

Unix detach is a new session. Windows detach is a new process group with `DETACHED_PROCESS`. Both wait on the same stdin handshake. A Windows detached child does not receive console Ctrl+C. Stop it with `service stop`.

Verification needs no authentication, seed data, or browser. Marasi creates the project database, CA material, an empty `wordlists` directory, and `templates/default_template.md` under the scratch config directory.

## Doctor

```bash
./dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" service status --json
```

Drive the instance only when the command exits zero and reports all of the following:

- `status` is `running`.
- `version` is `verify-$VERIFY_RUN_ID`.
- `instance` equals `$VERIFY_INSTANCE`.
- `project` is the canonical absolute path of `$VERIFY_CONFIG_DIR/projects/$VERIFY_PROJECT.marasi`. On macOS `/tmp` becomes `/private/tmp`. Linux `/tmp` usually stays `/tmp`. Windows canonicalization can change junctions and uses backslashes. Compare with the platform real path, not the string passed to `--config-dir`.
- `proxy_listener` is `127.0.0.1` with a non-empty assigned decimal port.

This status comes from the service reached through this run's private socket. If any identity field differs, stop. Do not drive or stop that instance.

After `project open` to a different project, `project` no longer matches `$VERIFY_PROJECT`. The instance is still ours when `instance` and `version` match. Doctor against the opened project path from that point.

## Drive

Use the same `--config-dir` and `--instance` on every command. Prefer `--json`. Use `curl` for proxy traffic. Read `features/README.md` and the feature file before choosing coverage. A proof is incomplete if that file has another user entry point the run ignores.

Commands that inspect, list, filter, replay, or otherwise act on captured traffic are not proven against an empty project. Send real client traffic through `proxy_listener`, then run the `marasi` command and assert on the traffic or state that command changes.

Capture one HTTP exchange:

1. Read `proxy_listener` from `service status --json`.
2. Start a local HTTP origin on `127.0.0.1` with a port assigned by the OS.
3. Run `events` and wait for `: connected` on stderr before the request. The stream has no replay. Human stdout is `event-name json`.
4. `curl --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$ORIGIN_PORT/proof.txt"`. `--noproxy ''` is required. `NO_PROXY` often bypasses localhost.
5. Require events stdout to print `traffic.request` then `traffic.response` for that exchange, with the same `id`.
6. `traffic list -q 'path = "/proof.txt" AND status_code = 200' --json`, then `traffic get` that `id`. The event `id` must match. `request.raw` and `response.raw` are base64.

WebSocket features need a real local WebSocket server and a client that holds the upgraded connection through the proxy. Checkpoint, launchpad, Armory, and waypoints are proven only when the origin or the stored traffic changes. Each feature file is the recipe.

Stop the `events` subscriber you started before cleanup. After `: connected`, SIGINT is a zero exit on Unix, including when the job inherited SIGINT ignored. See `features/events.md`. Windows cannot deliver that signal to another process. Foreground Ctrl-C is the equivalent.

## Evidence

Write the run's proof to:

```text
.agents/verification-artifacts/verify-marasi/$VERIFY_RUN_ID/
```

Keep the launch JSON, doctor JSON, the curl response, events stdout and stderr, `traffic list`, and `traffic get` for a capture proof. Keep the command output you asserted on for every other feature. `result.txt` is valid only when it says `PASS`.

A valid proof exercises the real CLI, the detached service, the proxy listener, and a normal origin or WebSocket peer. Internal setters and test-only endpoints do not count. A local origin or WebSocket peer replaces an external website, not Marasi internals.

## Cleanup

Stop only the instance this run started. Never kill by process name.

```bash
./dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" service stop --json
```

Interrupt the `events` subscriber first if it is still attached. Then stop the instance. Then terminate the recorded PIDs for the local origin, WebSocket peer, and client this run started, and remove only `$VERIFY_CONFIG_DIR`. On Unix that is `kill` of those PIDs. On Windows, stop those recorded PIDs. Do not match a process name.

`service stop` removes the socket, not the log. On Windows, removing the config directory can fail while the child still holds the log. Wait until `service status` reports the instance is not running, then remove the directory.

The evidence directory is separate from `$VERIFY_CONFIG_DIR`. Cleanup leaves it intact. Run cleanup after failed attempts too. If the variables no longer identify a run you created, inspect rather than guessing.
