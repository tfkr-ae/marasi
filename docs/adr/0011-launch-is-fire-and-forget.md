# Launch is fire-and-forget

The control API launch action returns `{"status":"launched"}` once the proxy client has sent the request. It does not wait for persist and does not return the new traffic id. Persist and membership happen later on the write queue, the same path as captured traffic, and show up as `traffic.request` and `traffic.response` with `launchpad_id` in metadata. Waiting would mean making that queue synchronous with launch. A 204 would leave `--json` with no stdout value (ADR-0007). There is no `launchpad.launched` event. That would duplicate `traffic.request`.

**Considered options:** wait until the pair is persisted and return `{id}`; return the full pair; 204 empty; a dedicated launch event. Rejected because persist is async, a WebSocket launch never has a normal pair, JSON mode needs one value, and traffic events already carry the launchpad id.
