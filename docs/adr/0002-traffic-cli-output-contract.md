# Human projection and JSON passthrough for traffic CLI

The traffic commands print a human projection by default, and `--json` writes the control API response body unchanged, including 4xx. `service stop` is silent because stop has nothing to inspect. JSON-only traffic would be unusable at a terminal, and a second JSON schema would drift from ADR-0001. A missing control listener has no HTTP body, so that error stays on stderr even with `--json`.

**Considered options:** JSON-only output, matching `service stop`; a CLI-specific JSON shape with decoded bodies. Rejected because a person reading traffic needs columns and raw HTTP text, and scripts should see the same bytes as any other control API client.
