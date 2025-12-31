-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS app (
    version TEXT NOT NULL,
	spki TEXT NOT NULL DEFAULT '',
	filters JSON DEFAULT '[
    "audio/aac",
    "image/apng",
    "image/avif",
    "video/x-msvideo",
    "image/bmp",
    "audio/midi",
    "audio/x-midi",
    "image/gif",
    "image/vnd.microsoft.icon",
    "image/jpeg",
    "audio/mpeg",
    "video/mp4",
    "video/mpeg",
    "audio/ogg",
    "video/ogg",
    "image/png",
    "image/svg+xml",
    "image/tiff",
    "video/mp2t",
    "audio/wav",
    "audio/webm",
    "video/webm",
    "image/webp",
    "video/3gpp",
    "audio/3gpp",
    "video/3gpp2",
    "audio/3gpp2",
    "application/vnd.ms-fontobject",
    "font/otf",
    "font/ttf",
    "font/woff",
    "font/woff2",
    "application/x-abiword",
    "application/vnd.amazon.ebook",
    "application/x-cdf",
    "application/x-csh",
    "application/epub+zip",
    "application/vnd.apple.installer+xml",
    "application/vnd.oasis.opendocument.presentation",
    "application/vnd.oasis.opendocument.spreadsheet",
    "application/vnd.oasis.opendocument.text",
    "application/vnd.visio",
    "application/vnd.mozilla.xul+xml",
    "application/javascript",
    "application/octet-stream",
    "binary/octet-stream",
    "image/x-icon",
    "image/vnd.djvu",
    "image/x-portable-pixmap",
    "image/x-portable-bitmap",
    "audio/basic",
    "audio/aiff",
    "audio/x-aiff",
    "audio/x-wav",
    "audio/x-mpegurl",
    "audio/x-ms-wma",
    "video/quicktime",
    "video/x-flv",
    "video/x-ms-wmv",
    "video/x-msvideo",
    "application/pdf",
    "application/msword",
    "application/vnd.ms-excel",
    "application/vnd.ms-powerpoint",
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    "application/vnd.openxmlformats-officedocument.presentationml.presentation",
    "application/rtf",
    "application/x-rar-compressed",
    "application/x-7z-compressed",
    "application/zip",
    "application/x-tar",
    "application/gzip",
    "application/x-font-ttf",
    "application/x-font-woff",
    "text/css",
    "text/calendar",
    "text/vcard",
    "application/x-shockwave-flash",
    "application/x-bzip",
    "application/x-bzip2"
	]'
);
CREATE TABLE IF NOT EXISTS launchpad (
    id TEXT PRIMARY KEY,
    description TEXT,
	name TEXT
);
CREATE TABLE IF NOT EXISTS request (
	id TEXT PRIMARY KEY,
	scheme TEXT NOT NULL CHECK (scheme <> ''),
	method TEXT NOT NULL CHECK (method <> ''),
	host TEXT NOT NULL,
	path TEXT NOT NULL,
	request_raw TEXT NOT NULL,
	status TEXT DEFAULT 'N/A',
	status_code INTEGER DEFAULT -1,
	response_raw TEXT DEFAULT '',
	content_type TEXT DEFAULT '',
	length TEXT DEFAULT '0',
	metadata JSON DEFAULT '{}',
	requested_at DATETIME NOT NULL,     
	responded_at DATETIME            
);
CREATE TABLE IF NOT EXISTS launchpad_request (
    request_id TEXT,
    launchpad_id TEXT,
    PRIMARY KEY (request_id, launchpad_id),
    FOREIGN KEY (request_id) REFERENCES request(id) ON DELETE CASCADE,
    FOREIGN KEY (launchpad_id) REFERENCES launchpad(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS notes (
    request_id TEXT PRIMARY KEY,
    note TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (request_id) REFERENCES request(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS extensions (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,          -- Unique name of the extension
    source_url TEXT NOT NULL,           -- Source URL (e.g., GitHub repository)
	author TEXT NOT NULL,
    lua_content TEXT NOT NULL,                  -- Lua script content for proxy extension
	update_at TIMESTAMP NOT NULL,
	enabled BOOLEAN DEFAULT false, 
	description TEXT NOT NULL,
	settings JSON DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS logs (
    id TEXT PRIMARY KEY,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    level TEXT NOT NULL CHECK (level IN ('DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL')),
    message TEXT NOT NULL,
    context JSON DEFAULT '{}',
    request_id TEXT,
    extension_id TEXT,
    FOREIGN KEY (request_id) REFERENCES request(id) ON DELETE CASCADE,
    FOREIGN KEY (extension_id) REFERENCES extensions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS waypoint (
    hostname TEXT PRIMARY KEY,
    override TEXT NOT NULL
);

INSERT INTO app (version)
SELECT '1.0.0'
WHERE NOT EXISTS (SELECT 1 FROM app);

INSERT INTO extensions (id, name, source_url, author, lua_content, update_at, enabled, description, settings)
SELECT 
    '01937d13-9632-72aa-83b9-c10ea1abbdd6', 
    'compass', 
    'marasi-internal', 
    'Steris', 
    'local scope = marasi:scope()
scope:clear_rules()
scope:add_rule("-.*\\.gstatic\\.com", "host")
scope:add_rule("-.*\\.googleapis\\.com", "host")
scope:add_rule("-.*\\/complete\\/search.*", "url")
scope:add_rule("-.*GetAsyncData.*", "url")
scope:add_rule("-.*\\/optimizationguide.*", "url")
scope:add_rule("-.*tbn:ANd9Gc.*", "url")
scope:add_rule("-.*\\/log.*", "url")

function processRequest(request)
  if not scope:matches(request) then
      request:skip()
  end
end

function processResponse(response)
  if not scope:matches(response) then
      response:skip()
  end
end
--[[
Welcome to the Marasi Compass Extension

This extension allows you to control the scope functionality of the Marasi proxy.
You can define inclusion and exclusion rules, check whether specific requests or responses are in scope, and manipulate the scope behavior.

### Default Behavior
- Default Allow: true (requests/responses not matching any rule will be allowed by default)

### Available Methods

#### marasi:scope()
- Returns the current scope object, which can be used to manage inclusion and exclusion rules.

#### Scope Object Methods:
1. add_rule(pattern: string, matchType: string)
   - Adds a new rule to the scope.
   - pattern: Regular expression to match the URL or host.
   - matchType: "host" or "url".
   - Usage: scope:add_rule("example\\.com", "host")

2. remove_rule(pattern: string, matchType: string)
   - Removes an existing rule from the scope.
   - pattern: Regular expression to remove.
   - matchType: "host" or "url".
   - Usage: scope:remove_rule("example\\.com", "host")

3. clear_rules()
   - Clears all inclusion and exclusion rules.
   - Usage: scope:clear_rules()

4. matches_string(input: string, matchType: string)
   - Checks if the given input matches the scope rules.
   - input: The string to match.
   - matchType: "host" or "url".
   - Returns: true or false.
   - Usage: local isMatch = scope:matches_string("https://example.com", "url")

5. matches(request|response)
   - Checks if a request or response is in scope.
   - Returns: true or false.
   - Usage: local isMatch = scope:matches(request)

6. set_default_allow(allow: boolean)
   - Sets the default behavior for requests/responses that do not match any rule.
   - allow: true or false.
   - Usage: scope:set_default_allow(false)

#### Example Usage

Adding Rules:
local scope = marasi:scope()
scope:add_rule("example\\.com", "host")
scope:add_rule("https://secure\\.example\\.com", "url")

Removing Rules:
scope:remove_rule("example\\.com", "host")

Checking Matches:
local isMatch = scope:matches_string("https://secure.example.com", "url")
print("Is URL in scope:", isMatch)

Clearing Rules:
scope:clear_rules()

Setting Default Allow:
scope:set_default_allow(false)

#### Notes
- Ensure you carefully define rules to avoid unintentional inclusions or exclusions.
- Use matches for more complex request/response matching.

Happy Scoping!
]]--',
    CURRENT_TIMESTAMP, 
    true, 
    'Scope Management Extension', 
    '{}'
WHERE NOT EXISTS (SELECT 1 FROM extensions WHERE name = 'compass');

-- Insert the "checkpoint" extension with formatted Lua content
INSERT INTO extensions (id, name, source_url, author, lua_content, update_at, enabled, description, settings)
SELECT '01937d13-9632-75b1-9e73-c5129b06fa8c', 'checkpoint', 'marasi-internal', 'Colms', 
'-- Intercept Code
function interceptRequest(request)
	return 1~=1
end

function interceptResponse(response)
	return 1~=1
end
', 
CURRENT_TIMESTAMP, true, 'Intercept Requests / Responses', "{}"
WHERE NOT EXISTS (SELECT 1 FROM extensions WHERE name = 'checkpoint');

INSERT INTO extensions (id, name, source_url, author, lua_content, update_at, enabled, description, settings)
SELECT '01937d13-9632-7f84-add5-14ec2c2c7f43', 'workshop', 'marasi-internal', 'TenSoon', 
'--[[
Welcome to the Marasi Workshop

You have access to two global objects: marasi and extension.

- marasi
	- config: marasi:config() 
		- Description: Returns the configuration directory path.
		- Usage: local configDir = marasi:config()

	- get_map: marasi:get_map()
		- Description: Retrieves all the requests and responses handled by the proxy.
		- Usage: local map = marasi:get_map()

	- request_builder: marasi:request_builder()
		- Description: Creates a new RequestBuilder object, allowing you to construct and send custom HTTP requests.
		- Usage:
			local builder = marasi:request_builder()
			builder:Method("GET")
			builder:URL("https://example.com")
			builder:Header("Content-Type", "application/json")
			builder:Send()

- extension
	- log: extension:log("message", "INFO/DEBUG/WARN/ERROR/FATAL")
		- Description: Logs a message with a specified severity level.
		- Usage: extension:log("This is an info message", "INFO")

	- settings: extension:settings()
		- Description: Retrieves the settings for the current extension as a table.
		- Usage: local settings = extension:settings()

	- set_settings: extension:set_settings(settings)
		- Description: Updates the settings for the current extension. The settings argument should be a table.
		- Usage:
			local newSettings = { key = "value" }
			extension:set_settings(newSettings)

You can also define custom handlers for processing requests and responses intercepted by the proxy:

- processRequest(request)
	- Description: Called when a request is intercepted by the proxy.
	- Parameters: request - A Request object with methods to inspect or modify the HTTP request.
	- Example:
		function processRequest(request)
			print("Request ID:", request:ID())
			print("Request Method:", request:Method())

			-- Example: Modifying headers
			request:Headers():Set("X-Custom-Header", "CustomValue")
			local contentType = request:Headers():Get("Content-Type")
			print("Content-Type:", contentType)
		end

- processResponse(response)
	- Description: Called when a response is intercepted by the proxy.
	- Parameters: response - A Response object with methods to inspect or modify the HTTP response.
	- Example:
		function processResponse(response)
			print("Response ID:", response:ID())
			print("Response Status:", response:Status())

			-- Example: Deleting a header
			response:Headers():Del("X-Unwanted-Header")
		end

Header Methods:
- Get: header:Get(name)
	- Description: Retrieves the value of a specified header.
	- Usage: local value = header:Get("Content-Type")

- Set: header:Set(name, value)
	- Description: Sets or updates the value of a specified header.
	- Usage: header:Set("Authorization", "Bearer token")

- Add: header:Add(name, value)
	- Description: Adds a new value to a header that supports multiple values.
	- Usage: header:Add("X-Custom-Header", "value")

- Del: header:Del(name)
	- Description: Deletes a specified header.
	- Usage: header:Del("X-Unwanted-Header")

RequestBuilder Methods:
- Method: builder:Method(method)
	- Description: Sets the HTTP method (e.g., "GET", "POST").

- URL: builder:URL(url)
	- Description: Sets the request URL.

- Body: builder:Body(body)
	- Description: Sets the request body.

- Header: builder:Header(name, value)
	- Description: Adds a custom header to the request.

- Cookie: builder:Cookie(name, value)
	- Description: Adds a cookie to the request.

- Send: builder:Send()
	- Description: Sends the constructed request and returns the response body.

Example Usage:
	local builder = marasi:request_builder()
	builder:Method("POST")
	builder:URL("https://api.example.com/submit")
	builder:Header("Content-Type", "application/json")
	builder:Body({"key": "value"}) (With single quotes)
	local response = builder:Send()
	print("Response:", response)

Notes:
- Make sure to configure your requests properly before sending them.
- Use extension:log to keep track of important events or errors in your Lua code.
- You can customize the behavior of the proxy by defining processRequest and processResponse functions.

Happy coding!
]]--
', 
CURRENT_TIMESTAMP, true, 'Lua Workshop', "{}"
WHERE NOT EXISTS (SELECT 1 FROM extensions WHERE name = 'workshop');

-- Trigger for after insert on notes
CREATE TRIGGER IF NOT EXISTS note_inserted
AFTER INSERT ON notes
FOR EACH ROW
BEGIN
    UPDATE request
    SET metadata = CASE
        WHEN NEW.note IS NULL OR NEW.note = ''
            THEN json_remove(metadata, '$.has_note')
        ELSE json_set(metadata, '$.has_note', true)
    END
    WHERE id = NEW.request_id;
END;

-- Trigger for after update on notes
CREATE TRIGGER IF NOT EXISTS note_updated
AFTER UPDATE ON notes
FOR EACH ROW
BEGIN
    UPDATE request
    SET metadata = CASE
        WHEN NEW.note IS NULL OR NEW.note = ''
            THEN json_remove(metadata, '$.has_note')
        ELSE json_set(metadata, '$.has_note', true)
    END
    WHERE id = NEW.request_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS note_inserted;
DROP TRIGGER IF EXISTS note_updated;

DROP TABLE IF EXISTS launchpad_request;
DROP TABLE IF EXISTS notes;
DROP TABLE IF EXISTS logs;
DROP TABLE IF EXISTS waypoint;
DROP TABLE IF EXISTS extensions;
DROP TABLE IF EXISTS request;
DROP TABLE IF EXISTS launchpad;
DROP TABLE IF EXISTS app;
-- +goose StatementEnd
