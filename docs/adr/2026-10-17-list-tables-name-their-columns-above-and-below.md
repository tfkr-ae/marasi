# List tables name their columns above and below the rows

A list command's human output is an aligned table with a row of column names both above and below the rows. A long list scrolls its header off the terminal, so the footer keeps the labels next to the last rows a person sees. An empty list prints nothing, because a header and footer around no rows says no more than silence does. Checkpoint, extension and project lists, which separated fields with one space, are aligned tables too, so every list reads the same way. The tables of `traffic sitemap` and `traffic types` follow the rule, and each sitemap level under its `# <node>` line gets its own header and footer.

Lists whose items are `key: value` blocks, such as `service list`, `armory template list` and `waypoint list`, already name every field and stay as they are. So do one-column lists, such as `chrome profile list`, and log lines from `logs` and `extension logs`.

Scripts read `--json` ([`--json` on every command, start first](2026-10-06-json-flag-for-agents.md), [Global CLI JSON and human output contract](2026-10-06-cli-output-contract.md)), so the extra lines change no machine contract. Text processing of human output must skip the first and last line.

**Considered options:** a header only; a header and footer only when stdout is a terminal. Rejected because the footer is the part that helps on a long list, and output that changes when piped is harder to predict and to test.
