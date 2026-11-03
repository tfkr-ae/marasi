# Control listener network address and remote

`service start --control-address HOST:PORT` serves the same control API that the Unix-domain socket serves, over plain HTTP at a network address. `service start --password` or `MARASI_PASSWORD` sets an optional control password, held only in the instance's memory. With one set, every request to that address needs it as `Authorization: Bearer`. The global `--remote URL` flag, or `MARASI_REMOTE`, makes the CLI drive that instance through the address instead of the socket. The CLI sends the password from `--password` or `MARASI_PASSWORD`.

## Sub-features

- Start with a network address: `service start --control-address HOST:PORT`. Port `0` picks a free port.
- Read the address from `control_listener` in `service start --json`, `service status --json` and `service control status`.
- Change it at runtime with `service control start|update --address A --port P` and `service control stop`.
- Read whether a password is set from `password_set` in `service control status --json`, or `password: set|none` in human output.
- Use any instance command remotely with `--remote` or `MARASI_REMOTE`, plus `--password` or `MARASI_PASSWORD`. This includes `service status`, `service restart`, `service stop`, `events`, `traffic`, `project list` and `project open`.
- Upload a local file through `--remote` with `wordlist add` and `report template add`.
- Read `cli_version` in a remote `service status`, and the `warning` it adds when the two versions differ.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a second instance for this feature so the main one keeps its socket-only setup. Use its own instance name and project name. Choose a password, export it as `MARASI_PASSWORD`, and run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$CTL_INSTANCE" service start --project-name "$CTL_PROJECT" --address 127.0.0.1 --port 0 --control-address 127.0.0.1:0 --json`. Remote commands use `dist/marasi --remote "http://$CONTROL_LISTENER" …` with `MARASI_PASSWORD` still exported, and no `--config-dir` or `--instance`.

## Driving it with shell and curl

1. Start the instance with `--control-address 127.0.0.1:0 --json`. Require exactly `instance`, `proxy_listener` and `control_listener`, and require `control_listener` to be `127.0.0.1` with an assigned port. `service status --json` must show the same `control_listener` and `os`. Human status must print `control listener network address: ADDR`.
2. No file under `$VERIFY_CONFIG_DIR/instances/` may hold the password, and `service status`, `service list` and the instance log must not contain it. `ps -axww -o args=` must not show it in the instance process's arguments.
3. Send raw HTTP requests to the address. With no password, a wrong password, or the password in a query string, require 401 `unauthorized`. With `Authorization: Bearer PASSWORD`, require 200 from `/service/status`, also with a hostname such as `Host: evil.example`. `GET /service/control-listener/token` must get 404 through the socket. A CORS preflight needs no password, and it must echo the `Origin` and allow the `Authorization` header without allowing credentials.
4. Run `--remote URL --json service status` once with `MARASI_PASSWORD` and once with `--password`. Require the instance, `cli_version` equal to `version`, and no `warning`. `MARASI_REMOTE` alone must work the same way. Build a second binary with a different `VERSION` and run the same remote status. Require `cli_version` to be that build's version and `warning: "this marasi is version X but the instance runs Y"`. Human output prints both as `cli version:` and `warning:` lines.
5. Run remote `events`, curl the proxy listener, and require remote `traffic list -q` to find the pair and the events stdout to carry its `traffic.request` and `traffic.response`.
6. Remote `wordlist add FILE` and `report template add FILE` must upload the file into the instance's config directory and delete the local file, as the local commands do. Require `wordlist.added` and `report.template.added` on the remote event stream. A second `wordlist add` with the same name must fail with `wordlist_already_exists` and keep the local file.
7. Remote `project list` must list the instance's projects. Remote `project open --name N` must resolve to the instance's `projects/N.marasi`. `project open --path rel/x.marasi` must fail with `invalid_project_request`.
8. Over the socket, run `service control status --json` and require `{"status":"active","control_listener":ADDR,"password_set":true}`. Human output is `status: active`, `control listener network address: ADDR` and `password: set`. `service control start` while one is active must fail with `listener_already_active`. `update` to a port that is already bound must fail with `listener_unavailable`, and the old address must keep serving. `update --address 127.0.0.1 --port 0` must move to a new address and close the old one, and the same password must work. `stop` prints `control listener network address stopped successfully` on stderr. Status then reads `{"status":"inactive","control_listener":null,"password_set":true}`. `stop` or `update` while inactive must fail with `listener_inactive`. `start` prints `control listener network address started on ADDR` on stderr, and the password must not change. A socket `events` stream must carry `control_listener.updated`, `control_listener.stopped` and `control_listener.started`, each with `password_set` and never the password.
9. `update --address 0.0.0.0 --port 0` writes a warning to the instance log that the address `is reachable beyond loopback and serves plain HTTP without TLS`. When stderr is not a terminal, the warning is not printed there. Move it back to `127.0.0.1` right away.
10. Start a third instance with `--control-address 127.0.0.1:0` and no password. Raw requests with no `Authorization` header must get 200, and a `Host` that is not the listener's address must get 421 `host_mismatch`. Stop it.
11. Remote `service restart --json` must return `restarted`. The same URL and password must keep working, a request with no password must still get 401, and `service control status` must report the same address. Remote `service stop --json` must return `stopped`, and the socket `service status` must then fail.

## Gotchas

- Readiness and status JSON always include `control_listener`. It is `null` when the instance has no network address.
- The password is never written to disk. An old `<instance>.token` file from a build with the control token is ignored. Keep the password out of committed evidence.
- `--remote` with `--instance` fails with `--remote and --instance cannot be used together: the remote URL already names one instance`.
- `service start` and `service list` refuse `--remote`, for example `marasi service start acts on this machine and does not accept --remote`.
- A wrong password fails with `control password rejected by URL: check --password or MARASI_PASSWORD`, and a missing one with `control password rejected by URL: the instance needs one: pass --password or set MARASI_PASSWORD`. A URL where nothing listens fails with `no instance answers at URL`.
- `service control update` needs both `--address` and `--port`. Otherwise it fails with `service control update requires --address and --port`.
- `service restart --json` returns `instance`, `status`, `version` and `proxy_listener` but not `control_listener`. Read the address from `service control status`.
