# Cursor pages and base64 raw for traffic reads

The control API lists traffic newest-first with a UUID cursor and a hard limit, and it returns request/response raw bytes as base64. Offset pages drift while a service instance is still capturing pairs. Domain `RawField` JSON is a UTF-8 string, which is not the bytes the proxy stored; a client that inspects images needs the stored bytes back.

**Considered options:** unbounded `GetRequestResponseSummary`; offset pagination; JSON strings for raw. Rejected because a live project grows during a read, and a string marshal cannot round-trip binary bodies.
