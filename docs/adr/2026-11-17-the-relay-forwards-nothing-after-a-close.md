# The relay forwards nothing after a close

The WebSocket relay is transparent: it forwards what each peer sends, protocol violations included, so the receiving peer decides what to do with them. This ADR adds one exception. Once a close has gone to a peer, the relay forwards only another close to that peer. A later message, ping or pong toward that peer is still processed and stored like any other message, so Checkpoint, extensions and storage see it. It is stored with `metadata.dropped_after_close` true and is not sent. The reply close and the TCP teardown still go through, and the other direction keeps forwarding until its own close is relayed.

RFC 6455 §5.5.1 says an endpoint must not send data after its close. When the relay forwards such data, a peer that has already answered the close can reset the connection when the data arrives. A reset makes the TCP stack discard the reply close the relay has not yet read, so the other peer never gets it. Autobahn 7.1.6 shows this under Checkpoint: the client sends a close and then a ping, and the strict server resets the connection when the ping reaches it. A direct connection has no relay between the peers, so the ping and the reply close cross on the wire and the reset never happens.

The check happens when the frame is written, inside the lock that orders the writes to that peer, not when the frame is read. A message held in Checkpoint while a close goes to the same peer, for example one injected toward it, is not sent after that close when it is released.

## Considered options

- **Forward everything (the old behaviour).** This is the most transparent option, but Marasi then sends data after a close for the peer, which no endpoint may do. It also loses the reply close whenever the receiving peer resets.
- **Drop frames after a close without recording them.** This keeps the wire correct, but hooks and the stored history would no longer show what the peer sent. Marasi is a testing tool, and what a peer sends after its close is evidence.
- **Record them and do not forward them (chosen).** The wire stays valid under RFC 6455, and the full history is kept.
