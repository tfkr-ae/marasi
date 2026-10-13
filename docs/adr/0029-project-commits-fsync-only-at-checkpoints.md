# Project commits fsync only at checkpoints

A project opens in WAL mode with `synchronous=NORMAL`, so a commit appends to the write-ahead log without an fsync, and SQLite fsyncs only when it checkpoints the log into the database. Traffic, WebSocket messages, and proxy logs reach the database through the write queue (ADR-0011), which one writer drains. The WebSocket relay sends every message, open, and close to the write queue and waits when it is full, so a slow commit stalls live connections. With the default `synchronous=FULL`, every commit was an fsync: on a GitHub runner under IO load one write took 441 ms, and a connection missed the Autobahn suite's 0.5 s close deadline (case 6.22.13).

**Considered options:** keep `FULL` and stop the relay from waiting on the writer; batch several queued items into one transaction. Rejected for now because a relay that never waits needs an unbounded queue or a policy for dropping records, and batching still stalls the relay once the queue fills behind a slow fsync. Either can be added later on top of this change.

**Consequences:** a Marasi crash loses nothing, because the log is already in the OS. An OS crash or power loss can lose the commits since the last checkpoint, but the database stays consistent. The relay still waits on the writer, so a slow enough disk can stall it, only less often.
