# Extension

Users inspect and change the Lua extensions loaded in the open project: source, print logs, settings, function calls, and enabled state.

## Sub-features

- List loaded extensions with `extension list`.
- Read one extension's lua and settings with `extension get`.
- Replace lua with `extension update` from `--file` or piped stdin.
- Read print logs with `extension logs`.
- Follow print lines and `marasi.gui` calls live with `events`: `extension.log`, `extension.gui.rendered`, and `extension.gui.updated`.
- Read settings with `extension settings get`.
- Replace settings with `extension settings set` from `--file` or piped stdin.
- Call a global lua function with `extension call`.
- Enable or disable with `extension enable` and `extension disable`.
- Read the Lua API reference with `extension api [NAME]`. It needs no service.
- Choose human-readable or `--json` output except `settings get`, which always prints the JSON envelope.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" extension list`. Copy an id into later get, update, logs, settings, call, enable, and disable commands. A fresh project seeds `compass`, `checkpoint`, and `workshop`. There is no create or install command.

## Driving it with shell and curl

Run `extension list --json` and require items named `compass`, `checkpoint`, and `workshop`. Save the workshop `id`. Run `extension get "$EXT_ID" --json` and require that id plus non-empty `lua_content`. Write a lua file that defines `function poke() print("poked") end`. Run `extension update "$EXT_ID" --file "$LUA" --json` and require that lua in `lua_content`. Run `extension call "$EXT_ID" poke --json` and require `{"status":"called"}`. Run `extension logs "$EXT_ID" --json` and require a log line containing `poked`. Run `extension settings set "$EXT_ID" --file "$SETTINGS" --json` with `{"theme":"verify"}`, then `extension settings get "$EXT_ID"` and require that theme. Run `extension disable "$EXT_ID" --json` and `extension enable "$EXT_ID" --json`.

For live output, start `events --json` in the background before the call. Update the workshop lua to define `function draw() marasi.gui:render("main", {title = "verify"}) marasi.gui:update("progress", 1) print("drawn") end`, then `extension call "$EXT_ID" draw`. Require, in order, `extension.gui.rendered` with `"target":"main","schema":{"title":"verify"}`, `extension.gui.updated` with `"key":"progress","value":1`, and `extension.log` with `"text":"drawn"`, each with the workshop `id` and `"name":"workshop"`. Run `extension disable "$EXT_ID"`, call `draw` again, and require no new `extension.gui.*` or `extension.log` line; `extension logs` still gains the `drawn` line.

For the reference, run `extension api` with no service and require the `HOOKS`, `LIBRARIES`, and `TYPES` headings. Run `extension api marasi.strings.upper --json` and require `kind` `function` and `name` `marasi.strings:upper`. Pick a function from `extension api marasi.strings`, call it with `:` in a lua file passed to `extension update`, print the result, and require it in `extension logs`. That proves the documented form runs.

## Gotchas

- Update and settings set require `--file` or piped stdin. A TTY stdin fails.
- `--args` on call must be a JSON array. A missing function returns `function_not_found`.
- Human `extension get` prints lua only. List, logs, and mutation confirmations use the usual stdout/stderr split. `extension settings get` always writes `{"settings":...}` to stdout.
- Update, settings set, and enable/disable publish `extension.updated`, `extension.settings.updated`, and `extension.enabled`. Subscribe with `events` before the mutation; the stream has no replay. Call and logs publish no event of their own; Lua that prints or calls `marasi.gui` publishes `extension.log`, `extension.gui.rendered` or `extension.gui.updated` while the extension is enabled, and nothing while it is disabled (docs/adr/2026-10-06-extension-enabled-honored.md).
- `extension api` ignores `--instance` and the service. An unknown NAME exits 1 with `no Lua API entry named`.
- Extensions belong to the currently open project. After `project open`, use that project's list; do not reuse ids from the previous project without listing again.
- `marasi.gui` exists only in a service instance's extensions, with `marasi.gui.type` `marasi-service`. `extension.log` carries `{"id","name","time","text"}`, `extension.gui.rendered` `{"id","name","target","schema"}`, and `extension.gui.updated` `{"id","name","key","value"}`. A `render` schema that is not a table is a Lua error.
