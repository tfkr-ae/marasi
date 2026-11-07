# The event stream resumes from a shared replay buffer

`GET /events` was live only. A client that lost the stream, through a network blip, a sleeping laptop, or the service dropping it because its queue overflowed, could not tell what it had missed and re-read every store. The stream now resumes.

Every published event gets an event id, `<run_id>.<seq>`: the process's run ID and a counter the broadcaster advances once per publish, under the lock it already takes. Every subscriber sees the same id for the same event, written as the SSE `id:` line. Clients treat the id as opaque and compare it only for equality. The `: connected` and `: heartbeat` comments carry no id. Every published event is replayable.

The instance keeps one replay buffer shared by all subscribers, holding the newest events up to 4096 events or 16 MiB of payload, whichever comes first, oldest evicted. A client resumes by sending the last id it saw as `Last-Event-ID`, or as `?last_event_id=` when it cannot set headers; the header wins when both are present, and an empty value is a fresh subscribe. The handler reads the events after that id and registers the subscriber in one step under the publish lock, so nothing is lost or repeated between the replay and the live queue. No state about a client survives its disconnect.

A resume is all or nothing. When the id is older than the buffer (`expired`), from another run (`restarted`), or not one the instance issued (`invalid`), the first event is `stream.reset` with `{"reason"}`, and the stream goes live with no replay. Its id names the newest event at that moment, so a client can resume from it later. A bad id is never answered with 400: a client that keeps a stale id would otherwise fail every reconnect instead of resyncing once.

A subscriber's queue holds 1024 events and its last slot is reserved. At 1023 queued events the next publish puts `stream.dropped` with `{"reason":"overflow"}` in that slot, with no id because it is the subscriber's own and not replayable, and closes the stream. The buffer is larger than the queue, so the dropped client can resume from the last id it received.

**Considered options:**

- A numeric id per instance, without the run ID. Rejected because a restarted instance starts its count again, and an old id would then resume at the wrong place instead of resetting.
- A buffer per subscriber kept after disconnect. Rejected because it holds memory for clients that may never return and needs expiry; one shared buffer costs the same for any number of clients.
- Replay what is still buffered when the id is too old. Rejected because the client cannot tell which events it lost, and would have to re-read everything anyway.
- Answer an unknown id with 400. Rejected because a client then has to know to retry without the id; the reset tells it in-band.
- Give `stream.dropped` an id and keep it in the buffer. Rejected because it concerns one subscriber, and other clients resuming would replay it.
