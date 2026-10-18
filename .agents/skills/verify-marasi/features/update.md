# Update

Users see whether a newer build is on their channel, switch channel, and install the update through a running service instance, then restart instances onto it.

## Sub-features

- Show the installed build, channel, automatic checks, last check, available update with its notes, install state, restart required, and install path with `update`.
- Check the channel now with `update check`.
- Print or switch the channel with `update channel [release|nightly]`.
- Install the update and follow its progress with `update install`.
- Install the latest build on the channel, even when older, with `update reinstall --yes`.
- See `restart required` in `service status` and `service list` after an install.
- Request machine-readable output with `--json`; `update install` and `update reinstall` stream NDJSON.

## How to get to it (user POV)

Start a service first (see [service lifecycle](service-lifecycle.md)). Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" update`. The build `make build` produces is a development build (a `(dev)` version, or no embedded signing key), so it reports `development: true` and refuses to check or install. Proving a check or an install needs a downloaded signed release or nightly build and network access to GitHub.

## Driving it with shell and curl

With a development build: `update --json` returns the `GET /update` body with `"development":true`, `"channel":null`, `"last_check":null`, and `"available":null`. Human `update` prints `installed: <version> (development build)`, `channel: none`, and `auto check: off (development build)`. `update check --json` and `update install --json` exit 1 with `{"error":"... development_build: a development build does not check for or install updates"}`. `update reinstall` without `--yes` exits 1 with the reinstall warning and sends no request. `update channel beta` exits 1 before any request.

With a release build older than the latest release: `update check --json` sets `last_check.at` and `available.version`. Start `update install --json` and require NDJSON `{"event":"update.progress","data":{...,"step":"download"...}}` lines, then `verify`, then `install`, then one `update.installed` line whose `install_path` equals `GET /update`'s `install_path`, and exit 0. `service status --json` then has `"restart_required":true`, and human status prints `restart required: yes`. `service restart` moves the instance onto the new build, and `restart_required` is `false` again. Do this only against a throwaway copy of the build, since the install replaces that executable.

With no instance running, every `update` command exits 1 with `instance $VERIFY_INSTANCE is not running; start one with marasi service start --instance $VERIFY_INSTANCE` (no `--instance` suffix for `default`).

## Gotchas

- The instance does the work, not the CLI (ADR-0050). The executable it replaces is its own, with symlinks resolved, which can differ from the `marasi` on PATH.
- `update install` subscribes to events before it posts, so it misses no progress. Human mode writes `installing <version> to <path>`, one line per step and per tenth of the download, and the result to stderr; stdout stays empty. `--json` writes only `update.progress`, `update.installed`, and `update.failed` events for that version.
- Ctrl-C stops following and exits 0. The install continues on the instance. Human mode prints `stopped following; the install of <version> continues on instance <name>, run marasi update to see it`. Cancel it through `DELETE /update/install` before the install step.
- A second `update install` on the same instance joins the running install (`following the install of ... already running on instance ...`). One on another instance of the same build gets 409 `update_in_progress` naming the holder.
- `update.failed` exits 1. Human mode prints `<verb> <version> failed at <step> (<kind>): <message>`, and the release page when there is one. Under `--json` the failed event line is the last output; no `{"error":...}` follows it.
- `marasi update` shows at most 5 lines of notes, then `... N more lines on the page`. `--json` has the full notes.
- Switching from nightly to release keeps the nightly until a newer release exists (ADR-0049). Human `update channel release` says so on stderr. `update reinstall --yes` installs the latest release at once.
- `update channel` writes `updates.channel` in `$VERIFY_CONFIG_DIR/marasi_config.yaml`. `updates.auto_check: false` stops the checks on start and every 6 hours. The last check of each channel is shared through `update_check.json` in the config directory.
- `service status` omits `restart required` for instances from builds older than updates.
