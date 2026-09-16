# HTTP traffic capture

Marasi accepts an HTTP client's connection on its assigned proxy listener, forwards the request to the origin, returns the origin response, and records both halves in the selected project.

## Sub-features

- Forward a plain HTTP request through the proxy.
- Return status, headers, and body from the origin.
- Persist request method, host, path, raw request, response status, headers, body, and timestamps.
- Capture HTTPS through Marasi's generated CA and TLS interception path.
- Emit live `traffic.request` and `traffic.response` events for each captured exchange. Subscribe with `events`.

## How to get to it (user POV)

Start a service and read `proxy_listener` from `service status`. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" events` and wait for `: connected` on stderr before sending traffic. Configure the HTTP client's proxy setting to `http://$PROXY_LISTENER`, then make a request normally. For `curl` requests to localhost, clear proxy bypass with `--noproxy ''`.

## Driving it with shell and curl

Start a local HTTP origin that returns a unique body. Start `events` first and wait until stderr is `: connected`. Run `curl --fail --show-error --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$ORIGIN_PORT/proof.txt"`, saving request command, response headers, and response body. Require the unique body and HTTP 200. Require events stdout to contain `traffic.request` then `traffic.response` whose JSON `id` matches. Then use `traffic list --path /proof.txt --status-code 200 --json` and `traffic get "$TRAFFIC_ID" --json` to prove Marasi recorded the same exchange.

## Gotchas

- `NO_PROXY` commonly contains localhost. `--proxy` alone may bypass Marasi, so include `--noproxy ''`.
- The helper covers HTTP. Changes to certificates, CONNECT handling, or TLS interception need a separate HTTPS drive. Trust `$VERIFY_CONFIG_DIR/marasi_cert.pem` with `curl --cacert` and send an `https://` request through the proxy. The origin certificate must be trusted by the proxy's outbound transport. A self-signed local origin returns 502; use a publicly trusted host.
- `events` is live and has no replay. Subscribe before the request. stderr is `: connected`; stdout is `event-name json`.
- A successful origin response alone does not prove capture. The events CLI, traffic CLI, and database row are required evidence.
- The local origin is a real production boundary. Do not replace Marasi's proxy or repository with test doubles.
