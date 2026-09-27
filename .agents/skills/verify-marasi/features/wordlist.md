# Wordlist

Users add wordlist files to a running instance, preview their entries, and remove them. Armory runs refer to these files by basename.

## Sub-features

- List wordlists by name with `wordlist list`.
- Preview entries with `wordlist preview NAME`.
- Limit preview size with `--limit`.
- Move a source file into the wordlists directory with `wordlist add PATH`.
- Remove a wordlist with `wordlist remove NAME`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" wordlist add "$SOURCE_FILE"`. List, preview, and remove use the same global flags.

## Driving it with shell and curl

Run `wordlist list --json` and require `{"items":[]}`. Write a source file outside `$VERIFY_CONFIG_DIR/wordlists` whose basename is `users.txt` and whose lines are `alpha`, `beta`, and `gamma`. Run `wordlist add "$SOURCE_FILE" --json` and require an item whose `name` is `users.txt`. Require the source path to be gone and `$VERIFY_CONFIG_DIR/wordlists/users.txt` to exist. Run `wordlist preview users.txt --json` and require those three lines. Run `wordlist preview users.txt --limit 1 --json` and require only `alpha`. Duplicate add of the same basename must fail. Run `wordlist remove users.txt --json`, then `wordlist list --json`, and require `{"items":[]}`.

## Gotchas

- `wordlist add` moves the source file. The path must be a regular file outside the wordlists directory. The stored name is the basename.
- List is ordered by name. Preview default `--limit` is 20. The server rejects values outside 1 to 100.
- Add and remove publish `wordlist.added` and `wordlist.removed`. Subscribe with `events` before the mutation; the stream has no replay.
- Wordlists live under the config directory, not the open project. `project open` does not hide them.
- Human add and remove print a confirmation to stderr. `--json` add and remove print the full list.
