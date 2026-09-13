# Service lifecycle

Marasi users start a named detached proxy service for a named project, inspect its identity and assigned proxy address, and stop it when finished.

## Sub-features

- Start an instance with `service start`.
- Inspect build, instance, project, and proxy-listener identity with `service status`.
- Stop the selected instance with `service stop`.
- Select isolated state with global `--config-dir` and `--instance` flags.
- Request machine-readable output with `--json`.

## How to get to it (user POV)

Build `dist/marasi`, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" service start --project "$VERIFY_PROJECT" --address 127.0.0.1 --port 0`. The command returns after the detached service is ready. Use the same global flags for status and stop.

## Driving it with shell and curl

Run `service start --json`, then `service status --json`. Match the returned `instance`, `project`, and `version` to the values supplied at launch. Require `proxy_listener` to contain `127.0.0.1` and an assigned decimal port. Run `service stop --json` and require its `instance` to equal `$VERIFY_INSTANCE` and its `status` to equal `stopped`. A second status call must fail with `instance $VERIFY_INSTANCE is not running`.

## Gotchas

- `service start` launches a detached child. The start command exiting does not mean the service stopped.
- Port `0` asks the OS for an unused proxy port and is the safe choice for parallel verification.
- The config directory contains the instance socket, log, project database, generated CA material, and wordlists. Use a new scratch directory under a short path such as `/tmp/mv.XXXXXX` because Marasi limits Unix-socket paths to 104 bytes.
- A project has an exclusive lock. Unique project names avoid colliding with another instance.
- A parent `go.work` can exclude this worktree. Build with `GOWORK=off`.
