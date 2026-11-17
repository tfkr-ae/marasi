# Go modifier errors fail closed, and proxy error responses never go upstream

Under Martian, a Go modifier that returned an error let the request continue, and Martian added a `Warning` header carrying Marasi's internal error text to the request it sent the origin. When the round trip failed, the client got an empty 502 that the stored pair could not tell apart from the origin's own. Marasi now stops a request whose Go modifier returns an error and answers it with a **Proxy error response**. An error from a Go modifier means only that something broke; control flow uses the explicit respond, drop and stop controls on the pair.

Marasi sends a proxy error response with a plain-text cause:

- a 502 when no response came from the origin: a dial failure, a route failure ([An upstream proxy fails closed](2026-10-23-an-upstream-proxy-fails-closed.md)), or a Go modifier error;
- a 400 for ambiguous framing ([Ambiguous request framing is refused with a 400](2026-11-17-ambiguous-request-framing-is-refused-with-a-400.md));
- for a request that loops back to the proxy listener, instead of the blank 200 Martian made up.

It is never sent upstream and carries no `Warning` header. It skips extensions and Checkpoint, because the origin never sent it and there is nothing for the tester to edit. The pair is stored with it and with `metadata.proxy_error` holding the cause; `metadata` is a JSON column that defaults to `{}`, so the schema does not change. The cause is also written to the proxy log, as before. A launch to an unreachable origin still finishes with a 502 pair.

Ordinary Lua errors still fail open, so a faulty extension does not take traffic down, and interrupted hooks still fail closed, as [Interrupted hooks fail closed](2026-10-09-interrupted-hooks-fail-closed.md) decided. A Go modifier is Marasi's own code: when it fails, the request may be half-modified, and forwarding it sends the origin something nobody wrote.

## Considered options

- **Fail open, as Martian did.** Rejected because a half-modified request reaches the origin, and the `Warning` header leaks Marasi's internals to the target and announces a testing tool ([The proxy adds no headers to a request](2026-10-19-the-proxy-adds-no-headers-to-a-request.md)).
- **Fail open but drop the `Warning`.** Rejected because the half-modified request still goes out.
- **An empty 502, as before.** Rejected because the tester cannot tell what failed without the proxy log, and the stored pair looks like the origin's answer.
- **Run proxy error responses through extensions and Checkpoint.** Rejected because the tester would be asked to edit a response the origin never sent.

**Consequences:** a user who relied on a failing Go modifier passing traffic through now sees a 502 instead. A traffic query on `metadata.proxy_error` separates Marasi's answers from the origin's.
