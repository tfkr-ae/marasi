# Service lifecycle

Marasi users start a named detached proxy service for a named project, inspect its identity and assigned proxy address, and stop it when finished.

## Sub-features

- Start an instance with `service start`.
- Inspect version, instance, project, proxy-listener identity, and whether a restart is required with `service status`.
- Stop the selected instance with `service stop`.
- Restart the selected instance on the installed build with `service restart [--force]`.
- List instances in the config directory with `service list`.
- Select isolated state with global `--config-dir` and `--instance` flags.
- Choose the project with `--project-name` or `--project`.
- Request machine-readable output with `--json`.

## How to get to it (user POV)

Build `dist/marasi`, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" service start --project-name "$VERIFY_PROJECT" --address 127.0.0.1 --port 0`. The command returns after the detached service is ready. Use the same global flags for status and stop.

## Driving it with shell and curl

Run `service start --project-name "$VERIFY_PROJECT" --address 127.0.0.1 --port 0 --json`. Start JSON is only `instance` and `proxy_listener`. Then `service status --json`. Match `instance` to `$VERIFY_INSTANCE`, `status` to `running`, and `version` to the build `VERSION`. Require `project` to be the canonical absolute path of `$VERIFY_CONFIG_DIR/projects/$VERIFY_PROJECT.marasi`. Require `proxy_listener` to contain `127.0.0.1` and an assigned decimal port, and to equal the start value. Run `service list --json` and require one item whose `instance` equals `$VERIFY_INSTANCE` and whose `status` is `running`. Run `service restart --json` and require `{"instance":"$VERIFY_INSTANCE","status":"restarted","version":...,"proxy_listener":...}` with the same `proxy_listener`; `service status --json` afterwards reports the same instance, project, and listener. Run `service stop --json` and require its `instance` to equal `$VERIFY_INSTANCE` and its `status` to equal `stopped`. A second `service status --json` must exit non-zero with `{"error":"instance $VERIFY_INSTANCE is not running"}`.

## Gotchas

- `service start` launches a detached child. The start command exiting does not mean the service stopped.
- Port `0` asks the OS for an unused proxy port and is the safe choice for parallel verification.
- `--project-name` selects `$configDir/projects/<name>.marasi`. The name is trimmed, and one trailing `.marasi` suffix is stripped. `--project` is a file path that must end in `.marasi`. The flags are mutually exclusive.
- Omitting both project flags opens `$configDir/projects/scratchpad.marasi`.
- Status `project` is that resolved absolute path, not the name passed to `--project-name`. On macOS `/tmp` canonicalizes through `/private/tmp`.
- The config directory contains the instance socket, log, project database, generated CA material, and wordlists. Use the short absolute scratch directory from Launch. Marasi rejects the socket path when its byte length plus one exceeds 104.
- A project has an exclusive lock. Unique project names avoid colliding with another instance.
- A parent `go.work` can exclude this worktree. Build with `GOWORK=off`.
- `service list --json` is `{"items":[...]}`. A running item is that instance's status body. Any other socket that is not a dial failure is `{"status":"unhealthy","instance":"<name>"}`, including a readable error or garbage JSON. Dial failures are omitted. List never starts or stops instances.
- `service restart` starts the successor from the instance's own executable, symlinks resolved, so it picks up an installed update. While items are held the route returns 409 `checkpoint_items_held` with an `items` list, and the CLI exits 1: human mode prints `restarting service: N Checkpoint items are held:` and one `id type summary` line per item on stderr, and `--json` prints only `{"error":"restarting service: checkpoint_items_held: N held Checkpoint items would be dropped; forward or drop them, or force the restart"}`, without the items. `--force` drops them. A successor that fails or is not ready within 30 s leaves the old instance serving, and the error and `service.restart_failed` event name the successor log, `$VERIFY_CONFIG_DIR/instances/$VERIFY_INSTANCE.log`. Human mode prints a warning on stderr first.
- A restart does not wait for proxied requests in flight. `service restart` returns `restarted` while a request to an origin that has not answered is still open, and the successor serves the listener at once. The old process stays alive, so `pgrep -f -- "--config-dir $VERIFY_CONFIG_DIR "` shows both until that request ends. When the origin answers, the client still gets the response, but the pair keeps `responded_at` null in the project. The next request on that kept-alive connection is not served: the connection closes without a response, and the request never reaches the origin. The old process then exits. Prove it with a raw socket: send an absolute-form GET for a path the origin holds, run `service restart --json`, release the origin, read the response, send a second GET on the same socket and require EOF, then require `pgrep` to show only the successor and the doctor to pass.
