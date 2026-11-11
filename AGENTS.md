## CLI skill

When a change adds or changes a CLI command, flag, or output, update `skills/marasi/SKILL.md` in the same change. `marasi skill` prints it.

## Before you commit

Run `GOWORK=off make check` and fix what it reports. It runs gofmt, `go vet`, staticcheck and the short tests. CI runs the same static checks through `make lint`.

## Release

To cut a release, follow `docs/agents/release.md`.

## Code maps

Maps live in `docs/agents/maps/`. Read the map there before you change or ask about:

- `request-path.md`: how a request moves through the proxy: listener, modifier order, capture, upstream, the headers Marasi adds, or any `http.Client`.
- `built-in-extensions.md`: the Lua of Compass, Checkpoint or Workshop, which is seeded in a migration rather than kept in `.lua` files.

## Agent skills

### Issue tracker

Issues live as markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Domain docs

This is a single-context repo with `GLOSSARY.md` and `docs/adr/` at the root. See `docs/agents/domain.md`.
