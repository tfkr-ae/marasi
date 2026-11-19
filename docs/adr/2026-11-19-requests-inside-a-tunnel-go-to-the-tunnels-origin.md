# Requests inside a tunnel go to the tunnel's origin

A request read inside a `CONNECT` tunnel goes to the origin the `CONNECT` named, whatever its `Host` field says, as it would with no proxy in between. Its `Host` is only a field the tester can change. Before, Marasi routed it by its `Host`, as Martian did, so a request carrying `Host: evil.example` in a tunnel to `example.com` went to `evil.example`, under a leaf certificate the client had accepted for `example.com`.

The tunnel's authority becomes the request's URL host, without the scheme's default port, so a URL built from `CONNECT example.com:443` reads `https://example.com/…` as one built from the browser's `Host` would. An edit in Checkpoint keeps the held request's destination unless its request line names a target. Scope host rules match the destination, so a tester who changes the `Host` of an in-scope request stays in scope. A modifier that changes the URL's host (Lua `url:set_host`, a waypoint) still sends the request elsewhere; Lua `req:set_host` changes only the field.

Outside a tunnel nothing changes: an absolute-form request goes to its target, and TLS sent straight to the proxy listener, which has no `CONNECT` authority, goes by its `Host`. A Launchpad or Armory request has no target apart from its `Host`, so it still goes there.

## Considered options

- **Route by `Host`, as Martian did.** Rejected because Host header testing (password-reset poisoning, cache poisoning, virtual-host and routing bypasses) needs a changed `Host` delivered to the real origin, and a request sent somewhere the client's TLS session never named is not what a direct connection would do.

**Consequences:** the in-tunnel request's `Host` and its destination can differ, and the stored pair's host column and sitemap still show the `Host` field. The conformance cases whose `Host` names no reachable server now reach nginx at the tunnel's end, so their accepted deviations are removed.
