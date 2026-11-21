# Built-in extensions

Where the Lua of Marasi's built-in extensions (Compass, Checkpoint, Workshop) lives and what an edit to it reaches. Names are functions and files, so search for them.

## Where the Lua lives

There are no `.lua` files. Each built-in is a SQL string literal in `db/migrations/00001_db_setup.sql`: an `INSERT INTO extensions` row with `source_url` `'marasi-internal'` and the script in `lua_content`. A later migration can change a built-in's Lua in every project: `00030_checkpoint_intercept_stream_message.sql` appends Checkpoint's `interceptStreamMessage` (which returns false) unless the script already names it, so a user's edits stay.

## What an edit reaches

The migrations are embedded and run by goose when a project opens. Goose applies `00001` once per project, so an edit to it reaches only projects created after the change. A project that already exists keeps the Lua it was created with. To reach existing projects, add a migration that updates the row, as `00030` does, and leave `00001` as it is so new projects get the same script.

Tests that open a project with `db.New` and load its extensions run the migrations, so they run the edited Lua. `startLaunchHarness` in `launchpad_test.go` is an example, as is any `service` test that opens a project.

## How each one runs

A project's extensions load from its database when it opens (`GetExtensions` in `service/project.go`). Compass and Checkpoint each have a fixed place in the request path: `CompassRequestModifier` and `CheckpointRequestModifier`, with matching response modifiers (see `request-path.md`). `ExtensionsRequestModifier` runs every other enabled extension, Workshop included, and skips those two. Compass is a core extension, so its modifier fails with `ErrExtensionNotFound` when a project has none. The Lua API that scripts call is in `extensions/`.

## On a streamed pair

A streamed pair's heads go through the same modifiers as any pair, so Compass judges it and Checkpoint's `interceptRequest`/`interceptResponse` hold its heads. Each stream message then runs `processStreamMessage` in every enabled extension but Checkpoint (`processStreamMessageExtensions`, `stream_message.go`); none of the built-ins defines it, so only user extensions act on messages. Checkpoint holds a message when the `stream_intercept` switch is on or its `interceptStreamMessage` returns true (`shouldInterceptStreamMessage`), and a direction's trailers only when the switch is on (`shouldInterceptTrailers`); a disabled Checkpoint holds neither. The built-in's `interceptStreamMessage` returns false, so out of the box a message is held only through the switch (`marasi checkpoint stream-intercept`). Where these run in the path is in `request-path.md` ([Streamed pairs](request-path.md#streamed-pairs)).
