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
- Start Chrome with a CDP endpoint with `chrome start --cdp`. Running it again on a chrome profile with a live CDP endpoint reuses that endpoint.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" chrome path add --path /path/to/chrome`. Add a profile with `chrome profile add pentest`. Start with `chrome start --profile pentest`. Add `--cdp` to get a CDP endpoint for Playwright, Puppeteer, or a raw CDP client.

## Driving it with shell and curl

Point `--path` at an executable stub when you need start proof without a GUI Chrome. Run `chrome path add --path "$STUB" --json` and require an item whose `path` is that stub and whose `os` is this machine. Run `chrome path list --json` and require the same item. Duplicate add of that path must fail. Run `chrome profile add pentest --json` and require an item whose `name` is `pentest`. Run `chrome profile list --json` and require that name. Run `chrome start --profile pentest --json` and require `{"status":"started","profile":"pentest"}`. Confirm the stub received `--proxy-server=http://$PROXY_LISTENER`, `--user-data-dir` under `$VERIFY_CONFIG_DIR/chrome_profiles/pentest`, and `--proxy-bypass-list=<-loopback>`. Use the same `--config-dir` string passed to `service start`, not its real path. Run `chrome start --json` with no `--profile` and require `default-profile`. Run `chrome path remove --path "$STUB" --json` and `chrome profile remove pentest --json`.

Quiet switches: in the recorded argv require exactly one `--disable-features=` whose comma list holds `NetworkPrediction`, `OmniboxUIExperimentMaxAutocompleteMatches`, `AimEnabled`, `OptimizationHints`, `NetworkTimeServiceQuerying`, `Translate`, `MediaRouter`, `DialMediaRouteProvider`, `AutofillServerCommunication`, `CertificateTransparencyComponentUpdater`, and `InterestFeedContentSuggestions`. Require `--disable-component-extensions-with-background-pages`, `--no-default-browser-check`, `--disable-breakpad`, and `--disable-search-engine-choice-screen`. Require that the exact `--disable-search-engine-choice`, `--disable-suggestions-service`, `--disable-search-geolocation-disclosure`, `--disable-omnibox-autocomplete-offers`, `--no-service-autorun`, `--disable-client-side-phishing-detection`, any `--remote-debugging-port`, and any `--host-resolver-rules` are absent.

CDP with a stub: run a local HTTP server on `127.0.0.1` with a port assigned by the OS. It answers `GET /json/version` with `{"webSocketDebuggerUrl":"ws://127.0.0.1:<port>/devtools/browser/<id>"}`. Register a stub that records argv and, only when argv holds `--remote-debugging-port=0`, creates its `--user-data-dir` and writes `<user-data-dir>/DevToolsActivePort` with line 1 `<port>` and line 2 `/devtools/browser/<id>`. The stub runs in the service's environment, not your shell, so pass the port and id through files, not variables. Run `chrome start --profile c --cdp --json` and require exit zero and `{"status":"started","profile":"c","cdp":{"http":"http://127.0.0.1:<port>","ws":"ws://127.0.0.1:<port>/devtools/browser/<id>","reused":false}}`, with `--remote-debugging-port=0` in that argv. Run it again and require the same `http` and `ws`, `reused: true`, and no `--remote-debugging-port` in the second argv. Without `--json`, require stderr `chrome started with profile c` then `CDP endpoint http://127.0.0.1:<port>`, with ` (reused)` appended on reuse, and empty stdout. Run plain `chrome start --profile c --json` while the endpoint is live and require no `cdp` key. Replace the stub with one that writes nothing, run `chrome start --profile n --cdp --json` on a new profile, and require exit 1 and stdout `{"error":"starting chrome: chrome_running_without_cdp: chrome profile n is running without a CDP endpoint; close it and start it again with --cdp"}`. Human mode prints the same message on stderr after `Error: `.

CDP with real Chrome: register `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` (or this OS's binary) as a path, add a fresh profile, and run `chrome start --profile <p> --cdp --json`. Require `curl --noproxy '*' "<cdp.http>/json/version"` to succeed with `webSocketDebuggerUrl` equal to `cdp.ws`. Run `start --cdp` again and require the same endpoint with `reused: true`. Leave it idle about 45 seconds, then `traffic list --json`: `www.google.com/async/folae` and `clients2.google.com/time` must not appear. Chrome 155 on macOS recorded only `https://www.google.com/search/warmup.html`.

## Gotchas

- `--path` is required on path add/remove. `--os` defaults to this machine and must be `darwin`, `linux`, or `windows`.
- Duplicate path or profile returns a conflict. Removing a missing path or profile returns not found.
- `chrome profile remove` also deletes `$VERIFY_CONFIG_DIR/chrome_profiles/<name>` and everything under it.
- `chrome start` with no `--profile` uses `default-profile` even if that name is not registered. Any other `--profile` must already exist.
- Start requires an active proxy listener. An inactive listener returns a conflict.
- Custom paths for this OS are tried before OS defaults. The first path `LookPath` accepts is the binary. Later OS paths are not tried. A non-executable file is skipped. If nothing is accepted, start fails with `chrome_unavailable` and launches nothing. A file `LookPath` accepts that is not a valid executable is selected, `Start` fails, and OS defaults are not tried. On darwin and linux a mode-`0755` shebang stub is accepted. On Windows `LookPath` accepts an existing path that already has an extension, including `.sh`; `Start` then fails and OS defaults are not tried. Only an extensionless path is resolved by appending `PATHEXT`.
- Start returns `{"status":"started","profile":...}` without waiting for flags or a page. The browser stays a child of the service. It is not a new session. Marasi does not write an args file or create the profile directory. The stub must record its own argv. It does not publish an events frame.
- Launched Chrome sets `--proxy-bypass-list=<-loopback>`. That removes Chrome's implicit localhost bypass, so localhost is sent through Marasi.
- Path and profile mutations publish `chrome.path.added`, `chrome.path.removed`, `chrome.profile.added`, and `chrome.profile.removed`.
- `--cdp` waits up to 5 seconds for the endpoint to answer before returning. Without `--cdp`, start returns at once and never reports `cdp`, even when the chrome profile has a live CDP endpoint.
- A CDP endpoint is live only when `DevToolsActivePort` parses (port 1-65535, path starting `/devtools/browser/`), `http://127.0.0.1:<port>/json/version` answers 200 without a proxy, and its `webSocketDebuggerUrl` ends with that path. Anything else is stale: Marasi deletes the file and launches with `--remote-debugging-port=0`. A stub server that answers with a different browser id counts as stale.
- `chrome_running_without_cdp` comes from the 5-second wait timing out. A stub that exits without writing the file is how to reach it. Real Chrome reaches it when the chrome profile is already open without CDP, because the new process hands off to the running browser and exits.
- Marasi keeps no CDP state. Running `start --cdp` again is the only way to read the endpoint later. It also opens a new window in the running browser.
- The quiet switches are always on. There is no flag to turn them off. No host is bypassed or blocked: `--proxy-bypass-list=<-loopback>` is the only bypass entry, and `--host-resolver-rules` is never passed ([Chrome is quieted with switches, and Compass takes the rest](../../../../docs/adr/2026-10-20-chrome-is-quieted-with-switches-and-compass-takes-the-rest.md)). Traffic no switch stops is excluded by Compass; see `scope.md`.
- Kill any process started from this run's `chrome_profiles` directory during cleanup. Match the full path with `pgrep -f "$VERIFY_CONFIG_DIR/chrome_profiles"`, never the process name, so a personal Chrome is untouched. Real Chrome starts about nine processes.
