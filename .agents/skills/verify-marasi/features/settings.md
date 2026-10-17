# Client settings

Clients store JSON values in the open project under their own client name and a key, such as `marasi-app` and `ledger.exclusion`. Marasi keeps them but never reads their meaning (ADR-0037). The `settings` command manages them over the `/client-settings` routes.

## Sub-features

- List a client's settings with `settings list CLIENT [--prefix P]` (`GET /client-settings/{client}`).
- Read one value with `settings get CLIENT KEY` (`GET /client-settings/{client}/{key}`).
- Write one value with `settings set CLIENT KEY JSON`, `--file PATH`, or piped stdin (`PUT /client-settings/{client}/{key}` with `{"value":…}`).
- Delete one with `settings delete CLIENT KEY` (`DELETE /client-settings/{client}/{key}`).
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service, then run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" settings set my-client theme '"dark"'` and `settings get my-client theme`.

## Driving it with shell and curl

Run `settings set cli-test 'ledger/view one' '{"columns": ["host"]}' </dev/null` and require `setting cli-test ledger/view one set` on stderr. `settings get cli-test 'ledger/view one'` prints the compact value `{"columns":["host"]}`; with `--json` it prints the stored object with `client`, `key`, `value`, and `updated_at`. Set a second key from stdin, `printf 1 | settings set cli-test other`. `settings list cli-test --prefix ledger/` prints only `ledger/view one  {"columns":["host"]}`, and `settings list cli-test --json` has both items. `settings delete cli-test 'ledger/view one'` prints `setting cli-test ledger/view one deleted`, and a later get exits 1 with `getting client setting: not_found`.

## Gotchas

- Keys may hold `/` and spaces; the CLI escapes them into the path.
- The value is checked as JSON before any request, so quote strings: `'"dark"'`. Pass exactly one of the JSON argument, `--file`, or piped stdin; anything else fails before any request with `settings set requires exactly one of text, --file, or piped stdin`. A TTY or `/dev/null` stdin is not input, so run the JSON-argument and `--file` forms with `</dev/null` from an agent shell.
- The client name `marasi` is reserved: `settings set marasi k 1 --json` is `{"error":"setting client setting: reserved_client"}`. A bad client name or key, or a value over 1 MiB, is `invalid_client_setting`.
- Human list shortens long values; use `settings get` for the full value.
- Settings belong to the open project. Without one, every command fails with `not_found`.
