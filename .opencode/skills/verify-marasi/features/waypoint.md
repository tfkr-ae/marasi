# Waypoint

Users map an original `host:port` to an override `host:port` in the open project so proxied requests to the original destination are forwarded to the override instead.

## Sub-features

- List waypoints in hostname order with `waypoint list`.
- Add a mapping with `waypoint add --hostname` and `--override`.
- Change an existing mapping with `waypoint update --hostname` and `--override`.
- Remove a mapping with `waypoint remove --hostname`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" waypoint add --hostname 127.0.0.1:$ORIGIN_A_PORT --override 127.0.0.1:$ORIGIN_B_PORT`. List, update, and remove use the same global flags.

## Driving it with shell and curl

Run `waypoint list --json` and require `{"items":[]}`. Run `waypoint add --hostname "127.0.0.1:$ORIGIN_A_PORT" --override "127.0.0.1:$ORIGIN_B_PORT" --json` and require that pair in `items`. Proxy `curl --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$ORIGIN_A_PORT/proof.txt"` and require the origin-B body. Duplicate add of that hostname must fail. Run `waypoint update --hostname "127.0.0.1:$ORIGIN_A_PORT" --override "127.0.0.1:$ORIGIN_A_PORT" --json`, then the same curl, and require the origin-A body. Run `waypoint remove --hostname "127.0.0.1:$ORIGIN_A_PORT" --json` and require `{"items":[]}`.

## Gotchas

- `--hostname` and `--override` are required on add and update. `--hostname` is required on remove. Both values must be `host:port`.
- Duplicate add of the same hostname fails. With `--json` the error is `adding waypoint: waypoint_already_exists`. Update or remove of a missing hostname fails with `updating waypoint: not_found` or `removing waypoint: not_found`. Human output includes the HTTP status and does not include those codes.
- JSON add, update, and remove return the full list. Human add, update, and remove confirm on stderr. List is ordered by hostname, not insertion time.
- Add and remove publish `waypoint.added` and `waypoint.removed`. Update publishes `waypoint.updated` only when the override changes. An unchanged override returns the list and publishes nothing. Subscribe with `events` before the mutation; the stream has no replay.
- Waypoints belong to the currently open project. After opening a different project, previous hostnames are not visible and no longer override traffic. Opening the current path does not hide them.
