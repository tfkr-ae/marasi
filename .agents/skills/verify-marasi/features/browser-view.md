# Browser view and Marasi's own pages

Users open Marasi's own pages at the host `marasi` through the proxy listener: the CA certificate at `/cert`, and a browser view of a pair at `/view/{uuid}`. A browser view shows a pair's stored response, unchanged, at its original URL, without contacting the origin ([A browser view serves the stored response once, at its original URL](../../../../docs/adr/2026-10-20-a-browser-view-serves-the-stored-response-once-at-its-original-url.md)).

## Sub-features

- Download the CA certificate from `http://marasi/cert` (or `https://marasi/cert`).
- Open `http://marasi/view/{uuid}`: a redirect to the pair's original URL, then the stored response for the next request to that URL.
- Plain-text errors: 404 for an unknown UUID, a pair number, or an unknown path on `marasi`; 409 for a pair with no response or a WebSocket upgrade.
- Requests to `marasi` and the stored-response request are never captured.

## How to get to it (user POV)

Start a service and read `proxy_listener` from `service status --json`. Point a browser or `curl --noproxy '' --proxy "http://$PROXY_LISTENER"` at the URLs above. Take the UUID from `traffic list --json`. There is no CLI command or control API call for these pages.

## Driving it with shell and curl

Run a local origin whose answer changes on every request, for example by numbering its visits, and count the requests it receives. Capture a request to it through the proxy, then take that pair's `id` from `traffic list -q 'host = "..."' --json` and note the pair count.

1. `curl -i` `http://marasi/cert` through the proxy. Require 200, `Content-Type: application/x-x509-ca-cert`, and a body equal to `certificate get --format der`.
2. `curl -i http://marasi/view/$ID`. Require `302`, `Cache-Control: no-store`, and `Location` equal to the captured URL.
3. `curl -i` that `Location` from the same client. Require the stored status, headers, and body from `traffic get $ID` (decode `response.raw`), the origin's request count unchanged, and the pair count unchanged.
4. Request the same URL again. Require a live answer, one more origin request, and one more pair.
5. Capture a POST, open its view, and follow with a GET. Require the stored POST response.
6. Require 404 for `/view/<random uuid>`, for `/view/1`, and for `/nothing`, each `text/plain` with no `Location`. Require the pair count unchanged after steps 1, 2, 3 and 6.

For a rendered proof, run headless Chrome with `--proxy-server=http://$PROXY_LISTENER '--proxy-bypass-list=<-loopback>' --screenshot` on `http://marasi/view/$ID`. Only the page's subresources reach the origin, and they are captured.

## Gotchas

- `http://marasi.cert` is not a Marasi page. It goes to the network like any other host.
- A pending browser view is keyed on the client host and the exact URL. Follow the redirect from the same client address within 10 seconds. A curl run from another address, or after 10 seconds, reaches the origin.
- It is used once, and opening a newer view of the same URL from the same client replaces it.
- Launches and Armory runs never use a pending browser view, even from the same machine.
- `project open` clears every pending browser view.
- Chrome bypasses the proxy for loopback origins unless you pass `--proxy-bypass-list=<-loopback>`.
- The stored response keeps its `Set-Cookie` and cache headers. Use a fresh browser profile for each run.
