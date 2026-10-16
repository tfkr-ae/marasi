# Dashboard counts are kept as pairs are written

The dashboard's pair count and intercepted count are stored in `dashboard_count`, not computed by scanning `request`. Counting intercepted pairs read every row's metadata, which sits after both raw bodies, so a cold 85,000-pair (1 GB) project took 2.9 s, and the project's single connection queued every other read behind it. Counting `request` read its whole primary-key index, 38 ms cold and growing with the project.

The pair count follows `pair_number` (ADR-0042) through triggers on that table. Only current binaries insert numbers, so the insert trigger costs nothing on another statement, and deleting a pair cascades to its number, so the delete trigger also counts deletes by an older binary. The intercepted pairs are a set, `pair_intercepted`, whose triggers keep its count. The repository adds or removes a pair in the transaction that writes the pair's metadata, and only when that write can touch the `intercepted` key. The `intercepted_count` background job (ADR-0039) adds the pairs captured before the migration, and the pairs an older binary captures later, which are above its high-water mark. A set means a pair that both the job and a write add counts once. `DashboardCounts` reports the job's completeness. `CountIntercepted` stays exact by scanning until the job is complete.

**Considered options:**

- **Triggers on `request` that keep the intercepted set for every writer.** Rejected because they cost about 15% of capture throughput: SQLite compiles a table's triggers into every statement that writes it, and the capture path prepares its statements each time.
- **A partial index on `json_extract(metadata, '$.intercepted')`.** Rejected because its migration scans every pair, 2.9 s at 85,000 pairs.

**Consequences:** an older binary that edits the `intercepted` key of a pair it did not capture leaves the count stale until a rebuild of `intercepted_count`, as the annotation index is left stale by its edits.
