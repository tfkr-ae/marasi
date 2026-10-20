## Go commands

Run Go commands as `GOWORK=off go …`. Agent worktrees can sit inside a checkout whose parent `go.work` excludes this module, and `go` then fails with "main module … does not contain package".

## Agent skills

### Issue tracker

Issues live as markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Domain docs

This is a single-context repo with `GLOSSARY.md` and `docs/adr/` at the root. See `docs/agents/domain.md`.
