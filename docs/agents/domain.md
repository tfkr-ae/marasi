# Domain docs

How engineering skills should consume this repo's domain documentation when exploring the codebase.

## Before exploring, read these

- `GLOSSARY.md` at the repo root.
- `docs/adr/`: read ADRs that touch the area you're about to work in.

If these paths do not exist, proceed silently. The `/domain-modeling` skill creates them when terms or decisions get resolved.

## Name ADRs by date and slug

Name a new ADR `docs/adr/YYYY-MM-DD-<slug>.md`, dated the day you write it. This overrides the sequential numbering in `/domain-modeling`: parallel worktrees each took the same next number. Refer to an ADR by its path, as a markdown link in docs.

## Use the glossary's vocabulary

When your output names a domain concept, use the term defined in `GLOSSARY.md`. This applies to issue titles, refactor proposals, hypotheses, and test names. Do not use synonyms that the glossary explicitly avoids.

If the glossary does not define the concept, reconsider whether you are inventing language the project does not use. If the gap is real, note it for `/domain-modeling`.

## Flag ADR conflicts

If your output contradicts an existing ADR, state the conflict instead of silently overriding it:

> _Contradicts `docs/adr/2026-10-19-the-proxy-adds-no-headers-to-a-request.md`, but worth reopening because..._
