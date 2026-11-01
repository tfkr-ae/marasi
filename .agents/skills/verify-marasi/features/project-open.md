# Project open

Users switch a running instance to another project without stopping the service or closing the control socket.

## Sub-features

- Open by name with `project open --name`.
- Open by `.marasi` path with `project open --path`.
- Use `project switch` as an alias for `project open`.
- List named projects with `project list`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" project open --name "$OTHER_PROJECT"`. Use `--path` for an explicit `.marasi` file. Later `traffic` commands read the newly opened project.

## Driving it with shell and curl

Subscribe with `events` and wait for `: connected`. Send one proxied request to `/before.txt`. Run `traffic list -q 'path = "/before.txt"' --json` and require one row. Run `project open --name "$OTHER_PROJECT" --json`. Require `project` to be the canonical absolute path ending in `$OTHER_PROJECT.marasi`, and the events stream to show `project.opening`, one `project.upgrading` per migration with `applied` counting 1 to `total`, then `project.opened`, all with that path. Run `service status --json` and require the same `project` path with `status` `running`. Run `traffic list -q 'path = "/before.txt"' --json` and require empty `items`. Send one proxied request to `/after.txt`, then `traffic list -q 'path = "/after.txt"' --json` and require that new row with `/before.txt` still absent. Run `project list --json` and require items for both `$VERIFY_PROJECT` and `$OTHER_PROJECT`, each with `name`, canonical `project` path and `modified_at` (RFC 3339 UTC) in name order. Human `project list` prints `NAME`, `MODIFIED` and `PROJECT` columns. Touch one project file with `touch -d 2025-06-07T08:09:10Z` and require its `modified_at` to be `2025-06-07T08:09:10Z`.

### Switch with a hung origin

Start a local origin whose `/hung` handler does not answer until a release file exists, for example a Python `http.server` handler that loops on `os.path.exists("$RELEASE_FILE")` with a short sleep, then answers 200 `late answer`. Start `curl -sS --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$ORIGIN_PORT/hung"` in the background with stdout to a file, and wait until the origin logs the request. Run `project open --name "$OTHER_PROJECT" --json`, timed with `python3 -I -c 'import subprocess,sys,time;t=time.monotonic();r=subprocess.run(sys.argv[1:]);print(time.monotonic()-t,file=sys.stderr);sys.exit(r.returncode)' …`. Require exit 0 in under 1 s, while curl is still running. Create `$RELEASE_FILE`. Require curl to exit 0 with body `late answer`. Run `traffic list -q 'path = "/hung"' --json` and require empty `items`. Run `project open --name "$VERIFY_PROJECT" --json`, then `traffic list -q 'path = "/hung"' --json`, and require one row with `responded_at` null.

### Switch that waits on a held Checkpoint item

Run `checkpoint intercept on`. Start a background curl to `/held.txt` and retry `checkpoint list --kind http --json` until it holds a `request`; save its `id`. Start human `project open --name "$OTHER_PROJECT"` in the background with stderr to a file. Within 2 s, require that stderr contains `waiting for Checkpoint request $CHECKPOINT_ID: GET http://127.0.0.1:$ORIGIN_PORT/held.txt HTTP/1.1` and that `project open` is still running. Run `checkpoint intercept off`, then `checkpoint drop "$CHECKPOINT_ID"`. Require `project open` to exit 0 with stdout `project: <path ending in $OTHER_PROJECT.marasi>`.

## Gotchas

- `--name` and `--path` are mutually exclusive and one is required.
- `--name` uses `$configDir/projects/<name>.marasi`, same as `service start --project-name`. The name is trimmed, and one trailing `.marasi` suffix is stripped. `--path` must end in `.marasi`.
- Opening the path that is already open succeeds, does not clear traffic, and publishes nothing. A switch to a different project publishes `project.opening`, its `project.upgrading` progress, and `project.opened` with `{"project":...}`, or `project.opening_failed` when it fails ([events](events.md)).
- `--path` to a missing `.marasi` file in an existing directory creates a new project. A missing parent directory fails before the service is asked, with an error naming the path and `resolving project parent directory`.
- Opening a different project starts a background build of the traffic index for pairs missing from it; a same-path no-op does not restart the build. To prove it, open a second project, send one request with a distinctive body, and switch back. With no service instance controlling the second project, empty its index and mark the saved `traffic_index` job incomplete, since a fresh open reads completeness from the saved state rather than scanning: `python3 -I -c 'import sqlite3,sys;c=sqlite3.connect(sys.argv[1]);c.execute("delete from traffic_index");c.execute("update background_job set complete = 0 where name = ?", ("traffic_index",));c.commit()' "$OTHER_PATH"`. Reopen it with `events` attached. Require one `traffic.index_complete` with that project path, then a `response_body:` query for the body finds the row with `index.complete` `true`. Never do this to a project the run did not create.
- A project has an exclusive lock. Opening a project another instance already holds fails. With `--json` the error is `opening project: project_already_open`.
- Opening a different project fails with `opening project: project_busy` while an Armory run is `in_progress`. Human output says `409 Conflict` and does not include the code. Reopening the current path still succeeds. Wait for Armory to leave `in_progress` before switching.
- A switch waits for genuine project work: a held Checkpoint item, running Lua, and the request and response side of a proxied request ([A project switch waits for held Checkpoint items and says so](../../../../docs/adr/2026-10-17-project-switch-waits-for-held-checkpoint-items.md)). After about 500 ms of waiting, human `project open` prints one `waiting for ...` line per item on stderr, and again whenever that changes: `waiting for Checkpoint request $ID: GET http://… HTTP/1.1`, `waiting for extension workshop`, or `waiting for 1 request or control call in project work`. The service publishes the same list as `project.waiting`. With `--json`, stderr stays empty and stdout is still one JSON value. `checkpoint forward` and `checkpoint drop` work during the wait. Interrupting `project open` cancels the switch and keeps the current project.
- A held Checkpoint item that belongs to a pending launch is the exception: the switch does not wait for it. It interrupts the launch, drops its hold, and succeeds. Every pending launch ends `failed` with reason `interrupted`, stored in the original project, and `launch.failed` is published before `project.opened`. `GET /launch/{id}` still reads it after the switch.
- A switch never waits on the network ([A project switch never waits on the network](../../../../docs/adr/2026-10-17-project-switch-never-waits-on-the-network.md)). A request still waiting on its origin, for its headers or its body, does not hold `project open`. Its client still receives the origin's response when it arrives. The pair stays in the project it was captured in, with no response (`responded_at` null), and the new project never sees it. A WebSocket handshake answered after a switch gets `502 Bad Gateway`.
- After a switch, `traffic list` and `traffic get` no longer see the previous project's rows.
- `service status` `project` becomes the new path. The launch doctor check against `$VERIFY_PROJECT` no longer matches, even though the instance is still ours.
- `project list` is local. It reads `$configDir/projects/*.marasi` without contacting the service, follows symlinks to regular project files, skips non-regular targets, and prints name order. Different names pointing to the same file remain separate items with the same canonical `project` path. A symlink reports its target's modification time, not its own, so `touch -h` on the link changes nothing. An open project's `modified_at` can lag its newest writes, since SQLite keeps them in the `-wal` file until a checkpoint. A broken symlink fails the list instead of being skipped. It works even when no instance is running.
