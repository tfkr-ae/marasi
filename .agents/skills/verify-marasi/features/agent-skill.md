# Agent skill

Users print the agent skill file that ships with this build, so a coding agent can install the copy that matches the installed `marasi`.

## Sub-features

- Print the embedded skill with `skill`.

## How to get to it (user POV)

Run `dist/marasi skill > ~/.agents/skills/marasi/SKILL.md`. No service instance is needed.

## Driving it with shell and curl

Run `dist/marasi skill` and require exit zero and stdout byte-identical to `skills/marasi/SKILL.md` in the repository. Run it again with `--config-dir /nonexistent --instance ../bad` and require the same output, because the command never resolves an instance. Run `dist/marasi skill --json` and require exit 1 with stdout `{"error":"skill does not support --json"}`. Require `dist/marasi --help` to list `skill`.

## Gotchas

- The text is built into the binary from `skills/marasi/SKILL.md`. A binary built before a skill edit prints the old text. Rebuild before checking a change to that file.
- `--json` is rejected rather than wrapping the Markdown.
- `go test ./cmd/marasi -run TestSkillCommandsExist` checks every `marasi` command and flag in the skill's `sh` blocks against the CLI. Run it after changing either.
