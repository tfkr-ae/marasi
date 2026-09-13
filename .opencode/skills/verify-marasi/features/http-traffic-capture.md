# HTTP traffic capture

Marasi accepts an HTTP client's connection on its assigned proxy listener, forwards the request to the origin, returns the origin response, and records both halves in the selected project.

## Sub-features

- Forward a plain HTTP request through the proxy.
- Return status, headers, and body from the origin.
- Persist request method, host, path, raw request, response status, headers, body, and timestamps.
- Capture HTTPS through Marasi's generated CA and TLS interception path.
- Publish live request and response events through the secondary control API's `/events` stream.

## How to get to it (user POV)

Start a service and read `proxy_listener` from `service status`. Configure the HTTP client's proxy setting to `http://$PROXY_LISTENER`, then make a request normally. For `curl` requests to localhost, clear proxy bypass with `--noproxy ''`.

## Driving it with shell and curl

Start a local HTTP origin that returns a unique body. Run `curl --fail --show-error --noproxy '' --proxy "http://$PROXY_LISTENER" "http://127.0.0.1:$ORIGIN_PORT/proof.txt"`, saving request command, response headers, and response body. Require the unique body and HTTP 200. Then use `traffic list --path /proof.txt --status-code 200 --json` and `traffic get "$TRAFFIC_ID" --json` to prove Marasi recorded the same exchange.

## Gotchas

- `NO_PROXY` commonly contains localhost. `--proxy` alone may bypass Marasi, so include `--noproxy ''`.
- The helper covers HTTP. Changes to certificates, CONNECT handling, or TLS interception need a separate HTTPS drive.
- A successful origin response alone does not prove capture. The traffic CLI and database row are required evidence.
- The local origin is a real production boundary. Do not replace Marasi's proxy or repository with test doubles.
