# Captured traffic inspection

Users list stored request/response pairs and open one pair to inspect its metadata and raw HTTP messages.

## Sub-features

- List the newest page of traffic with `traffic list`.
- Filter by exact host, exact method, exact status code, or path prefix.
- Limit page size and continue with `--cursor`.
- Read one pair by UUID with `traffic get "$TRAFFIC_ID"`.
- Read one pair's metadata with `traffic metadata get "$TRAFFIC_ID"`.
- Replace one pair's metadata with `traffic metadata update "$TRAFFIC_ID" --file` or piped stdin.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Send traffic through a running instance, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" traffic list`. Copy an ID from the list into `TRAFFIC_ID` and pass it to `traffic get`. Add list flags when narrowing a busy project.

## Driving it with shell and curl

Create at least one known proxied request. Run `traffic list --path /proof.txt --status-code 200 --limit 1 --json`, require exactly the expected path and status, and save its UUID. Run `traffic get "$TRAFFIC_ID" --json`; require the same ID, `GET`, path, response status 200, and raw request and response data. Run `traffic metadata get "$TRAFFIC_ID" --json` and require the stored metadata object. Replace it with `traffic metadata update "$TRAFFIC_ID" --file "$META_FILE" --json`, then get again and require the new keys. For filter or pagination changes, create distinct requests and prove both matching and non-matching cases plus every returned cursor.

## Gotchas

- Each page is the newest remaining rows, oldest first within the page. `--limit 1` is still the newest match. `next_cursor` is the oldest id of the current page. Never assume a stable UUID or timestamp.
- `--host` is exact and may include the origin port for a non-default port.
- `--path` is a prefix filter, while method and status code are exact filters.
- Human output writes `next_cursor=$NEXT_CURSOR` to stderr. JSON keeps it in `next_cursor`.
- `traffic get` requires a UUID. Invalid IDs fail before repository lookup.
- `traffic metadata update` requires `--file` or piped stdin with a non-empty JSON object body. A TTY stdin fails.
- Metadata replaces the stored object. `traffic metadata get` returns the current object plus `has_note`.
