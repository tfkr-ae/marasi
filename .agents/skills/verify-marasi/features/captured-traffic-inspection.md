# Captured traffic inspection

Users list stored request/response pairs and open one pair to inspect its metadata and raw HTTP messages.

## Sub-features

- List the newest page of traffic with `traffic list`.
- Narrow the list with an AIP-160 query, `-q` or `--query`. `traffic list --help` lists the fields: exact-case `=`/`!=` with `*` at either end on `host`, `method`, `scheme`, `path`, and `content_type`; comparisons on `status_code`, `requested_at`, and `responded_at`; type-strict `metadata.<key>`; and case-insensitive `:` text search on `request_head`, `request_body`, `response_head`, `response_body`, `note`, and `metadata`, or bare text across all of them; and launchpad membership with `launchpad = "<launchpad id>"`, `launchpad != "<launchpad id>"` (which includes pairs in no launchpad), and `launchpad:*`; and sitemap subtrees with `sitemap = "<node name>"` and `sitemap != "<node name>"`, where an unknown name matches nothing. Combine with `AND`, `OR`, `NOT`, `-`, and parentheses.
- Limit page size and continue with `--cursor`.
- Read one pair by UUID with `traffic get "$TRAFFIC_ID"`. Its `--json` body has `prettified: {request, response}`, the proxy's prettified copies of the two messages, outside `metadata`.
- Read one pair's metadata with `traffic metadata get "$TRAFFIC_ID"`.
- Replace one pair's metadata with `traffic metadata update "$TRAFFIC_ID" --file` or piped stdin.
- Choose human-readable or `--json` output for `traffic list`, `traffic get`, and `traffic metadata update`. `traffic metadata get` always returns JSON, without `prettified-request` or `prettified-response`.

## How to get to it (user POV)

Send traffic through a running instance, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" traffic list`. Copy an ID from the list into `TRAFFIC_ID` and pass it to `traffic get`. Add list flags when narrowing a busy project.

## Driving it with shell and curl

Send two distinct proxied requests, for example `GET /proof.txt` returning 200 and `POST /other` returning 404. Run `traffic list -q "host = \"$HOST\" AND method = \"GET\" AND path = \"/proof*\" AND status_code = 200" --limit 1 --json` and require that one row. A `host` clause that omits the origin port, and a method/status pair that matches nothing, must return empty `items`. A query naming an unknown field, such as `-q 'stauts = 200'`, must fail with `invalid_query` and a position. A text query on part of the origin body, in other case, such as `-q 'response_body:"<FRAGMENT>"'`, must return the GET row, and the same fragment as `request_body:` must not. A text term under three characters, such as `-q '"ab"'`, must fail with `invalid_query`. `traffic list --limit 1 --json` is the newest row. Pass its `next_cursor` to a second `--limit 1` page and require the older row and `next_cursor` null. Run `traffic get "$TRAFFIC_ID" --json` and base64-decode `request.raw` and `response.raw`. Require the method, path, status, and origin body inside those bytes. For an origin that answers `{"b":2,"a":1}` as `application/json`, `prettified.response` is the status line and headers followed by the indented JSON with sorted keys, and `metadata` has neither `prettified-request` nor `prettified-response`. A copy is `null` when the body cannot be prettified, for example plain text or an empty body. `traffic metadata get` on a fresh row is `{}`. `traffic metadata update --file` with `{"phase":"two","has_note":false,"prettified-request":"client","prettified-response":"client"}`, then get again, must return only `{"phase":"two"}`. After that update, `-q 'metadata:"phase"'` must return that row, and `-q '"client"'` must not, because the prettified keys are never indexed.

## Gotchas

- Each page is the newest remaining rows, oldest first within the page. `--limit 1` is still the newest match. `next_cursor` is the oldest id of the current page when an older page remains, otherwise `null` (JSON) or absent (human stderr). Never assume a stable UUID or timestamp.
- `host = "..."` is exact, keeps case, and may include the origin port for a non-default port. A `*` at the start or end matches anything there.
- `path = "/proof*"` matches a path prefix. `status_code` also takes `<`, `<=`, `>`, and `>=`. The removed `--host`, `--method`, `--path`, and `--status-code` flags are unknown flags.
- Human output writes `next_cursor=$NEXT_CURSOR` to stderr. JSON keeps it in `next_cursor`.
- Every `traffic list` page has `"index":{"complete":bool}`. While an opened project's older pairs are still being indexed in the background it is `false`, text conditions can miss those pairs, and human output adds a `notice:` line on stderr. A new project, or one fully indexed, is `true`.
- The sitemap has no CLI command. Read it with `curl` at `GET /traffic/sitemap?node=&q=&exclude=&as_of=&cursor=&limit=`. An empty `node` lists the origins (`scheme://host[:port]`, lowercased). Each item has `name`, `segment`, `pairs`, `errors` (5xx responses), and `has_children`, ordered by `pairs` highest first, `limit` 1 through 500 (default 100). Counts leave out the `exclude` types, and an item with no pairs left is not listed. Decimal ids, UUIDs, and 32/40/64-character hex collapse to `{int}`, `{uuid}`, and `{hash}`, so `/users/42` is under `.../users/{int}`. An unknown `node` is 404. The body has `"sitemap":{"complete":bool}`; a `traffic list` query with a `sitemap` condition carries the same object. With `q` (even empty) the read is in query mode: `as_of` is required (400 `bad_request` without it, and for `as_of` without `q`), and the counts are computed exactly over the pairs matching `q` and `exclude` with an id at or below `as_of`, so a pair captured later never changes them. The read stops when the client disconnects.
- A project captured by a Marasi from before ADR-0036 kept prettified copies inside metadata. On open, the `prettified_move` background job moves them out, newest pairs first, and `GET /jobs` lists it. `traffic get` returns the copies in `prettified` whether or not the pair has moved yet, and `traffic metadata get` never shows them.
- `traffic get` requires a UUID. Invalid IDs fail before repository lookup.
- `traffic metadata update` requires `--file` or piped stdin with a non-empty JSON object body. A TTY stdin fails.
- `--limit` must be 1 through 500. The default is 200.
- Metadata update replaces client-owned keys. It drops a submitted `has_note`, `prettified-request`, or `prettified-response`, leaves the pair's prettified copies unchanged because they are not metadata, and sets `has_note` to JSON boolean `true` only when a note already exists. `notes set` is a different writer: it stores `has_note` as the JSON number `1`. `traffic metadata get` omits `prettified-request` and `prettified-response`. It does not add `has_note` when no note exists.
