# Service lifecycle

Marasi users start a named detached proxy service for a named project, inspect its identity and assigned proxy address, and stop it when finished.

## Sub-features

- Start an instance with `service start`.
- Inspect version, instance, project, and proxy-listener identity with `service status`.
- Stop the selected instance with `service stop`.
- Select isolated state with global `--config-dir` and `--instance` flags.
- Choose the project with `--project-name` or `--project`.
- Request machine-readable output with `--json`.

## How to get to it (user POV)

Build `dist/marasi`, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" service start --project-name "$VERIFY_PROJECT" --address 127.0.0.1 --port 0`. The command returns after the detached service is ready. Use the same global flags for status and stop.

## Driving it with shell and curl

Run `service start --project-name "$VERIFY_PROJECT" --address 127.0.0.1 --port 0 --json`. Start JSON is only `instance` and `proxy_listener`. Then `service status --json`. Match `instance` to `$VERIFY_INSTANCE`, `status` to `running`, and `version` to the build `VERSION`. Require `project` to be the canonical absolute path of `$VERIFY_CONFIG_DIR/projects/$VERIFY_PROJECT.marasi`. Require `proxy_listener` to contain `127.0.0.1` and an assigned decimal port, and to equal the start value. Run `service stop --json` and require its `instance` to equal `$VERIFY_INSTANCE` and its `status` to equal `stopped`. A second `service status --json` must exit non-zero with `{"error":"instance $VERIFY_INSTANCE is not running"}`.

## Gotchas

- `service start` launches a detached child. The start command exiting does not mean the service stopped.
- Port `0` asks the OS for an unused proxy port and is the safe choice for parallel verification.
- `--project-name` selects `$configDir/projects/<name>.marasi`. `--project` is a file path that must end in `.marasi`. The flags are mutually exclusive.
- Omitting both project flags opens `$configDir/projects/scratchpad.marasi`.
- Status `project` is that resolved absolute path, not the name passed to `--project-name`. On macOS `/tmp` canonicalizes through `/private/tmp`.
- The config directory contains the instance socket, log, project database, generated CA material, and wordlists. Use a new scratch directory under a short path such as `/tmp/mv.XXXXXX`. Marasi rejects the socket path when its byte length plus one exceeds 104.
- A project has an exclusive lock. Unique project names avoid colliding with another instance.
- A parent `go.work` can exclude this worktree. Build with `GOWORK=off`.
