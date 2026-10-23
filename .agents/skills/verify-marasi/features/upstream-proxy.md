# Upstream proxy

Users keep a machine-level list of named upstream proxies (`http`, `https` or `socks5`) in `marasi_config.yaml`, and each project selects at most one. All traffic to the origin then leaves through the selected upstream proxy, and each pair records the route that carried it. A route that cannot carry a request fails closed with a proxy log. It never falls back to a direct connection.

## Sub-features

- List entries with `upstream list`, with the open project's selection marked.
- Add an entry with `upstream add NAME URL [--ca PEM-FILE]`.
- Change an entry with `upstream update NAME --url … | --ca … | --no-ca`.
- Remove an entry with `upstream remove NAME`.
- Select one for the open project with `upstream use NAME`, or go direct with `upstream use --none`.
- Test an entry with `upstream check NAME TARGET-URL`.
- Read a pair's route in `traffic get`, query it with `upstream_proxy = "NAME"` or `upstream_proxy = ""`, and see it on `traffic.response`.
- Read the selection and whether it is configured in `service status`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. You need a real upstream proxy outside the repo. Write a throwaway HTTP CONNECT proxy and SOCKS5 proxy in a temp directory, for example a small Go program, and have each log the target it was asked for and require dummy credentials such as `u:p`. Do not install a proxy system-wide. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" upstream add corp-http 'http://u:p@127.0.0.1:$HTTP_PROXY_PORT'`. The other commands use the same global flags.

## Driving it with shell and curl

1. Start a local origin, an HTTP CONNECT upstream proxy and a SOCKS5 upstream proxy, each on its own `127.0.0.1` port, and run `events`.
2. `upstream add` an `http://u:p@…` entry and a `socks5://u:p@…` entry. Require `has_credentials: true` and no credentials in the `--json` list. Require `upstream_proxy.added` with the whole list as `items`.
3. `upstream use` the HTTP entry and require `{"selected":NAME,"configured":true}` with `--json` and `upstream_proxy.selected`. Proxy a curl to the origin. Require the origin body, a `CONNECT host:port` with the right `Proxy-Authorization` in the upstream proxy's log (plain HTTP is tunnelled too), `upstream_proxy: NAME (http://host:port)` in `traffic get`, the pair in `traffic list -q 'upstream_proxy = "NAME"'`, and `upstream_proxy` on `traffic.response`.
4. `upstream check` each entry against the origin and require `outcome: ok`. Add an entry with wrong credentials and require `auth_rejected`. Check a target port nothing listens on and require `rejected`. A bad target URL is `invalid_target`.
5. `upstream use` the SOCKS5 entry while the service runs and curl `http://localhost:$ORIGIN_PORT/…`. Require the SOCKS5 proxy to log the hostname `localhost` (address type domain), not an IP, and the pair to record the SOCKS5 route. `proxy_listener` must not change.
6. Stop the selected upstream proxy's process and curl again. Require a 502 whose `Warning` names the upstream proxy and `connection refused`, one `ERROR` proxy log with `context.upstream_proxy`, `context.address` and `context.cause: refused` and the pair's `request_id`, a `log.added` event, the pair's route still naming the upstream proxy it tried, and no new line in the origin's access log. `upstream check` now reports `refused`.
7. `upstream use --none`, curl again, and require the origin body, `upstream_proxy: direct` in `traffic get`, and the pair in `traffic list -q 'upstream_proxy = ""'`.
8. Search the `events` stdout, the instance log and every `--json` output for the dummy credentials. Require zero matches.

## Gotchas

- `use` dials nothing, so a broken upstream proxy shows up only as failed requests. Use `check` to test one first.
- `check` exits 0 whatever the outcome. Read `outcome`. It sends no request, so the upstream proxy logs a tunnel but the origin logs nothing.
- `check` against an IP target sends a SOCKS5 IP address type. Use a hostname such as `localhost` to prove the hostname is sent unresolved.
- Removing the selected entry, or opening a project whose selection this machine does not list, makes every request fail with `cause: not_configured`. `service status` then shows `upstream proxy: NAME (not configured on this machine; requests fail)`, and the human `list` warns on stderr.
- `update` needs one of `--url`, `--ca` or `--no-ca`. `--ca` must be a PEM file holding a certificate, or the error is `invalid_upstream_proxy`.
- `add`, `update` and `remove` publish their events only on the instance that made the edit. A second instance on the same config directory applies the change on its next request without an event.
- `marasi_config.yaml` holds the credentials and is written with mode `0600`. Keep it out of evidence.
- Pairs captured before routes were recorded have `upstream_proxy: null` and match neither `= ""` nor `= "NAME"`.
