# Launchpad

Users group captured traffic into named launchpads in the open project and fire a raw HTTP working copy tagged with that pad.

## Sub-features

- Create an empty pad with `launchpad create --name`.
- List pads oldest first with `launchpad list`.
- Read one pad and its linked traffic with `launchpad get`.
- Rename or describe a pad with `launchpad update`.
- Attach an existing request UUID with `launchpad link --request`.
- Send a working copy with `launchpad launch --scheme` and `--raw-file` or piped stdin.
- Cancel a pending launch by its launch id with `launchpad cancel`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" launchpad create --name Login`. Link a traffic id from `traffic list` when you want a pad member. Launch by piping a raw HTTP request or passing `--raw-file`.

## Driving it with shell and curl

Run `launchpad create --name Login --description variants --json` and save `id`. Run `launchpad list --json` and require that id. Run `launchpad update "$PAD_ID" --description replay --json`. Link a captured request with `launchpad link "$PAD_ID" --request "$TRAFFIC_ID" --json`. Pipe `GET /launchpad-proof.txt HTTP/1.1` with CRLF or LF line endings, a `Host` of the local origin, and a blank line to `launchpad launch "$PAD_ID" --scheme http --json` and require `{"launch_id":"…","status":"pending"}`. Save `launch_id`. Retry `launchpad get "$PAD_ID" --json` until it contains both the linked id and a new `/launchpad-proof.txt` row whose id is the launch id. Confirm that launched path with `traffic list -q 'path = "/launchpad-proof.txt"' --json`. Retry until that row's `metadata` is `{"launch":{"status":"completed"},"launchpad":true,"launchpad_id":"$PAD_ID"}`. `traffic list -q 'metadata.launch.status = "completed"' --json` must return it. `traffic list -q 'metadata.launchpad = true' --json` and `-q "metadata.launchpad_id = \"$PAD_ID\""` must return it, and `-q 'metadata.launchpad = "true"'` must not, because the stored value is a JSON boolean.

To prove cancel, serve an origin path that never answers, launch it, and save `launch_id`. Run `launchpad cancel "$LAUNCH_ID" --json` and require `{"launch_id":"$LAUNCH_ID","launchpad_id":"$PAD_ID","status":"cancelled"}`. Retry `traffic list -q 'metadata.launch.status = "cancelled"' --json` until it returns the launch id with no response. Run the same cancel again: it must exit 1 with `{"error":"cancelling launch: launch_finished: launch $LAUNCH_ID is cancelled"}`. A random UUID must exit 1 with `cancelling launch: not_found`.

## Gotchas

- `--name` is required on create and must be non-empty. Update requires `--name` or `--description`.
- Launch needs `--scheme http` or `https`, and either `--raw-file` or piped stdin. A TTY stdin fails. The raw bytes must be parseable HTTP with a non-empty `Host` and a header/body blank line. CRLF works; LF-only is accepted.
- The working copy is the raw bytes you supply. Linked members are not replayed and are not required to launch.
- Launch returns `{"launch_id":"…","status":"pending"}` as soon as the launch is accepted, before the round trip. `GET /launch` lists pending launches and `GET /launch/{id}` reads one while it is pending. The launch then ends as `completed`, `cancelled`, or `failed` (with a `reason`), published as `launch.completed`, `launch.cancelled`, or `launch.failed`, and the outcome is merged into the row's `metadata.launch`. Retry `launchpad get` and `traffic list` until the launched path and its `metadata.launch` appear.
- `POST /launch/{id}/cancel` (`launchpad cancel`) works from any client, not just the one that launched. It cancels the proxy client's request, so the upstream round trip is abandoned and no response is stored. A finished launch returns 409 `{"error":"launch_finished",…}` with its final record, read from the stored pair's `metadata.launch`. An id that is not a launch returns 404 `not_found`.
- Launch sends through the proxy listener, persists a new traffic row, and auto-links that row to the pad. The proxy client tags the request with an internal `x-marasi-launch-id` header. Marasi honors it only from its own client for a pending launch, uses it as the row id, sets the `launchpad` and `launchpad_id` metadata keys from the launch, and removes it before sending. The stored raw request does not contain it. The header from any other client is removed and ignored.
- Link and launch look up ids in the currently open project. After opening a different project, previous pads and request ids are not visible. Opening the current path does not hide them.
- Duplicate link of the same request to the same pad returns a conflict.
