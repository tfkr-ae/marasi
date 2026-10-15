# Background jobs

The open project runs background jobs that build the search indexes and the sitemap from captured traffic. Users watch their progress and restart one from the beginning.

## Sub-features

- List the jobs with `jobs list` (`GET /jobs`): `traffic_index`, `annotation_index`, `sitemap`, and `prettified_move`, each with `complete`, `done`, and `total`.
- Restart one with `jobs rebuild NAME` (`POST /jobs/{name}/rebuild`, 202).
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service and capture some traffic, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" jobs list`. Run `jobs rebuild sitemap` and list again.

## Driving it with shell and curl

Capture three pairs through the proxy. Retry `jobs list --json` until the `sitemap` item has `"complete":true`. Human `jobs list` prints headerless rows `name  complete|running  done/total`. Run `jobs rebuild sitemap` and require `job sitemap rebuild started` on stderr and a zero exit; with `--json` it prints the job object `{"name":"sitemap","complete":…,"done":…,"total":…}`. Retry `jobs list --json` until `sitemap` is complete again, then require `traffic sitemap` to list the origin with all three pairs. `jobs rebuild nonexistent` exits 1 with `rebuilding job: not_found`.

## Gotchas

- `jobs list` needs an open project; without one it is `listing jobs: not_found`.
- Rebuilding a job that cannot be rebuilt is `rebuilding job: not_rebuildable`.
- `jobs rebuild sitemap` publishes `sitemap.changed` with `{"reason":"rebuild"}`, because the tree is empty until the job refills it. Subscribe with `events` first; the stream has no replay.
- The rebuild returns once the job restarts, not when it finishes. Reads that depend on a job report it: `index.complete` and `sitemap.complete` in JSON, and a `notice:` line on stderr in human output.
- `jobs rebuild traffic_index` re-decodes bodies that were indexed before content-encoding decoding (see captured-traffic-inspection.md).
