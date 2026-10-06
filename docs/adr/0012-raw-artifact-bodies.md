# Raw artifact bodies, JSON metadata

Traffic raw is base64 inside JSON so `--json` can passthrough one object (ADR-0001). Artifacts can be 20MB, and encoding that is wasted size plus a full parse of a 27MB string. Upload is a raw HTTP body and returns JSON metadata. `GET` of an artifact is JSON metadata. The stored bytes are a separate content GET. CLI `--json` on download is an error. Use get for metadata. Download writes a file.

**Considered options:** base64 JSON both ways; multipart; `--json` download returning a URL; `--json` download as a CLI-made path object. Rejected because 20MB base64 is ceremony, multipart is worse in the CLI, a Unix-socket control listener has no fetchable URL, and two download encodings would split the contract.
