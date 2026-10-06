# Domain docs

How engineering skills should consume this repo's domain documentation when exploring the codebase.

## Before exploring, read these

- `GLOSSARY.md` at the repo root.
- `docs/adr/`: read ADRs that touch the area you're about to work in.

If these paths do not exist, proceed silently. The `/domain-modeling` skill creates them when terms or decisions get resolved.

## File structure

This is a single-context repo:

```
/
├── GLOSSARY.md
├── docs/adr/
│   ├── 0001-event-sourced-orders.md
│   └── 0002-postgres-for-write-model.md
└── src/
```

## Use the glossary's vocabulary

When your output names a domain concept, use the term defined in `GLOSSARY.md`. This applies to issue titles, refactor proposals, hypotheses, and test names. Do not use synonyms that the glossary explicitly avoids.

If the glossary does not define the concept, reconsider whether you are inventing language the project does not use. If the gap is real, note it for `/domain-modeling`.

## Flag ADR conflicts

If your output contradicts an existing ADR, state the conflict instead of silently overriding it:

> _Contradicts ADR-0007 (event-sourced orders), but worth reopening because..._
