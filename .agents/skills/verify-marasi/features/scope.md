# Scope check

Users test whether a URL falls inside the running instance's scope without sending traffic. The verdict comes from the live in-memory scope rules. A fresh project loads the compass extension, which clears rules and installs excludes, so the instance is not allow-all. There is no `scope add` or `scope remove` command. `extension call` and settings cannot add a rule. `extension update` can, because the new Lua runs immediately and any extension can call `marasi:scope()`.

## Sub-features

- Check one URL with `scope check <url>`.
- Read the verdict, the matched rule, and the compass flag with human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" scope check "example.com/path?q=1"`. The positional URL is sent verbatim; the service trims whitespace and prepends `https://` when no scheme is present.

## Driving it with shell and curl

Run `scope check "example.com/path?q=1"` and require `tested_url: https://example.com/path?q=1`, `in_scope: true`, `compass_enabled: true`, and no `rule:` or `match_type:` lines. Run `scope check "https://example.com/path?q=1" --json` and require `in_scope` true, that `tested_url`, `rule` null, and `compass_enabled` true. Run `scope check "www.gstatic.com" --json` and require `in_scope` false, `tested_url` `https://www.gstatic.com`, `rule.pattern` `.*\.gstatic\.com`, `rule.match_type` `host`, and `compass_enabled` true. Run `scope check ""` and require failure with `checking scope: bad_request`.

## Gotchas

- A miss is allow-all: `in_scope` true with `rule` null. Compass has already installed excludes by the time the first check runs, so `www.gstatic.com` and `www.googleapis.com` are out of scope. `example.com` still misses and allows.
- `tested_url` is the normalized URL, not the input. A schemeless input gains `https://`; surrounding whitespace is trimmed.
- When no rule matches, JSON `rule` is `null` and human output omits both the `rule:` and `match_type:` lines.
- `compass_enabled: false` does not disable filtering. The verdict still applies the scope rules.
- `scope check` never mutates rules and publishes no event.
- Bare `scope` requires the `check` subcommand. `scope check` takes exactly one positional URL; there is no `--url` flag.
- An empty or unparseable URL fails with `checking scope: bad_request`. A missing instance fails with `instance $VERIFY_INSTANCE is not running`.
