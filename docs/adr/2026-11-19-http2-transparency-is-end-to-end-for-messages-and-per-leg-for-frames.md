# HTTP/2 transparency is end to end for messages and per leg for frames

HTTP/2 has no single byte stream that runs from client to origin. Each leg has its own SETTINGS, flow-control windows, HPACK tables and stream IDs. **Transparent** therefore applies to messages. The h2 conformance suites compare these with the direct baseline: pseudo-header and field order and values, status, body bytes, trailers, an origin's `RST_STREAM` reaching the client on the matching stream with the same error code, and `GOAWAY`. Frame boundaries, padding, SETTINGS, `WINDOW_UPDATE` and flow control, `PRIORITY`, HPACK encoding choices and stream IDs belong to one leg. They go on the allow-list and are scored by leg rules, the MUSTs of RFC 9113, as hop-by-hop fields are in HTTP/1.1 ([Conformance is scored against a direct baseline](2026-11-16-conformance-is-scored-against-a-direct-baseline.md)). Header case lost when a request/response pair is translated between HTTP/1.1 and HTTP/2 is also allowed, as [Transparency keeps header case and order](2026-11-16-transparency-keeps-header-case-and-order.md) foresaw.

When an origin resets a stream partway through a body, Marasi forwards what arrived, resets the client's stream with the same code, and stores the pair as cut short, as it does when an HTTP/1.1 origin closes mid-body.

This amends [Conformance is scored against a direct baseline](2026-11-16-conformance-is-scored-against-a-direct-baseline.md). The h2-client and h2-origin suites stop being report-only. They become required checks on every pull request, on all four conformance runs (proxy, checkpoint-forward, checkpoint-edit, extension), as the h1 suites are. Before that, the h2-origin catalogue grows past the five cases that only prove its recording format.

## Considered options

- **Compare frames end to end.** Rejected because no proxy that terminates both legs can keep another leg's SETTINGS or frame boundaries, so every case would differ.
- **Keep the h2 suites report-only, or enforce only the proxy run.** Rejected because Checkpoint and extensions run on HTTP/2 traffic too, and a suite that does not block lets regressions merge.
