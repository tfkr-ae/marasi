# Extension

Users inspect and change the Lua extensions loaded in the open project: source, print logs, settings, function calls, and enabled state.

## Sub-features

- List loaded extensions with `extension list`.
- Read one extension's lua and settings with `extension get`.
- Replace lua with `extension update` from `--file` or piped stdin.
- Read print logs with `extension logs`.
- Read settings with `extension settings get`.
- Replace settings with `extension settings set` from `--file` or piped stdin.
- Call a global lua function with `extension call`.
- Enable or disable with `extension enable` and `extension disable`.
- Choose human-readable or `--json` output except `settings get`, which always prints the JSON envelope.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" extension list`. Copy an id into later get, update, logs, settings, call, enable, and disable commands. A fresh project seeds `compass`, `checkpoint`, and `workshop`. There is no create or install command.

## Driving it with shell and curl

Run `extension list --json` and require items named `compass`, `checkpoint`, and `workshop`. Save the workshop `id`. Run `extension get "$EXT_ID" --json` and require that id plus non-empty `lua_content`. Write a lua file that defines `function poke() print("poked") end`. Run `extension update "$EXT_ID" --file "$LUA" --json` and require that lua in `lua_content`. Run `extension call "$EXT_ID" poke --json` and require `{"status":"called"}`. Run `extension logs "$EXT_ID" --json` and require a log line containing `poked`. Run `extension settings set "$EXT_ID" --file "$SETTINGS" --json` with `{"theme":"verify"}`, then `extension settings get "$EXT_ID"` and require that theme. Run `extension disable "$EXT_ID" --json` and `extension enable "$EXT_ID" --json`.

## Gotchas

- Update and settings set require `--file` or piped stdin. A TTY stdin fails.
- `--args` on call must be a JSON array. A missing function returns `function_not_found`.
- Human `extension get` prints lua only. List, logs, and mutation confirmations use the usual stdout/stderr split. `extension settings get` always writes `{"settings":...}` to stdout.
- Update, settings set, and enable/disable publish `extension.updated`, `extension.settings.updated`, and `extension.enabled`. Subscribe with `events` before the mutation; the stream has no replay. Call and logs do not publish.
- Extensions belong to the currently open project. After `project open`, use that project's list; do not reuse ids from the previous project without listing again.
