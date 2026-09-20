# Launchpad

Users group captured traffic into named launchpads in the open project and fire a raw HTTP working copy tagged with that pad.

## Sub-features

- Create an empty pad with `launchpad create --name`.
- List pads oldest first with `launchpad list`.
- Read one pad and its linked traffic with `launchpad get`.
- Rename or describe a pad with `launchpad update`.
- Attach an existing request UUID with `launchpad link --request`.
- Send a working copy with `launchpad launch --scheme` and `--raw-file` or piped stdin.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" launchpad create --name Login`. Link a traffic id from `traffic list` when you want a pad member. Launch by piping a raw HTTP request or passing `--raw-file`.

## Driving it with shell and curl

Run `launchpad create --name Login --description variants --json` and save `id`. Run `launchpad list --json` and require that id. Run `launchpad update "$PAD_ID" --description replay --json`. Link a captured request with `launchpad link "$PAD_ID" --request "$TRAFFIC_ID" --json`. Pipe `GET /launchpad-proof.txt HTTP/1.1` with CRLF line endings, a `Host` of the local origin, and a blank line to `launchpad launch "$PAD_ID" --scheme http --json` and require `{"status":"launched"}`. Retry `launchpad get "$PAD_ID" --json` until it contains both the linked id and a new `/launchpad-proof.txt` row. Confirm that launched path with `traffic list --path /launchpad-proof.txt --json`.

## Gotchas

- `--name` is required on create and must be non-empty. Update requires `--name` or `--description`.
- Launch needs `--scheme http` or `https`, and either `--raw-file` or piped stdin. A TTY stdin fails. The raw bytes must be parseable HTTP with a non-empty `Host` and a header/body blank line. Use CRLF.
- The working copy is the raw bytes you supply. Linked members are not replayed and are not required to launch.
- Launch returns `{"status":"launched"}` before the new traffic row is visible. Retry `launchpad get` and `traffic list` until the launched path appears.
- Launch sends through the proxy listener, persists a new traffic row, and auto-links that row to the pad.
- Link and launch look up ids in the currently open project. After `project open`, previous pads and request ids are not visible.
- Duplicate link of the same request to the same pad returns a conflict.
