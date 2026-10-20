# The highlight is a side table, not a request column

A pair's highlight is a number from 1 to 8 stored in `pair_highlight(request_id, highlight)`, a `WITHOUT ROWID` table that cascades on delete, rather than in a column on `request` or in metadata. A `request` row holds both raw bodies, so a column that grows from NULL to a value makes SQLite delete and reinsert the whole record: 10,000 first highlights on 21 KB rows took 1.65 s against a 500 ms budget. Metadata would also put the highlight in the search index and make each change re-index the pair. Marasi gives the number no meaning; the client owns each number's colour and label.

**Considered options:** a `highlight` column on `request`; a metadata key, as marasi-app wrote it before. Rejected for the write cost and the re-indexing respectively.
