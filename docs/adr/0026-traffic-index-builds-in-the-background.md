# The traffic index builds in the background

The migration only creates the contentless FTS5 table. A background build then indexes every request/response pair missing from the index, newest first, in small batches. Each batch is one transaction. Newly captured pairs, late responses, and note or metadata changes are indexed by the traffic repository's Go write path. The build runs on every open, so the same mechanism handles the first build, resuming after a crash or a project switch, and pairs written by an older Marasi binary that has no indexing code. A project switch cancels the build instead of waiting for it. Until the build finishes, traffic list responses report `index.complete: false`. When it finishes, `traffic.index_complete` is announced once.

**Considered options:** backfill inside a Go migration; a saved `indexed_through` marker; SQLite triggers. Rejected because a migration backfill blocks opening a large project for minutes, a marker cannot see pairs that older binaries add later, and triggers would need the binary check and head/body split either in SQL or in a Go function that older binaries do not register.

**Consequences:** query results can be incomplete while `index.complete` is false. This reverses the earlier stance that queries never return partial results, because the alternative is minutes of blocked project opens.
