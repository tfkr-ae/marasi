# Control listener network address and remote

`service start --control-address HOST:PORT` serves the same control API that the Unix-domain socket serves, over plain HTTP at a network address. Every request to that address needs the instance's control token as `Authorization: Bearer`. The global `--remote URL` flag, or `MARASI_REMOTE`, makes the CLI drive that instance through the address instead of the socket. The token comes from `--token-file PATH` or `MARASI_TOKEN`.

## Sub-features

- Start with a network address: `service start --control-address HOST:PORT`. Port `0` picks a free port.
- Read the address from `control_listener` in `service start --json`, `service status --json` and `service control status`.
- Change it at runtime with `service control start|update --address A --port P` and `service control stop`.
- Print the token with `service control token`, or replace it with `service control token --rotate`. Both work over the socket only.
- Use any instance command remotely with `--remote` or `MARASI_REMOTE`, plus `--token-file` or `MARASI_TOKEN`. This includes `service status`, `service restart`, `service stop`, `events`, `traffic`, `project list` and `project open`.
- Upload a local file through `--remote` with `wordlist add` and `report template add`.
- Read `cli_version` in a remote `service status`, and the `warning` it adds when the two versions differ.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a second instance for this feature so the main one keeps its socket-only setup. Use its own instance name and project name. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$CTL_INSTANCE" service start --project-name "$CTL_PROJECT" --address 127.0.0.1 --port 0 --control-address 127.0.0.1:0 --json`. Then run `service control token` with the same flags, and write the token to a mode `0600` file outside the repo's tracked files. Remote commands use `dist/marasi --remote "http://$CONTROL_LISTENER" --token-file "$TOKEN_FILE" …` with no `--config-dir` or `--instance`.

## Driving it with shell and curl

1. Start the instance with `--control-address 127.0.0.1:0 --json`. Require exactly `instance`, `proxy_listener` and `control_listener`, and require `control_listener` to be `127.0.0.1` with an assigned port. `service status --json` must show the same `control_listener` and `os`. Human status must print `control listener network address: ADDR`.
2. Require `$VERIFY_CONFIG_DIR/instances/$CTL_INSTANCE.token` to have mode `0600` and hold 64 hex characters. `service control token` prints it, and `--json` prints `{"token":…}`. `service status` and `service list` must not contain it.
3. Send raw HTTP requests to the address. With no token, a wrong token, or the token in a query string, require 401 `unauthorized`. With `Authorization: Bearer TOKEN`, require 200 from `/service/status`. A `Host` that is not the listener's address must get 421 `host_mismatch`. `GET /service/control-listener/token` and `POST /service/control-listener/token/rotate` must get 403 `socket_only` even with the token, and the token file must not change. A CORS preflight needs no token, and it must echo the `Origin` and allow the `Authorization` header without allowing credentials.
4. Run `--remote URL --json service status` once with `MARASI_TOKEN` and once with `--token-file`. Require the instance, `cli_version` equal to `version`, and no `warning`. `MARASI_REMOTE` alone must work the same way. Build a second binary with a different `VERSION` and run the same remote status. Require `cli_version` to be that build's version and `warning: "this marasi is version X but the instance runs Y"`. Human output prints both as `cli version:` and `warning:` lines.
5. Run remote `events`, curl the proxy listener, and require remote `traffic list -q` to find the pair and the events stdout to carry its `traffic.request` and `traffic.response`.
6. Remote `wordlist add FILE` and `report template add FILE` must upload the file into the instance's config directory and delete the local file, as the local commands do. Require `wordlist.added` and `report.template.added` on the remote event stream. A second `wordlist add` with the same name must fail with `wordlist_already_exists` and keep the local file.
7. Remote `project list` must list the instance's projects. Remote `project open --name N` must resolve to the instance's `projects/N.marasi`. `project open --path rel/x.marasi` must fail with `invalid_project_request`.
8. Over the socket, run `service control status` and require `{"status":"active","control_listener":ADDR}`. Human output is `status: active` followed by `control listener network address: ADDR`. `service control start` while one is active must fail with `listener_already_active`. `update` to a port that is already bound must fail with `listener_unavailable`, and the old address must keep serving. `update --address 127.0.0.1 --port 0` must move to a new address and close the old one, and the same token must work. `stop` prints `control listener network address stopped successfully` on stderr. Status then reads `{"status":"inactive","control_listener":null}`. `stop` or `update` while inactive must fail with `listener_inactive`. `start` prints `control listener network address started on ADDR` on stderr, and the token must not change. A socket `events` stream must carry `control_listener.updated`, `control_listener.stopped` and `control_listener.started`.
9. `update --address 0.0.0.0 --port 0` writes a warning to the instance log that the address `is reachable beyond loopback and serves plain HTTP without TLS`. When stderr is not a terminal, the warning is not printed there. Move it back to `127.0.0.1` right away.
10. Keep a remote `events` stream open and run `service control token --rotate --json`. Require a new 64-hex token in the output and in the token file. The old token must get 401 at once and the new one 200. The remote stream that used the old token must end. The socket `events` stream must carry `control_listener.token_rotated` with an empty payload and never the token.
11. Remote `service restart --json` must return `restarted`. The same URL and token must keep working, and `service control status` must report the same address. Remote `service stop --json` must return `stopped`, and the socket `service status` must then fail.

## Gotchas

- Readiness and status JSON always include `control_listener`. It is `null` when the instance has no network address.
- The token file stays in `instances/` after `service stop`. Removing the scratch config directory removes it. Keep tokens out of committed evidence, and delete any token file you wrote yourself.
- `--remote` with `--instance` fails with `--remote and --instance cannot be used together: the remote URL already names one instance`. `--token-file` without `--remote` fails with `--token-file needs --remote or MARASI_REMOTE`. `--remote` with no token fails with `--remote needs the instance's control token: set MARASI_TOKEN or pass --token-file PATH`.
- `service start` and `service list` refuse `--remote`, for example `marasi service start acts on this machine and does not accept --remote`.
- A rejected token fails with `control token rejected by URL: check MARASI_TOKEN or --token-file`. A URL where nothing listens fails with `no instance answers at URL`.
- `service control update` needs both `--address` and `--port`. Otherwise it fails with `service control update requires --address and --port`.
- `service restart --json` returns `instance`, `status`, `version` and `proxy_listener` but not `control_listener`. Read the address from `service control status`.
