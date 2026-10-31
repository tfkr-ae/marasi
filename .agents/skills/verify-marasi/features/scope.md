# Scope check

Users list the running instance's scope rules and test whether a URL falls inside that scope without sending traffic. Both read the live in-memory scope rules. A fresh project loads the compass extension, which clears rules and installs excludes, so the instance is not allow-all. There is no `scope add` or `scope remove` command. Custom Lua can change rules and the default policy through `marasi:scope()`: `extension update` executes source immediately, and `extension call` can invoke a function that changes them. Replacing settings alone does not add rules.

## Sub-features

- List the include and exclude rules and the default policy with `scope list`.
- Check one URL with `scope check <url>`.
- Read the verdict, the matched rule, and the compass flag with human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" scope list` to read the rules. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" scope check "example.com/path?q=1"` to test one URL. The positional URL is sent verbatim; the service trims whitespace and prepends `https://` when no scheme is present.

## Driving it with shell and curl

Run `scope list --json` on a fresh project and require `default_allow` true, `include` `[]`, and nine `exclude` items from Compass, sorted by `pattern` then `match_type`: first `.*GetAsyncData.*` (`url`), last `android\.clients\.google\.com` (`host`). Run it three times and require byte-identical output. Run `scope list` and require `default_allow: true`, then a table whose first and last lines are `RULE  MATCH TYPE  PATTERN` (aligned) and whose rows read `exclude  <match type>  <pattern>`. To prove includes and the default policy, run `extension update` on the workshop extension with Lua that calls `marasi:scope()` `add_rule('zeta\\.example', 'url')`, `add_rule('target\\.example', 'host')`, `add_rule('target\\.example', 'url')`, `add_rule('-blocked\\.target\\.example', 'host')` and `set_default_allow(false)`. Then `scope list --json` requires `default_allow` false, `include` in the order `target\.example`/`host`, `target\.example`/`url`, `zeta\.example`/`url`, and `blocked\.target\.example`/`host` as the last exclude. Human rows list includes before excludes. `scope check https://blocked.target.example/x` must then name that exclude.

Run `scope check "example.com/path?q=1"` and require `tested_url: https://example.com/path?q=1`, `in_scope: true`, `compass_enabled: true`, and no `rule:` or `match_type:` lines. Run `scope check "https://example.com/path?q=1" --json` and require `in_scope` true, that `tested_url`, `rule` null, and `compass_enabled` true. Run `scope check "www.gstatic.com" --json` and require `in_scope` false, `tested_url` `https://www.gstatic.com`, `rule.pattern` `.*\.gstatic\.com`, `rule.match_type` `host`, and `compass_enabled` true. The default Compass rules also exclude Chrome background traffic that no Chrome switch stops. Run `scope check --json` on `https://android.clients.google.com/c2dm/register3` and require `in_scope` false with `rule.pattern` `android\.clients\.google\.com` and `match_type` `host`. Run it on `https://accounts.google.com/ListAccounts?gpsia=1&source=ChromiumBrowser` and require false with `accounts\.google\.com\/ListAccounts` and `match_type` `url`. Run it on `https://edgedl.me.gvt1.com/edgedl/x` and require false with `.*\.gvt1\.com` and `match_type` `host`. Run it on `https://accounts.google.com/signin/v2/identifier` and require `in_scope` true with `rule` null, because only the `ListAccounts` path is excluded. Run `scope check ""` and require failure with `checking scope: bad_request`.

## Gotchas

- A miss uses the live default policy. In a fresh project it allows: `in_scope` true with `rule` null. Custom Lua can set `marasi:scope():set_default_allow(false)`, making an unmatched URL return false with `rule` null. Compass has already installed excludes by the time the first check runs, so `www.gstatic.com` and `www.googleapis.com` are out of scope. `example.com` still misses and allows under the fresh-project policy.
- The Compass defaults are seeded in the first migration, so only a project created by this build has the `android.clients.google.com`, `ListAccounts`, and `gvt1.com` excludes. A project created by an older build keeps its old rules. Prove them in a fresh `--project-name`.
- JSON escapes `&` in `tested_url` as `\u0026`. Decode the JSON before comparing.
- `tested_url` is the normalized URL, not the input. A schemeless input gains `https://`; surrounding whitespace is trimmed.
- When no rule matches, JSON `rule` is `null` and human output omits both the `rule:` and `match_type:` lines.
- `compass_enabled: false` does not disable Scope check evaluation: the verdict still applies the live rules. Disabling Compass skips its traffic-processing hooks; the check is not proof that Compass will filter a proxied request.
- `scope check` and `scope list` never mutate rules and publish no event. There is still no `scope add`.
- `scope list` answers the live rules when Compass is disabled too; disabling Compass leaves the rules in place.
- An empty `scope list` table prints only the `default_allow:` line. `--json` keeps `include` and `exclude` as `[]`, never `null`.
- The list's order is fixed; the rule `scope check` names is not (`docs/adr/2026-10-06-scope-check-does-not-stabilize-the-named-rule.md`).
- Bare `scope` requires the `check` or `list` subcommand. `scope list` takes no arguments. `scope check` takes exactly one positional URL; there is no `--url` flag.
- An empty or unparseable URL fails with `checking scope: bad_request`. A missing instance fails with `instance $VERIFY_INSTANCE is not running`.
