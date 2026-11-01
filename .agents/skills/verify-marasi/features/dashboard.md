# Dashboard counts

The open project's totals: pairs, notes, launchpads, and intercepted pairs. The project keeps them as pairs are written, so the read does not scan traffic ([Dashboard counts are kept as pairs are written](../../../../docs/adr/2026-10-16-dashboard-counts-are-kept-as-pairs-are-written.md)).

## Sub-features

- Read the counts with `dashboard` (`GET /dashboard`): `pairs`, `notes`, `launchpads`, `intercepted`, and `intercepted_count` with `complete`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service, capture some traffic, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" dashboard`.

## Driving it with shell and curl

On the fresh project, require `dashboard --json` to print `{"pairs":0,"notes":0,"launchpads":0,"intercepted":0,"intercepted_count":{"complete":true}}`. Capture three pairs through the proxy from a local origin. Pick one pair's `id` from `traffic list --json`, then run `echo '{"intercepted":true}' | traffic metadata update ID`, `echo note | notes set ID`, and `launchpad create --name dash`. Require `dashboard --json` to print `{"pairs":3,"notes":1,"launchpads":1,"intercepted":1,"intercepted_count":{"complete":true}}`. Human `dashboard` prints four aligned `name  count` lines in the order `pairs`, `notes`, `launchpads`, `intercepted`, with nothing on stderr.

## Gotchas

- `dashboard` needs an open project; without one it is `reading dashboard: not_found`.
- While the `intercepted_count` job is not complete, `intercepted` leaves out the pairs it has not reached, `intercepted_count.complete` is `false`, and human output adds `notice: the intercepted count is still building; it may be incomplete` on stderr. `jobs rebuild intercepted_count` restarts the job, but on a small project it completes before the next read, so the incomplete state is hard to catch live.
- `pairs` counts every captured pair, including the proxy's 502 for an origin it cannot reach.
