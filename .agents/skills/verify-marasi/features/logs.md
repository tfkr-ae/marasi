# Proxy logs

Users page the running instance's proxy log entries and continue with a cursor.

## Sub-features

- List the newest page with `logs`.
- Limit page size with `--limit` and continue with `--cursor`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. A proxied request does not by itself add a log row. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" logs` after start, or after `listener start` / `listener update`, which each write a startup line.

## Driving it with shell and curl

After start, `logs --json` must contain an `INFO` item whose `message` is `Marasi Service Started on $PROXY_LISTENER`. Send one proxied request and require that its traffic id is not a `request_id` on that page. `logs --limit 1 --json` on a fresh project is that one startup item and `next_cursor` is null. To page, `listener update --address 127.0.0.1 --port 0`, then `logs --limit 1 --json` is the new startup line and `next_cursor` is set. `logs --cursor "$CURSOR" --limit 1 --json` is the older startup line, not the curl. Human `logs` prints one `timestamp level message` line per entry with optional `request=` and `extension=` suffixes, oldest first within the page, and writes `next_cursor=` to stderr only when another page remains.

## Gotchas

- A successful proxy does not insert a proxy log and does not set `request_id`. The startup line is written when the listener binds. Another row needs another `GetListener`, such as `listener start` or `listener update`, not another curl.
- Pages are newest id first. The service accepts `--limit` 1 through 500 and defaults to 200. `--cursor` must be a UUID.
- Log entries belong to the currently open project file. `project open` to a different project switches the log view; reopening the same path keeps its rows.
- Human output prints the page oldest first. `--json` keeps the newest-first order with `next_cursor`.
- A missing log store returns not found. An unhealthy instance is not a log page.
