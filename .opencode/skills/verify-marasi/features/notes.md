# Notes

Users attach a text note to one captured request/response pair, list pairs that have notes, and clear notes.

## Sub-features

- Set a note with `notes set UUID` from text, `--file`, or piped stdin.
- List noted pairs newest first with `notes list`.
- Page the list with `--limit` and `--cursor`.
- Clear a note with `notes clear UUID`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Send traffic through a running instance, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" notes set "$TRAFFIC_ID" "look here"`. List with `notes list` and clear with `notes clear "$TRAFFIC_ID"`.

## Driving it with shell and curl

Create one known proxied request and save `TRAFFIC_ID`. Run `notes set "$TRAFFIC_ID" "verify-note" --json` and require the same id plus that note. Run `notes list --json` and require an item with that id and note. The page is every noted pair in the open project, not a query for one id. Run `traffic metadata get "$TRAFFIC_ID" --json` and require `has_note` equal to the number `1`, not boolean `true`. Run `notes clear "$TRAFFIC_ID" --json`, then `notes list --json`, and require no item with that id.

## Gotchas

- Set requires exactly one of trailing text, `--file`, or piped stdin, and the note must be non-empty. A TTY stdin fails.
- `notes set` on a missing request id returns not found. Invalid UUIDs fail before repository lookup.
- Clearing a pair with no note returns not found.
- List pages newest remaining noted rows. Human list writes `next_cursor=$NEXT_CURSOR` to stderr; JSON keeps it in `next_cursor`.
- `has_note` in metadata is the JSON number `1` after a note is set. It is not boolean `true`.
- Notes belong to the currently open project. After opening a different project, previous notes are not visible. Opening the current path does not hide them.
- Set publishes `note.updated`. Clear publishes `note.deleted`. Subscribe with `events` before the mutation; the stream has no replay.
