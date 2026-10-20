# Built-in extensions

Where the Lua of Marasi's built-in extensions (Compass, Checkpoint, Workshop) lives and what an edit to it reaches. Names are functions and files, so search for them.

## Where the Lua lives

There are no `.lua` files. Each built-in is a SQL string literal in `db/migrations/00001_db_setup.sql`: an `INSERT INTO extensions` row with `source_url` `'marasi-internal'` and the script in `lua_content`. To change a built-in, edit its literal there.

## What an edit reaches

The migrations are embedded and run by goose when a project opens. Goose applies `00001` once per project, so an edit to it reaches only projects created after the change. A project that already exists keeps the Lua it was created with.

Tests that open a project with `db.New` and load its extensions run the migrations, so they run the edited Lua. `startLaunchHarness` in `launchpad_test.go` is an example, as is any `service` test that opens a project.

## How each one runs

A project's extensions load from its database when it opens (`GetExtensions` in `service/project.go`). Compass and Checkpoint each have a fixed place in the request path: `CompassRequestModifier` and `CheckpointRequestModifier`, with matching response modifiers (see `request-path.md`). `ExtensionsRequestModifier` runs every other enabled extension, Workshop included, and skips those two. Compass is a core extension, so its modifier fails with `ErrExtensionNotFound` when a project has none. The Lua API that scripts call is in `extensions/`.
