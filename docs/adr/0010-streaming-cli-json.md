# Streaming commands write NDJSON

ADR-0007 says `--json` writes exactly one JSON value to stdout. `marasi events` is a live stream, so one object at exit is useless and forbidding `--json` special-cases the command agents want to pipe.

Once the stream is connected, JSON mode writes one compact object per event, each ending with a newline: `{"event":"<name>","data":<payload>}`. The payload is the control API `data:` bytes, not remarshaled. Connected comments and heartbeats are omitted. A failure before the stream connects still writes `{"error":"..."}` and exits 1. After connect, stdout stays event objects only. A later failure exits 1 with no extra JSON.

Human mode writes the `: connected` comment to stderr, then `<name> <payload>` per event on stdout, and errors to stderr. Heartbeats stay out. SIGINT and a clean close after connect exit 0. Never connected, overflow drop, and read errors exit 1.

This amends ADR-0007 for streaming commands only. Other commands still write one JSON value.

**Considered options:** forbidding `--json`; buffering an array until exit; printing payload JSON without the event name; appending `{"error":"..."}` as a trailing NDJSON line. Rejected because agents need a live named stream, an array is not live, payloads are mixed types that share `id`, and errors must not look like events.
