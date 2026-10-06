# Metadata writes replace the document

A client that highlights a request/response pair needs to write metadata without a key-by-key CLI. Merge would let `{"highlight":"red"}` keep launchpad and intercept keys, but then omitting a key could not delete it, and get-then-put would never shrink the document. Replace matches Lua `set_metadata` and the repository `UpdateMetadata`. After replace, the server copies `prettified-request` and `prettified-response` back from the previous row if they existed, and recomputes `has_note` from the notes table, so a round trip cannot drop proxy-owned body copies or lie about whether a note exists.

**Considered options:** merge keys with null-to-delete; a highlight-specific command; replace with no overlay. Rejected because merge and get-then-put fight, highlight is not a domain term, and a literal replace would delete prettified bodies and desync `has_note`.
