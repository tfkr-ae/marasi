# `--json` on every command, start first

The CLI is used by agents as well as people at a terminal. Every command should accept `--json`. This work adds it to `service start` only. `service stop` stays silent until its own change.

Start `--json` cannot reuse ADR-0002's passthrough rule, because start has no HTTP body. The start object is `instance` and `proxy_listener` on stdout.
