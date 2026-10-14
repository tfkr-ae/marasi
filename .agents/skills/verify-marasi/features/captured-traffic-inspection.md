# Captured traffic inspection

Users list stored request/response pairs and open one pair to inspect its metadata and raw HTTP messages.

## Sub-features

- List the newest page of traffic with `traffic list`.
- Narrow the list with an AIP-160 query, `-q` or `--query`. `traffic list --help` lists the fields: exact-case `=`/`!=` with `*` at either end on `host`, `method`, `scheme`, `path`, and `content_type`; comparisons on `status_code`, `requested_at`, and `responded_at`; type-strict `metadata.<key>`, and `metadata.<key>:*` for pairs that have the key with any value, including `null`; and case-insensitive `:` text search on `request_head`, `request_body`, `response_head`, `response_body`, `note`, and `metadata`, or bare text across all of them; and launchpad membership with `launchpad = "<launchpad id>"`, `launchpad != "<launchpad id>"` (which includes pairs in no launchpad), and `launchpad:*`. Combine with `AND`, `OR`, `NOT`, `-`, and parentheses.
- Limit page size and continue with `--cursor`.
- Read one pair by UUID with `traffic get "$TRAFFIC_ID"`.
- Read one pair's metadata with `traffic metadata get "$TRAFFIC_ID"`.
- Replace one pair's metadata with `traffic metadata update "$TRAFFIC_ID" --file` or piped stdin.
- Set or clear one metadata key on a selection with `POST /traffic/bulk/metadata` (no CLI): `{"selection":…,"key":"a.b","value":<any JSON>}` or `{"selection":…,"key":"a.b","clear":true}`. It returns `{"count":n}`, counting only pairs whose metadata changed, and leaves every other key as it was.
- Choose human-readable or `--json` output for `traffic list`, `traffic get`, and `traffic metadata update`. `traffic metadata get` always returns JSON, without `prettified-request` or `prettified-response`.

## How to get to it (user POV)

Send traffic through a running instance, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" traffic list`. Copy an ID from the list into `TRAFFIC_ID` and pass it to `traffic get`. Add list flags when narrowing a busy project.

## Driving it with shell and curl

Send two distinct proxied requests, for example `GET /proof.txt` returning 200 and `POST /other` returning 404. Run `traffic list -q "host = \"$HOST\" AND method = \"GET\" AND path = \"/proof*\" AND status_code = 200" --limit 1 --json` and require that one row. A `host` clause that omits the origin port, and a method/status pair that matches nothing, must return empty `items`. A query naming an unknown field, such as `-q 'stauts = 200'`, must fail with `invalid_query` and a position. A text query on part of the origin body, in other case, such as `-q 'response_body:"<FRAGMENT>"'`, must return the GET row, and the same fragment as `request_body:` must not. A text term under three characters, such as `-q '"ab"'`, must fail with `invalid_query`. `traffic list --limit 1 --json` is the newest row. Pass its `next_cursor` to a second `--limit 1` page and require the older row and `next_cursor` null. Run `traffic get "$TRAFFIC_ID" --json` and base64-decode `request.raw` and `response.raw`. Require the method, path, status, and origin body inside those bytes. `traffic metadata get` on a fresh row is `{}`. `traffic metadata update --file` with `{"phase":"two","has_note":false,"prettified-request":"client","prettified-response":"client"}`, then get again, must return only `{"phase":"two"}`. After that update, `-q 'metadata:"phase"'` must return that row, and `-q '"client"'` must not, because the prettified keys are never indexed.

## Gotchas

- Each page is the newest remaining rows, oldest first within the page. `--limit 1` is still the newest match. `next_cursor` is the oldest id of the current page when an older page remains, otherwise `null` (JSON) or absent (human stderr). Never assume a stable UUID or timestamp.
- `host = "..."` is exact, keeps case, and may include the origin port for a non-default port. A `*` at the start or end matches anything there.
- `path = "/proof*"` matches a path prefix. `status_code` also takes `<`, `<=`, `>`, and `>=`. The removed `--host`, `--method`, `--path`, and `--status-code` flags are unknown flags.
- Human output writes `next_cursor=$NEXT_CURSOR` to stderr. JSON keeps it in `next_cursor`.
- Every `traffic list` page has `"index":{"complete":bool}`. While an opened project's older pairs are still being indexed in the background it is `false`, text conditions can miss those pairs, and human output adds a `notice:` line on stderr. A new project, or one fully indexed, is `true`.
- `traffic get` requires a UUID. Invalid IDs fail before repository lookup.
- `traffic metadata update` requires `--file` or piped stdin with a non-empty JSON object body. A TTY stdin fails.
- `--limit` must be 1 through 500. The default is 200.
- Metadata update replaces client-owned keys. It drops a submitted `has_note`, `prettified-request`, or `prettified-response`, keeps any previous prettified bodies in storage, and sets `has_note` to JSON boolean `true` only when a note already exists. `notes set` is a different writer: it stores `has_note` as the JSON number `1`. `traffic metadata get` omits `prettified-request` and `prettified-response`. It does not add `has_note` when no note exists.
- Bulk metadata keys are dotted like query keys (`a.b` is `b` inside `a`); a missing parent object is created, and a parent that is not an object is left alone and not counted. A key with an empty segment, `"` or `\`, or a top-level `prettified-request`, `prettified-response` or `has_note`, is a 400 `{"error":"invalid_metadata_key","message":…}` naming it. Missing `key`, or not exactly one of `value` and `"clear":true`, is 400 `invalid_bulk_request`. `"value":null` sets a JSON null.
- A response merges its metadata into the stored metadata (RFC 7396 `json_patch`) instead of replacing it, so a key set while the pair was in flight survives the response. A key cleared while the pair was in flight comes back if the response-time context still has it.
