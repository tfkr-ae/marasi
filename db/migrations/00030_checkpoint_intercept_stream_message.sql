-- The built-in Checkpoint extension gains interceptStreamMessage, which
-- decides whether Checkpoint holds a stream message (spec: Lua). Checkpoint
-- is seeded once by 00001, so a project that already exists gets the hook
-- here (docs/agents/maps/built-in-extensions.md). It is appended, so a
-- user's edits to the script stay, and a script that already defines the
-- hook is left as it is. The default holds nothing.

-- +goose Up
UPDATE extensions
SET lua_content = lua_content || '
function interceptStreamMessage(message)
	return false
end
'
WHERE name = 'checkpoint' AND source_url = 'marasi-internal'
  AND lua_content NOT LIKE '%interceptStreamMessage%';

-- +goose Down
UPDATE extensions
SET lua_content = replace(lua_content, '
function interceptStreamMessage(message)
	return false
end
', '')
WHERE name = 'checkpoint' AND source_url = 'marasi-internal';
