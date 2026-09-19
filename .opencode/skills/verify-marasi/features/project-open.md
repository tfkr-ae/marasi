# Project open

Users switch a running instance to another project without stopping the service or closing the control socket.

## Sub-features

- Open by name with `project open --name`.
- Open by `.marasi` path with `project open --path`.
- Use `project switch` as an alias for `project open`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" project open --name "$OTHER_PROJECT"`. Use `--path` for an explicit `.marasi` file. Later `traffic` commands read the newly opened project.

## Driving it with shell and curl

Send one proxied request to `/before.txt`. Run `traffic list --path /before.txt --json` and require one row. Run `project open --name "$OTHER_PROJECT" --json`. Require `project` to be the canonical absolute path ending in `$OTHER_PROJECT.marasi`. Run `service status --json` and require the same `project` path with `status` `running`. Run `traffic list --path /before.txt --json` and require empty `items`. Send one proxied request to `/after.txt`, then `traffic list --path /after.txt --json` and require that new row with `/before.txt` still absent.

## Gotchas

- `--name` and `--path` are mutually exclusive and one is required.
- `--name` uses `$configDir/projects/<name>.marasi`, same as `service start --project-name`. `--path` must end in `.marasi`.
- A project has an exclusive lock. Opening a project another instance already holds fails.
- Opening fails with `project_busy` while a Checkpoint item is pending or an Armory run is `in_progress`. Forward or drop held items and wait for Armory to leave `in_progress` first.
- After a switch, `traffic list` and `traffic get` no longer see the previous project's rows.
- `service status` `project` becomes the new path. The launch doctor check against `$VERIFY_PROJECT` no longer matches, even though the instance is still ours.
