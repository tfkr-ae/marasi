# Proxy logs

Users page the running instance's proxy log entries and continue with a cursor.

## Sub-features

- List the newest page with `logs`.
- Limit page size with `--limit` and continue with `--cursor`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service and send traffic first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" logs`. Add `--limit` or `--cursor` when paging a busy instance.

## Driving it with shell and curl

Send one proxied request so the log has rows. Run `logs --json` and require `items` non-empty with `timestamp`, `level`, and `message` on each entry. Run `logs --limit 1 --json` and require exactly one item. Copy `next_cursor` when present into `logs --cursor "$CURSOR" --json` and require the next older page. Human `logs` prints one `timestamp level message` line per entry with optional `request=` and `extension=` suffixes, oldest first within the page, and writes `next_cursor=` to stderr only when another page remains.

## Gotchas

- Pages are newest first. The service accepts `--limit` 1 through 500 and defaults to 200. `--cursor` must be a UUID.
- Log entries belong to the running instance. `project open` does not clear them.
- Human output prints the page oldest first. `--json` keeps the newest-first order with `next_cursor`.
- A missing log store returns not found. An unhealthy instance is not a log page.
