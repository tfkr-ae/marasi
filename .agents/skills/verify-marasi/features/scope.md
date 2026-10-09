# Scope check

Users test whether a URL falls inside the running instance's scope without sending traffic. The verdict comes from the live in-memory scope rules. A fresh project loads the compass extension, which clears rules and installs excludes, so the instance is not allow-all. There is no `scope add` or `scope remove` command. Custom Lua can change rules and the default policy through `marasi:scope()`: `extension update` executes source immediately, and `extension call` can invoke a function that changes them. Replacing settings alone does not add rules.

## Sub-features

- Check one URL with `scope check <url>`.
- Read the verdict, the matched rule, and the compass flag with human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" scope check "example.com/path?q=1"`. The positional URL is sent verbatim; the service trims whitespace and prepends `https://` when no scheme is present.

## Driving it with shell and curl

Run `scope check "example.com/path?q=1"` and require `tested_url: https://example.com/path?q=1`, `in_scope: true`, `compass_enabled: true`, and no `rule:` or `match_type:` lines. Run `scope check "https://example.com/path?q=1" --json` and require `in_scope` true, that `tested_url`, `rule` null, and `compass_enabled` true. Run `scope check "www.gstatic.com" --json` and require `in_scope` false, `tested_url` `https://www.gstatic.com`, `rule.pattern` `.*\.gstatic\.com`, `rule.match_type` `host`, and `compass_enabled` true. Run `scope check ""` and require failure with `checking scope: bad_request`.

## Gotchas

- A miss uses the live default policy. In a fresh project it allows: `in_scope` true with `rule` null. Custom Lua can set `marasi:scope():set_default_allow(false)`, making an unmatched URL return false with `rule` null. Compass has already installed excludes by the time the first check runs, so `www.gstatic.com` and `www.googleapis.com` are out of scope. `example.com` still misses and allows under the fresh-project policy.
- `tested_url` is the normalized URL, not the input. A schemeless input gains `https://`; surrounding whitespace is trimmed.
- When no rule matches, JSON `rule` is `null` and human output omits both the `rule:` and `match_type:` lines.
- `compass_enabled: false` does not disable Scope check evaluation: the verdict still applies the live rules. Disabling Compass skips its traffic-processing hooks; the check is not proof that Compass will filter a proxied request.
- `scope check` never mutates rules and publishes no event.
- Bare `scope` requires the `check` subcommand. `scope check` takes exactly one positional URL; there is no `--url` flag.
- An empty or unparseable URL fails with `checking scope: bad_request`. A missing instance fails with `instance $VERIFY_INSTANCE is not running`.
