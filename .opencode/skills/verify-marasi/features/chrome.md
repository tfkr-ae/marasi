# Chrome

Users register Chrome executable paths and named profiles on a running instance, then start a browser pointed at the active proxy listener.

## Sub-features

- Add a Chrome executable path with `chrome path add --path`.
- Remove a path with `chrome path remove --path`.
- List paths with `chrome path list`.
- Add a named profile with `chrome profile add`.
- Remove a profile with `chrome profile remove`.
- List profiles with `chrome profile list`.
- Start Chrome with `chrome start`, optionally `--profile`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" chrome path add --path /path/to/chrome`. Add a profile with `chrome profile add pentest`. Start with `chrome start --profile pentest`.

## Driving it with shell and curl

Point `--path` at an executable stub when you need start proof without a GUI Chrome. Run `chrome path add --path "$STUB" --json` and require that path in `items`. Run `chrome path list --json` and require the same item. Duplicate add of that path must fail. Run `chrome profile add pentest --json` and require `pentest` in `items`. Run `chrome profile list --json` and require that name. Run `chrome start --profile pentest --json` and require `{"status":"started","profile":"pentest"}`. Confirm the stub received `--proxy-server=http://$PROXY_LISTENER` and `--user-data-dir` under `$VERIFY_CONFIG_DIR/chrome_profiles/pentest`. Run `chrome start --json` with no `--profile` and require `default-profile`. Run `chrome path remove --path "$STUB" --json` and `chrome profile remove pentest --json`.

## Gotchas

- `--path` is required on path add/remove. `--os` defaults to this machine and must be `darwin`, `linux`, or `windows`.
- Duplicate path or profile returns a conflict. Removing a missing path or profile returns not found.
- `chrome start` with no `--profile` uses `default-profile` even if that name is not registered. Any other `--profile` must already exist.
- Start requires an active proxy listener. An inactive listener returns a conflict.
- Custom paths are tried before OS defaults. A stub at `--path` replaces the Chrome binary the same way a local origin replaces an external website.
- Start launches a detached process and returns `{"status":"started","profile":...}` without waiting for the process to write flags or load a page. Retry until the stub args file or profile directory appears. It does not publish an events frame.
- Launched Chrome sets `--proxy-bypass-list=<-loopback>`, so localhost is not sent through Marasi.
- Path and profile mutations publish `chrome.path.added`, `chrome.path.removed`, `chrome.profile.added`, and `chrome.profile.removed`.
- Kill any process started from this run's `chrome_profiles` directory during cleanup.
