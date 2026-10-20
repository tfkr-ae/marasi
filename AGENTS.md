## Go commands

Run Go commands as `GOWORK=off go …`. Agent worktrees can sit inside a checkout whose parent `go.work` excludes this module, and `go` then fails with "main module … does not contain package".

## CLI skill

When a change adds or changes a CLI command, flag, or output, update `skills/marasi/SKILL.md` in the same change. `marasi skill` prints it.

## Code maps

Maps live in `docs/agents/maps/`. Read the map there before you change or ask about:

- `request-path.md`: how a request moves through the proxy: listener, modifier order, capture, upstream, the headers Marasi adds, or any `http.Client`.

## Agent skills

### Issue tracker

Issues live as markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Domain docs

This is a single-context repo with `GLOSSARY.md` and `docs/adr/` at the root. See `docs/agents/domain.md`.
