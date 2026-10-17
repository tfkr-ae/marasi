# The search index splits annotations from bodies

Notes and metadata get their own small contentless FTS5 index, keyed through `traffic_index_key` like the body index. Editing a note or metadata then never re-tokenises request and response bodies. A query still spans both indexes: the translator unions them for bare text and `OR`. Existing body rows still hold note and metadata text, so body-index lookups use a column filter on the head and body columns, and writes put NULL in the old columns. Only the new small index is backfilled, in the background. Bodies are decoded for indexing from the stored raw bytes, for every content encoding and for request bodies too, up to a decoded size cap. Storage keeps the bytes as captured. Pairs already indexed are re-decoded only by an explicit rebuild of the search index.

**Considered options:** re-index every pair into a new two-index layout during a migration. Rejected because it re-tokenises every body, which takes hours on a large project.
