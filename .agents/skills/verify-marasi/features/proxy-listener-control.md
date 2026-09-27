# Proxy listener control

Users can stop, start again, move, and inspect the TCP listener of a running service without closing its project or control socket. There is no `listener restart` command.

## Sub-features

- Read the listener state with `listener status`.
- Print the active address with `listener address`.
- Stop an active listener while keeping the service running.
- Start an inactive listener with `--address` and `--port`.
- Move an active listener with `listener update --address` and `--port`.
- Read the same assigned address from `service status`.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" listener status`. Replace `status` with `address`, `stop`, `start`, or `update` for the other operations. The instance's control API remains reachable while the proxy listener is inactive.

## Driving it with shell and curl

Use `listener status --json` and require `status` `active` plus an assigned `proxy_listener`. Run `listener address --json` and require the same `proxy_listener`. Run `listener stop --json`, require `status` `inactive` and `proxy_listener` `null`, and confirm `service status --json` still reports `running`. Run `listener start --address 127.0.0.1 --port 0 --json`, then `listener update --address 127.0.0.1 --port 0 --json`. Require a new non-empty address after each operation and use `curl --proxy` through the final address to prove it accepts proxy traffic.

## Gotchas

- `listener start` and `listener update` require both `--address` and `--port`. A partial flag set fails before the API. There is no retained last endpoint to fill in.
- Starting an already active listener fails. With `--json` the error is `starting proxy listener: listener_already_active`. Updating an inactive listener fails with `updating proxy listener: listener_inactive`. Human output includes `409 Conflict` and does not include those codes.
- Stopping an already inactive listener succeeds. `listener address` fails while inactive.
- Stop the service in cleanup even when the proxy listener is already inactive.
- Assigned port numbers are evidence values, not constants. Never reuse one in another run.
