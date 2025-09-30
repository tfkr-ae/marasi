package db

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"time"

	marasi "github.com/tfkr-ae/marasi"
	_ "github.com/tfkr-ae/marasi/db/migrations"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql migrations/*.go
var embedMigrations embed.FS

// Repository implements the marasi.Repository interface using SQLite as the backend.
// It provides database operations for requests, responses, extensions, logs, and configuration.
type Repository struct {
	dbConn *sqlx.DB // SQLite database connection
}

// NewProxyRepo creates a new Repository instance with the provided database connection.
//
// Parameters:
//   - db: SQLite database connection
//
// Returns:
//   - *Repository: New repository instance
func NewProxyRepo(db *sqlx.DB) *Repository {
	return &Repository{
		dbConn: db,
	}
}

func (repo *Repository) InsertLog(log marasi.Log) error {
	query := `
	INSERT INTO logs (id, level, timestamp, message, context, request_id, extension_id)
	VALUES (?, ?, ?, ?, ?, ?, ?)
`
	_, err := repo.dbConn.Exec(
		query,
		log.ID,
		log.Level,
		log.Timestamp.Format("2006-01-02 15:04:05"),
		log.Message,
		log.Context,
		log.RequestID,
		log.ExtensionID,
	)
	return err
}

func (repo *Repository) InsertRequest(req marasi.ProxyRequest) error {
	_, err := repo.dbConn.Exec("INSERT INTO request(id, scheme, method, host, path, request_raw, metadata, requested_at) VALUES (?,?,?,?,?,?,?,?)", req.ID, req.Scheme, req.Method, req.Host, req.Path, req.Raw, req.Metadata, req.RequestedAt)
	if err != nil {
		return fmt.Errorf("inserting request %d : %w", req.ID, err)
	}
	return nil
}

func (repo *Repository) InsertResponse(res marasi.ProxyResponse) error {
	result, err := repo.dbConn.Exec(`UPDATE request
              SET status = ?, status_code = ?, response_raw = ?, content_type = ?, length = ?, metadata = ?, responded_at = ?
              WHERE id = ?`, res.Status, res.StatusCode, res.Raw, res.ContentType, res.Length, res.Metadata, res.RespondedAt, res.ID)
	if err != nil {
		return fmt.Errorf("writing response %d : %w", res.ID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for response %s : %w", res.ID, err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no request found with id %s to update", res.ID)
	}

	return nil
}

func (repo *Repository) GetMetadata(id uuid.UUID) (metadata marasi.Metadata, err error) {
	err = repo.dbConn.Get(&metadata, "SELECT metadata FROM request WHERE id = ?", id)
	if err != nil {
		return metadata, fmt.Errorf("selecting metadata for request %v : %w", id, err)
	}
	return metadata, nil
}
func (repo *Repository) UpdateMetadata(metadata marasi.Metadata, ids ...uuid.UUID) error {
	for _, id := range ids {
		_, err := repo.dbConn.Exec("UPDATE request SET metadata = ? WHERE id = ?", metadata, id)
		if err != nil {
			return fmt.Errorf("updating metadata %v for %v : %w", metadata, id, err)
		}
	}
	return nil
}

func (repo *Repository) GetItems() (map[uuid.UUID]marasi.Row, error) {
	type Item struct {
		marasi.ProxyRequest
		marasi.ProxyResponse
		Metadata marasi.Metadata
	}
	query := `
	SELECT
		id AS request_id,
		scheme,
		method,
		host,
		path,
		request_raw,
		id AS response_id,
		status,
		status_code,
		content_type,
		length,
		metadata
	FROM request;
	`
	var items []Item
	start := time.Now()
	err := repo.dbConn.Select(&items, query)
	if err != nil {
		log.Print(err)
		//return nil, fmt.Errorf("querying proxy items: %w", err)
	}

	log.Printf("Query executed in %s", time.Since(start))
	log.Print(len(items))
	results := make(map[uuid.UUID]marasi.Row, len(items))

	start = time.Now()
	for _, item := range items {
		req := item.ProxyRequest
		res := item.ProxyResponse
		results[req.ID] = marasi.Row{
			Request:  req,
			Response: res,
			Metadata: item.Metadata,
		}
	}
	log.Printf("Creating the map %s", time.Since(start))
	return results, nil
}

// Get the response data for a given uuid
func (repo *Repository) GetResponse(id uuid.UUID) (response marasi.ProxyResponse, err error) {
	err = repo.dbConn.Get(&response, "SELECT id as response_id, status, status_code, content_type, length, response_raw FROM request WHERE id = ?", id)
	if err != nil {
		return marasi.ProxyResponse{}, fmt.Errorf("getting response with id %d : %w", id, err)
	}
	return response, nil
}

// TODO : Probably to be removed
func (repo *Repository) CountRows() (count int32, err error) {
	err = repo.dbConn.Get(&count, "SELECT COUNT(*) FROM request")
	if err != nil {
		return 0, fmt.Errorf("getting request count : %w", err)
	}
	return count, nil
}

func (repo *Repository) CountNotes() (count int32, err error) {
	err = repo.dbConn.Get(&count, "SELECT COUNT(*) FROM notes WHERE note IS NOT NULL AND note != ''")
	if err != nil {
		return 0, fmt.Errorf("getting request count : %w", err)
	}
	return count, nil
}
func (repo *Repository) CountLaunchpads() (count int32, err error) {
	err = repo.dbConn.Get(&count, "SELECT COUNT(*) FROM launchpad")
	if err != nil {
		return 0, fmt.Errorf("getting launchpad count : %w", err)
	}
	return count, nil
}

func (repo *Repository) CountIntercepted() (count int32, err error) {
	query := `
        SELECT COUNT(*)
        FROM request
        WHERE json_extract(metadata, '$.intercepted') = true
    `
	err = repo.dbConn.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("getting intercepted count: %w", err)
	}
	return count, nil
}

func (repo *Repository) GetLaunchpads() (launchpads []marasi.Launchpad, err error) {
	err = repo.dbConn.Select(&launchpads, "SELECT * FROM launchpad")
	if err != nil {
		return []marasi.Launchpad{}, fmt.Errorf("getting launchpads : %w", err)
	}
	return launchpads, nil
}

func (repo *Repository) GetLaunchpadRequests(id uuid.UUID) (launchpadRequests []marasi.ProxyRequest, err error) {
	query := `
		SELECT id as request_id, scheme, method, host, path, request_raw, metadata
		FROM request
		JOIN launchpad_request rr ON request.id = rr.request_id
		WHERE rr.launchpad_id = ?
    `

	err = repo.dbConn.Select(&launchpadRequests, query, id)
	if err != nil {
		return []marasi.ProxyRequest{}, fmt.Errorf("getting launchpad requests: %w", err)
	}
	return launchpadRequests, nil
}

func (repo *Repository) CreateLaunchpad(name string, description string) (id uuid.UUID, err error) {
	launchpadUUID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generating uuid : %w", err)
	}
	_, err = repo.dbConn.Exec("INSERT INTO launchpad(id, description, name) VALUES (?,?,?)", launchpadUUID, description, name)
	if err != nil {
		return uuid.Nil, fmt.Errorf("creating new launchpad %s : %w", name, err)
	}
	return launchpadUUID, nil
}
func (repo *Repository) LinkRequestToLaunchpad(requestID uuid.UUID, launchpadID uuid.UUID) error {
	query := `
	INSERT INTO launchpad_request (request_id, launchpad_id) VALUES (?, ?)
	`
	_, err := repo.dbConn.Exec(query, requestID, launchpadID)
	if err != nil {
		return fmt.Errorf("linking request with launchpad: %w", err)
	}
	return nil
}

func (repo *Repository) DeleteLaunchpad(launchpadID uuid.UUID) error {
	result, err := repo.dbConn.Exec("DELETE FROM launchpad WHERE id = ?", launchpadID)
	if err != nil {
		return fmt.Errorf("deleting launchpad %s : %w", launchpadID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("fetching rows affected : %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no launchpad with id %s", launchpadID)
	}
	return nil
}

func (repo *Repository) UpdateLaunchpad(launchpadID uuid.UUID, name, description string) error {
	query := `UPDATE launchpad SET name = COALESCE(NULLIF(?, ''), name), description = COALESCE(NULLIF(?, ''), description) WHERE id = ?`
	result, err := repo.dbConn.Exec(query, name, description, launchpadID)
	if err != nil {
		return fmt.Errorf("updating launchpad: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("fetching rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no launchpad found with ID %s", launchpadID)
	}
	return nil
}

// Update the luacode for an extension
func (repo *Repository) UpdateLuaCode(extensionName string, code string) error {
	// Update the intercept column with new code for the first row based on ROWID
	query := `UPDATE extensions SET lua_content = ? WHERE name = ?`

	// Execute the update statement
	_, err := repo.dbConn.Exec(query, code, extensionName)
	if err != nil {
		return fmt.Errorf("updating extension: %v", err)
	}
	return nil
}

// Get the lua code
func (repo *Repository) GetExtensionLuaCode(extensionName string) (code string, err error) {
	query := `SELECT lua_content FROM extensions WHERE name = ?`

	// Execute the query and scan the result into interceptCode
	err = repo.dbConn.QueryRow(query, extensionName).Scan(&code)
	if err != nil {
		return "", fmt.Errorf("getting extension %s code: %v", extensionName, err)
	}

	return code, nil
}

func (repo *Repository) GetNote(id uuid.UUID) (note string, err error) {
	query := `SELECT note FROM notes WHERE request_id = ?`
	err = repo.dbConn.Get(&note, query, id)
	if err != nil {
		return "", fmt.Errorf("getting note for request %s : %w", id, err)
	}
	return note, nil
}

func (repo *Repository) UpdateNote(id uuid.UUID, note string) (err error) {
	query := `
    INSERT INTO notes (request_id, note, created_at)
    VALUES (?, ?, CURRENT_TIMESTAMP)
    ON CONFLICT(request_id) DO UPDATE SET
        note = excluded.note,
        created_at = CURRENT_TIMESTAMP;
    `
	_, err = repo.dbConn.Exec(query, id, note)
	if err != nil {
		return fmt.Errorf("updating note %s for request %s : %w", note, id, err)
	}
	return nil
}

// Remove an extension
func (repo *Repository) RemoveExtension(name string) error {
	query := "DELETE FROM extensions WHERE name = ?"
	_, err := repo.dbConn.Exec(query, name)
	if err != nil {
		return fmt.Errorf("removing extension %s : %w", name, err)
	}
	return nil
}

// Load a new extension
func (repo *Repository) CreateExtension(name string, sourceUrl string, author string, luaContent string, publishedDate time.Time, description string) error {
	query := `
	INSERT INTO extensions (id, name, source_url, author, lua_content, update_at, enabled, description)
		VALUES (?,?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name)
		DO UPDATE SET
			source_url = excluded.source_url,
			author = excluded.author,
			lua_content = excluded.lua_content,
			update_at = excluded.update_at,
			enabled = excluded.enabled,
			description = excluded.description;
	`
	extensionUUID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generating uuid : %w", err)
	}
	_, err = repo.dbConn.Exec(query, extensionUUID, name, sourceUrl, author, luaContent, publishedDate, true, description)
	if err != nil {
		return fmt.Errorf("creating new extension : %w", err)
	}
	return nil
}

// Get an extensions
func (repo *Repository) GetExtension(name string) (*marasi.Extension, error) {
	var extension marasi.Extension
	err := repo.dbConn.Get(&extension, "SELECT * FROM extensions WHERE name = ?", name)
	if err != nil {
		return &extension, fmt.Errorf("fetching all extensions : %w", err)
	}
	return &extension, nil
}

// Get all the extensions
func (repo *Repository) GetExtensions() ([]*marasi.Extension, error) {
	var extensions []*marasi.Extension
	err := repo.dbConn.Select(&extensions, "SELECT * FROM extensions ORDER BY id ASC")
	if err != nil {
		return extensions, fmt.Errorf("fetching all extensions : %w", err)
	}
	return extensions, nil
}

// Get all logs
func (repo *Repository) GetLogs() ([]marasi.Log, error) {
	var logs []marasi.Log
	err := repo.dbConn.Select(&logs, "SELECT * FROM logs")
	if err != nil {
		return logs, fmt.Errorf("fetching all logs : %w", err)
	}
	return logs, nil
}

// Get settings for an extension
func (repo *Repository) GetExtensionSettings(id uuid.UUID) (marasi.Metadata, error) {
	var settings marasi.Metadata
	err := repo.dbConn.Get(&settings, "SELECT settings FROM extensions WHERE id = ?", id.String())
	if err != nil {
		return settings, fmt.Errorf("fetching extension %s settings : %w", id.String(), err)
	}
	return settings, nil
}
func (repo *Repository) SetExtensionSettings(id uuid.UUID, settings marasi.Metadata) error {
	_, err := repo.dbConn.Exec("UPDATE extensions SET settings = ? WHERE id = ?", settings, id.String())
	if err != nil {
		return fmt.Errorf("updating settings (%v) for extension %s : %w", settings, id.String(), err)
	}
	return nil
}

func (repo *Repository) UpdateSPKI(spki string) error {
	_, err := repo.dbConn.Exec("UPDATE app SET spki = ?", spki)
	if err != nil {
		return fmt.Errorf("updating spki value %s : %w", spki, err)
	}
	return nil
}

func (repo *Repository) GetFilters() (results []string, err error) {
	var filtersString string
	err = repo.dbConn.Get(&filtersString, "SELECT filters FROM app LIMIT 1")
	if err != nil {
		return []string{}, fmt.Errorf("getting filters : %w", err)
	}
	err = json.Unmarshal([]byte(filtersString), &results)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal filters JSON: %w", err)
	}
	return results, nil
}

func (repo *Repository) SetFilters(filters []string) error {
	// Marshal the []string to JSON
	marshalledFilters, err := json.Marshal(filters)
	if err != nil {
		return fmt.Errorf("failed to marshal filters: %w", err)
	}

	// Update the filters in the app table
	query := "UPDATE app SET filters = ?"
	_, err = repo.dbConn.Exec(query, marshalledFilters)
	if err != nil {
		return fmt.Errorf("failed to update filters: %w", err)
	}
	return nil
}

// GetWaypoints retrieves all waypoints as a map[hostname]override.
func (repo *Repository) GetWaypoints() (map[string]string, error) {
	rows, err := repo.dbConn.Queryx("SELECT hostname, override FROM waypoint")
	if err != nil {
		return nil, fmt.Errorf("retrieving waypoints: %w", err)
	}
	defer rows.Close()

	waypoints := make(map[string]string)

	for rows.Next() {
		var hostname, override string
		if err := rows.Scan(&hostname, &override); err != nil {
			return nil, fmt.Errorf("scanning waypoint: %w", err)
		}
		waypoints[hostname] = override
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating waypoints: %w", err)
	}

	return waypoints, nil
}

// CreateOrUpdateWaypoint creates a new waypoint or updates existing override.
func (repo *Repository) CreateOrUpdateWaypoint(hostname string, override string) error {
	_, err := repo.dbConn.Exec(`
		INSERT INTO waypoint(hostname, override)
		VALUES (?, ?)
		ON CONFLICT(hostname) DO UPDATE SET override=excluded.override
	`, hostname, override)
	if err != nil {
		return fmt.Errorf("creating or updating waypoint for hostname %s: %w", hostname, err)
	}
	return nil
}

// DeleteWaypoint deletes a waypoint entry by hostname.
func (repo *Repository) DeleteWaypoint(hostname string) error {
	result, err := repo.dbConn.Exec("DELETE FROM waypoint WHERE hostname=?", hostname)
	if err != nil {
		return fmt.Errorf("deleting waypoint for hostname %s: %w", hostname, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking deletion rows affected for hostname %s: %w", hostname, err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no waypoint entry found to delete for hostname %s", hostname)
	}

	return nil
}

func (repo *Repository) Close() error {
	err := repo.dbConn.Close()
	if err != nil {
		return fmt.Errorf("closing repo : %w", err)
	}
	return nil
}

var schema = `
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
      request:DoNotLog()
  end
end

function processResponse(response)
  if not scope:matches(response) then
      response:DoNotLog()
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
`

// New creates a new SQLite database connection with WAL mode, foreign keys enabled,
// and applies all database migrations. It configures connection pooling for optimal performance.
//
// Parameters:
//   - name: Path to the SQLite database file
//
// Returns:
//   - *sqlx.DB: Configured database connection
//   - error: Connection or migration error
func New(name string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("sqlite", fmt.Sprintf("%s?_journal=WAL&_timeout=5000&_fk=true", name))
	if err != nil {
		return nil, fmt.Errorf("connecting to db : %w", err)
	}
	goose.SetBaseFS(embedMigrations)

	if err := goose.SetDialect(string(goose.DialectSQLite3)); err != nil {
		return nil, fmt.Errorf("setting dialect for migrations : %w", err)
	}

	if err := goose.Up(db.DB, "migrations"); err != nil {
		return nil, fmt.Errorf("applying migration : %w", err)
	}
	db.SetMaxIdleConns(10) // Reader conn
	db.SetMaxOpenConns(1)  // Write
	return db, nil
}
