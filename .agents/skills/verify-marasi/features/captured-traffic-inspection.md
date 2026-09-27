# Captured traffic inspection

Users list stored request/response pairs and open one pair to inspect its metadata and raw HTTP messages.

## Sub-features

- List the newest page of traffic with `traffic list`.
- Filter by exact host, exact method, exact status code, or path prefix.
- Limit page size and continue with `--cursor`.
- Read one pair by UUID with `traffic get "$TRAFFIC_ID"`.
- Read one pair's metadata with `traffic metadata get "$TRAFFIC_ID"`.
- Replace one pair's metadata with `traffic metadata update "$TRAFFIC_ID" --file` or piped stdin.
- Choose human-readable or `--json` output for `traffic list`, `traffic get`, and `traffic metadata update`. `traffic metadata get` always returns the stored JSON object.

## How to get to it (user POV)

Send traffic through a running instance, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" traffic list`. Copy an ID from the list into `TRAFFIC_ID` and pass it to `traffic get`. Add list flags when narrowing a busy project.

## Driving it with shell and curl

Send two distinct proxied requests, for example `GET /proof.txt` returning 200 and `POST /other` returning 404. Run `traffic list --host "$HOST" --method GET --path /proof --status-code 200 --limit 1 --json` and require that one row. A host filter that omits the origin port, and a method/status pair that matches nothing, must return empty `items`. `traffic list --limit 1 --json` is the newest row. Pass its `next_cursor` to a second `--limit 1` page and require the older row and `next_cursor` null. Run `traffic get "$TRAFFIC_ID" --json` and base64-decode `request.raw` and `response.raw`. Require the method, path, status, and origin body inside those bytes. `traffic metadata get` on a fresh row is `{}`. `traffic metadata update --file` with `{"phase":"two","has_note":false,"prettified-request":"client","prettified-response":"client"}`, then get again, must return only `{"phase":"two"}`.

## Gotchas

- Each page is the newest remaining rows, oldest first within the page. `--limit 1` is still the newest match. `next_cursor` is the oldest id of the current page when an older page remains, otherwise `null` (JSON) or absent (human stderr). Never assume a stable UUID or timestamp.
- `--host` is exact and may include the origin port for a non-default port.
- `--path` is a prefix filter, while method and status code are exact filters.
- Human output writes `next_cursor=$NEXT_CURSOR` to stderr. JSON keeps it in `next_cursor`.
- `traffic get` requires a UUID. Invalid IDs fail before repository lookup.
- `traffic metadata update` requires `--file` or piped stdin with a non-empty JSON object body. A TTY stdin fails.
- `--limit` must be 1 through 500. The default is 200.
- Metadata update replaces client-owned keys. It drops a submitted `has_note`, `prettified-request`, or `prettified-response`, keeps any previous prettified bodies, and sets `has_note` to JSON boolean `true` only when a note already exists. `notes set` is a different writer: it stores `has_note` as the JSON number `1`. `traffic metadata get` returns the stored value. It does not add `has_note` when no note exists.
