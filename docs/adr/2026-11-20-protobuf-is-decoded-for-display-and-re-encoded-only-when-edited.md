# Protobuf is decoded for display and re-encoded only when edited

A gRPC, gRPC-Web or Connect stream message carrying protobuf is shown and edited in a text form compatible with Google's protoscope language, decoded without a schema from the stored bytes and never stored itself. An unedited message goes out as the bytes it came in. An edited message is encoded from the text form, which states every field's wire type and can express non-minimal varints, field order and split repeated fields, so the text form round-trips byte for byte. The codec is Marasi's own, in the Go standard library, not the protoscope module.

We decided this because protobuf has no single encoding of a value, and a schema-less decode is a guess: a length-delimited field may be a string, bytes, a nested message or a packed repeated field. Re-encoding every message from a guessed type would change unedited traffic, which breaks transparency.

**Considered options:** show raw bytes only (editing hex is not the editing users have for WebSocket text); re-encode every message canonically from a guessed type (changes unedited messages); decode with a schema from `.proto` files or server reflection (a feature of its own with per-project schema storage, left as a follow-up that falls back to this decode).
