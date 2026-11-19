# Compressed responses are transparent on the wire and decoded in storage

Marasi decoded a response whose `Content-Encoding` is exactly `gzip` or `br`, removed `Content-Encoding`, and sent the decoded body to the client, so the client never received what the origin sent. Now the client receives the origin's encoded bytes and headers unchanged. The stored pair, Checkpoint and extensions still see the body decoded with `Content-Encoding` removed, as before. The pair records the coding Marasi undid in `metadata.decoded_content_encoding`, with the wire length, so a tester knows the client received the encoded form. An edit that changes the decoded body is sent decoded, with `Content-Encoding` removed and the body re-framed ([Marasi owns its HTTP/1.1 wire format](2026-11-17-marasi-owns-its-http1-wire-format.md)). Every other coding, including `x-gzip`, `deflate`, `zstd`, stacked codings and a different case, is stored and shown as received, as before.

## Considered options

- **Decode for the client too (the old behaviour).** Rejected because the client receives a body the origin did not send, without its `Content-Encoding`, which breaks transparency.
- **Store the encoded wire bytes.** Rejected because every reader of a stored body assumes it is readable, and each would have to decode it: marasi-app's body viewer, the CLI's traffic output, report export, prettified bodies ([Prettified bodies leave metadata](2026-10-14-prettified-bodies-leave-metadata.md)), and extensions and Checkpoint editing the body. Only the search index already decodes stored bodies. New pairs would also look different from the pairs already stored. Storing the wire bytes stays out of scope until those readers decode.
- **Widen the set of decoded codings.** Out of scope; new pairs would look different from old ones in the app and the CLI.

**Consequences:** the stored body is not the wire body for these responses; `metadata.decoded_content_encoding` is the record of that. Before [The proxy adds no headers to a request](2026-10-19-the-proxy-adds-no-headers-to-a-request.md), Go's transport asked for gzip and decoded it out of sight; now the coding the origin chose reaches the client.
