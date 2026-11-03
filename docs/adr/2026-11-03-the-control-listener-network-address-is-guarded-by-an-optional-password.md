# The control listener's network address is guarded by an optional password

The control token was hard to set up. It was generated, stored in a file, and could be read only through the socket, so a GUI built on the service, or a phone pairing with an instance, needed an extra trip through the socket to obtain a secret the user never chose. The token is replaced by an optional control password that the user chooses with `--password` or `MARASI_PASSWORD`. That one name sets the password on `service start`, `service control start`, and `service control update`, and presents it alongside `--remote`. The password is kept only in the instance's memory: a restart passes it to the successor through the successor's environment, never as an argument, and stopping the instance forgets it. Any client of the control listener, on the socket or on the network address, can change the password or remove it with `--no-password`. Changing it drops the network connections and event streams that used the old one. Clients send it as `Authorization: Bearer`.

With a password, the network address behaves as the token version did. It echoes any `Origin` and accepts any `Host`, because a page or a rebound name that lacks the password gets nothing. Hostnames, Tailscale names, reverse proxies, and sibling containers all work.

Without a password, the network address still keeps out websites in the user's browser. CORS does not do this: a simple `POST` reaches the server without a preflight, and CORS only stops the page from reading the response, which the network address allows anyway. Chrome 142 and Firefox 153 ask the user before a public site reaches loopback or the local network, but Safari does not yet, and a user can click allow. So without a password:

- A request that carries an `Origin` is refused unless that origin was allowed with `--allow-origin` or `MARASI_ALLOW_ORIGIN`. Native clients send no `Origin` and are unaffected. There is no default list: a GUI passes its own origin, or sets a password.
- `Host` must name the bound IP, or `localhost` on loopback. On a wildcard address it must be an IP or `localhost`. A DNS-rebound page arrives as same-origin with no `Origin` and with its own domain in `Host`, so this rule stops it from reading the instance.
- No `GET` route changes state, because `<img src>` sends a `GET` with no `Origin`.

A network address without a password warns on stderr and in the instance log. Beyond loopback it is refused unless `--no-password` is passed, so exposing an instance that can run Lua to a network is a deliberate choice. The plain-HTTP warning beyond loopback stays.

`marasi service control pair` prints a QR code containing `{"url","password","instance"}`, with the password included when one is set. For a wildcard address the URL uses the IP of the default-route interface, which `--host` overrides. Pairing reads the address and password from a route that only the socket serves, or, with `--remote`, from the CLI's own URL and password.

This supersedes the token, CORS, and socket-only token rules in [The control listener's network address is guarded by a token alone](2026-10-24-the-control-listener-network-address-is-guarded-by-a-token-alone.md). Its plain-HTTP, `https` `--remote`, and remote path and upload rules stand.

**Considered options:**

- Keep the token. Rejected because a generated secret that only the socket can reveal made setting up a GUI and pairing indirect, and nothing it guarded needed more than a chosen password.
- Store the password on disk. Rejected because a restart can hand it over in memory, and nothing else needs it after the instance stops.
- Allow password changes only through the socket. Rejected because a network client that holds the password already controls the instance.
- Accept any origin without a password and rely on Local Network Access. Rejected because Safari lacks it and a prompt can be accepted.
- Apply an origin allowlist even with a password. Rejected because a page without the password gets nothing, and the allowlist would add a setup step for every browser GUI.
- Ship a built-in list of GUI origins. Rejected because a GUI that starts its instance can pass its own origin or a password.
- Add a `Host` allowlist for hostnames without a password. Rejected because setting a password already makes every hostname work.
- Accept any `Host` on a wildcard address without a password. Rejected because a DNS-rebound page could then read traffic.
