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

Run `project open --name "$OTHER_PROJECT" --json`. Require `project` to be the canonical absolute path ending in `$OTHER_PROJECT.marasi`. Run `service status --json` and require the same `project` path with `status` `running`. Send one proxied request through the current `proxy_listener`, then `traffic list --path /proof.txt --json` and require the new project to contain that request rather than the previous project's history.

## Gotchas

- `--name` and `--path` are mutually exclusive and one is required.
- `--name` uses `$configDir/projects/<name>.marasi`, same as `service start --project-name`. `--path` must end in `.marasi`.
- A project has an exclusive lock. Opening a project another instance already holds fails.
- After a switch, `traffic list` and `traffic get` no longer see the previous project's rows.
