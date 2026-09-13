# Global CLI JSON and human output contract

`--json` is a persistent root option, so every executable command accepts it before or after its subcommands. Help and command groups remain human-readable. If an invocation is too malformed for Cobra to recognize `--json`, Cobra also retains its human error output.

Once Cobra recognizes JSON mode, an executed command writes exactly one JSON value to stdout, writes nothing to stderr, and exits with status 0 on success or 1 on failure. CLI-created values are compact and end with one newline. Every CLI-owned failure has the shape `{"error":"message"}`. Control API failures use the same shape: a valid string `error` keeps the attempted operation and API message, while a missing or invalid API error falls back to the operation and HTTP status.

Success values keep the useful representation of each command. `service start` returns `instance` and `proxy_listener`. `service stop` returns `instance` and `status` set to `stopped`, but only after ownership cleanup finishes. Successful traffic reads write the control API body byte-for-byte, so the API body controls their whitespace and trailing newline.

Human output remains the default. Start success and all errors stay on stderr, while traffic keeps its terminal projection. Stop success writes `instance <name> stopped successfully` with a trailing newline to stderr after ownership cleanup finishes.

This decision supersedes ADR-0002's traffic-error passthrough and silent-stop rules, ADR-0004's JSON start-error rule and silent-stop rule, and ADR-0006's staged restriction of `--json` to `service start`.

**Considered options:** command-local JSON flags; passing control API errors through unchanged; keeping successful stop silent. Rejected because callers would still need command-specific flags, streams, and result handling.
