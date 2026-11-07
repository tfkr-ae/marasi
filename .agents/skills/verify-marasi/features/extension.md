# Extension

Users inspect and change the Lua extensions loaded in the open project: source, print logs, settings, function calls, and enabled state.

## Sub-features

- List loaded extensions with `extension list`.
- Read one extension's lua and settings with `extension get`, and with `--json` its web component globals `ui_code` and `version`.
- Replace lua with `extension update` from `--file` or piped stdin.
- Read print logs with `extension logs`.
- Follow print lines and `marasi.gui` calls live with `events`: `extension.log`, `extension.gui.rendered`, and `extension.gui.updated`.
- Read what an extension last drew with `extension ui`.
- Read settings with `extension settings get`.
- Replace settings with `extension settings set` from `--file` or piped stdin.
- Call a global lua function with `extension call`.
- Enable or disable with `extension enable` and `extension disable`.
- Read the Lua API reference with `extension api [NAME]`. It needs no service.
- Choose human-readable or `--json` output except `settings get` and `ui`, which always print JSON.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" extension list`. Copy an id into later get, update, logs, settings, call, enable, and disable commands. A fresh project seeds `compass`, `checkpoint`, and `workshop`. There is no create or install command.

## Driving it with shell and curl

Run `extension list --json` and require items named `compass`, `checkpoint`, and `workshop`. Save the workshop `id`. Run `extension get "$EXT_ID" --json` and require that id plus non-empty `lua_content`. Write a lua file that defines `function poke() print("poked") end`. Run `extension update "$EXT_ID" --file "$LUA" --json` and require that lua in `lua_content`. Run `extension call "$EXT_ID" poke --json` and require `{"status":"called"}`. Run `extension logs "$EXT_ID" --json` and require a log line containing `poked`. Run `extension settings set "$EXT_ID" --file "$SETTINGS" --json` with `{"theme":"verify"}`, then `extension settings get "$EXT_ID"` and require that theme. Run `extension disable "$EXT_ID" --json` and `extension enable "$EXT_ID" --json`.

For live output, start `events --json` in the background before the call. Update the workshop lua to define `function draw() marasi.gui:render("main", {title = "verify"}) marasi.gui:update("progress", 1) print("drawn") end`, then `extension call "$EXT_ID" draw`. Require, in order, `extension.gui.rendered` with `"target":"main","schema":{"title":"verify"}`, `extension.gui.updated` with `"key":"progress","value":1`, and `extension.log` with `"text":"drawn"`, each with the workshop `id` and `"name":"workshop"`. Run `extension disable "$EXT_ID"`, call `draw` again, and require no new `extension.gui.*` or `extension.log` line; `extension logs` still gains the `drawn` line.

For the reference, run `extension api` with no service and require the `HOOKS`, `LIBRARIES`, and `TYPES` headings. Run `extension api marasi.strings.upper --json` and require `kind` `function` and `name` `marasi.strings:upper`. Pick a function from `extension api marasi.strings`, call it with `:` in a lua file passed to `extension update`, print the result, and require it in `extension logs`. That proves the documented form runs.

## Gotchas

- Update and settings set require `--file` or piped stdin. A TTY stdin fails.
- `--args` on call must be a JSON array. A missing function returns `function_not_found`.
- A Lua error in `extension update` or `extension call` is refused with `lua_error` and the message Lua raised, for example `calling extension: lua_error: [string "function boom()"]:2: nope`. A syntax error names its line too: `[string "this is not lua"]:1: syntax error near is`. The chunk name is the source's first line, cut short by Lua when long.
- A refused `extension update` stores nothing: `extension get` still shows the previous source and `updated_at`, no `extension.updated` is published, and the project still reopens. A project that fails to open logs `Failed to open project` at ERROR with the cause, and `project.opening_failed` names it too.
- Human `extension get` prints lua only. List, logs, and mutation confirmations use the usual stdout/stderr split. `extension settings get` always writes `{"settings":...}` to stdout.
- Update, settings set, and enable/disable publish `extension.updated`, `extension.settings.updated`, and `extension.enabled`. `extension.settings.updated` carries the extension's `id` and `name` with the new `settings`, though the `settings set` response is still `{"settings":…}` alone. Subscribe with `events` before the mutation; the stream has no replay. Call and logs publish no event of their own; Lua that prints or calls `marasi.gui` publishes `extension.log`, `extension.gui.rendered` or `extension.gui.updated` while the extension is enabled, and nothing while it is disabled (docs/adr/2026-10-06-extension-enabled-honored.md).
- `extension get --json` carries `ui_code` and `version`: the string globals as the extension's last Lua execution left them, each `""` when unset (a fresh project's seeded extensions set neither). To prove it, `extension update` the workshop with Lua that sets both, require them in `extension get --json`, then start `events --json`, update with a new `ui_code`, and require `extension.updated` with the workshop `id` followed by the new value on the next `get`. A source that drops the global keeps the old value until the project reopens, because update runs in the existing Lua state.
- `extension api` ignores `--instance` and the service. An unknown NAME exits 1 with `no Lua API entry named`.
- Extensions belong to the currently open project. After `project open`, use that project's list; do not reuse ids from the previous project without listing again.
- `extension ui UUID` (`GET /extension/{id}/ui`) prints `{"id","name","renders","state"}` as JSON in both modes: what an enabled extension last drew per `marasi.gui:render` target other than `toast`, and its last `marasi.gui:update` value per key, so a client that joins later can draw it. Both maps are `{}` when nothing is kept. Disabling the extension forgets it, nothing is kept while it is disabled, and `project open` starts empty. A new render at a target replaces the old one; an `extension update` whose code no longer draws a target leaves the old render until disable or reopen. To prove it, run `extension update` on the workshop with Lua that renders `main` twice and `toast` once and updates one key, then require only the second `main` schema and that key in `extension ui`.
- `marasi.gui` exists only in a service instance's extensions, with `marasi.gui.type` `marasi-service`. `extension.log` carries `{"id","name","time","text"}`, `extension.gui.rendered` `{"id","name","target","schema"}`, and `extension.gui.updated` `{"id","name","key","value"}`. A `render` schema that is not a table is a Lua error.
