-- The seeded Checkpoint script defined interceptWebSocketMessage as a no-op
-- that returns false. While that hook is defined, no WebSocket message can
-- stream: the relay must buffer each message whole so the hook can see it.
-- An undefined hook means the same as returning false, so the untouched
-- seeded hook is commented out. A script the user changed is left alone.

-- +goose Up
UPDATE extensions
SET lua_content = replace(
    lua_content,
    'function interceptWebSocketMessage(message)' || char(10) || char(9) || 'return false' || char(10) || 'end',
    '-- Define interceptWebSocketMessage(message) to choose WebSocket messages to hold.' || char(10) ||
    '-- While it is defined, every message is buffered whole before it is forwarded.' || char(10) ||
    '-- function interceptWebSocketMessage(message)' || char(10) || '-- ' || char(9) || 'return false' || char(10) || '-- end'
)
WHERE name = 'checkpoint';

-- +goose Down
UPDATE extensions
SET lua_content = replace(
    lua_content,
    '-- Define interceptWebSocketMessage(message) to choose WebSocket messages to hold.' || char(10) ||
    '-- While it is defined, every message is buffered whole before it is forwarded.' || char(10) ||
    '-- function interceptWebSocketMessage(message)' || char(10) || '-- ' || char(9) || 'return false' || char(10) || '-- end',
    'function interceptWebSocketMessage(message)' || char(10) || char(9) || 'return false' || char(10) || 'end'
)
WHERE name = 'checkpoint';
