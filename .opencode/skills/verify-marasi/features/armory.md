# Armory

Users store raw HTTP templates in the open project, create runs against those templates with named wordlists, and fire the generated requests through the active proxy listener.

## Sub-features

- Create a template with `armory template create --name` and optional `--description` / `--raw-file`.
- List templates oldest first with `armory template list`.
- Read one template with `armory template get`.
- Change name, description, or raw template with `armory template update`.
- Delete a template with `armory template delete`.
- Validate a raw template and attack type with `armory run validate`.
- Create a run with `armory run create --template` and `--attack-type`.
- List runs for one template with `armory run list --template`.
- Read one run with `armory run get`.
- Start a draft run with `armory run start`.
- Cancel an in-progress run with `armory run cancel`.
- List a run's captured traffic with `armory run traffic`.
- Delete a run with `armory run delete`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Put wordlist files in `$VERIFY_CONFIG_DIR/wordlists`, or move them there with `wordlist add`. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" armory template create --name Lifecycle --raw-file request.raw`. Create a run from that template id, then `armory run start`.

## Driving it with shell and curl

Write `$VERIFY_CONFIG_DIR/wordlists/users.txt` with one line `payload`. Write a raw template that is a complete request, including the header-terminating blank line: `GET /armory-@@x@@.txt HTTP/1.1`, a `Host` of the local origin, and a blank line. Use CRLF. Run `armory template create --name Lifecycle --description first --raw-file "$RAW" --json` and save `id`. Run `armory template list --json` and require that id. Run `armory template update "$TEMPLATE_ID" --description replay --json`. Pipe the same raw bytes to `armory run validate --attack-type harpoon --wordlist users.txt --http --json` and require `{"status":"ok"}`. Run `armory run create --template "$TEMPLATE_ID" --attack-type harpoon --wordlist users.txt --http --json` and require `status` `draft`. Run `armory run list --template "$TEMPLATE_ID" --json` and `armory run get "$RUN_ID" --json`. Run `armory run start "$RUN_ID" --json`. Retry `armory run traffic "$RUN_ID" --json` until it contains `/armory-payload.txt`. Run `armory run get "$RUN_ID" --json` until `status` is `complete`. Run `armory run delete "$RUN_ID" --json`, then `armory template delete "$TEMPLATE_ID" --json`. To prove cancel, start a second run against a slow origin and call `armory run cancel` while `status` is `in_progress`.

## Gotchas

- Template `--name` is required and must be non-empty. Update requires `--name`, `--description`, or `--raw-file`.
- `--attack-type` is `harpoon`, `broadside`, `tandem`, or `maelstrom`. Matching ignores case. The stored value uses that spelling. `--wordlist` is repeatable and names files under `$VERIFY_CONFIG_DIR/wordlists`. The service creates that directory empty; it does not seed wordlist files. `wordlist add` moves a source file there.
- `--http` sends generated requests as HTTP. Omit it and Marasi uses HTTPS.
- Validate needs `--raw-file` or piped stdin. A TTY stdin fails. Create uses the stored template, not stdin. Validate and start reject a template that is not a complete HTTP request with a non-empty `Host` and a header-terminating blank line. A run also needs at least one `@@` payload position. Harpoon and broadside need exactly one wordlist.
- `armory run list` requires `--template`. Traffic listing accepts `--limit` and `--cursor`, but rows are oldest first. `traffic list` pages newest remaining rows.
- Start sends through the proxy listener and stamps `metadata.armory_run_id`. A one-payload harpoon can finish before you read `armory run get`. Retry get and traffic until the path appears.
- Cancel of a run that is not `in_progress` returns `run_not_active`. Cancel signals the active run and returns immediately. The JSON body may still show `in_progress`. Retry `armory run get` until status is no longer `in_progress`.
- Duplicate start of an already started run fails. Templates and runs belong to the currently open project.
- Template and run mutations publish `armory.template.created`, `armory.template.updated`, `armory.template.deleted`, `armory.run.created`, `armory.run.updated`, and `armory.run.deleted`. Subscribe with `events` before the mutation; the stream has no replay.
