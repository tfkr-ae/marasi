# Prettified bodies leave metadata

The proxy's `prettified-request` and `prettified-response` copies move out of a pair's metadata into their own table. Inside the metadata JSON they made every list row run `json_remove`, made every `metadata.<key>` condition parse whole bodies, and pushed the timestamp columns onto overflow pages. A resumable background job moves existing values, and reads look in both places until it finishes. Reads still return the prettified bodies, because marasi-app shows them behind a prettify toggle, but no longer inside metadata, and the metadata overlay of [Metadata writes replace the document](2026-10-06-metadata-replace-with-overlay.md) no longer needs to copy them back.

This is an interim home. Prettifying may become a client concern, and then Marasi would stop storing these copies.
