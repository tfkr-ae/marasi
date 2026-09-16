# Proxy listener control

Users can stop, restart, move, and inspect the TCP listener of a running service without closing its project or control socket.

## Sub-features

- Read the listener state with `listener status`.
- Print the active address with `listener address`.
- Stop an active listener while keeping the service running.
- Start an inactive listener, optionally overriding `--address` or `--port`.
- Move an active listener with `listener update --address` or `--port`.
- Read the same assigned address from `service status`.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" listener status`. Replace `status` with `address`, `stop`, `start`, or `update` for the other operations. The instance's control API remains reachable while the proxy listener is inactive.

## Driving it with shell and curl

Use `listener status --json` and require `status` `active` plus an assigned `proxy_listener`. Run `listener address --json` and require the same `proxy_listener`. Run `listener stop --json`, require `status` `inactive` and `proxy_listener` `null`, and confirm `service status --json` still reports `running`. Run `listener start --port 0 --json`, then `listener update --port 0 --json`. Require a new non-empty address after each operation and use `curl --proxy` through the final address to prove it accepts proxy traffic.

## Gotchas

- `listener update` requires at least one of `--address` or `--port`.
- Starting an already active listener returns a conflict. Updating an inactive listener also returns a conflict.
- Stopping an already inactive listener succeeds. `listener address` fails while inactive.
- Stop the service in cleanup even when the proxy listener is already inactive.
- Assigned port numbers are evidence values, not constants. Never reuse one in another run.
