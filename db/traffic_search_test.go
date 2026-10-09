package db

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// textPair is one captured request/response pair for text search tests.
type textPair struct {
	name    string
	host    string
	request string
	// response nil leaves the pair in flight.
	response []byte
}

// captureTextPairs captures the pairs oldest first through the normal write
// operations and returns their ids by name.
func captureTextPairs(t *testing.T, repo *Repository, pairs []textPair) map[string]uuid.UUID {
	t.Helper()
	ids := make(map[string]uuid.UUID, len(pairs))
	for _, pair := range pairs {
		host := pair.host
		if host == "" {
			host = "example.com"
		}
		id := captureRequest(t, repo, host, pair.request)
		if pair.response != nil {
			captureResponse(t, repo, id, pair.response)
		}
		ids[pair.name] = id
		time.Sleep(2 * time.Millisecond)
	}
	return ids
}

func captureRequest(t *testing.T, repo *Repository, host, raw string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating uuid: %v", err)
	}
	err = repo.InsertRequest(&domain.ProxyRequest{
		ID:          id,
		Scheme:      "https",
		Method:      "GET",
		Host:        host,
		Path:        "/",
		Raw:         []byte(raw),
		Metadata:    map[string]any{},
		RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	return id
}

func captureResponse(t *testing.T, repo *Repository, id uuid.UUID, raw []byte) {
	t.Helper()
	err := repo.InsertResponse(&domain.ProxyResponse{
		ID:          id,
		Status:      "200 OK",
		StatusCode:  200,
		ContentType: "text/plain",
		Length:      "0",
		Raw:         raw,
		Metadata:    map[string]any{},
		RespondedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("inserting response: %v", err)
	}
}

// listNames runs query through ListTraffic and returns the pair names newest
// first.
func listNames(t *testing.T, repo *Repository, ids map[string]uuid.UUID, query string) string {
	t.Helper()
	items, _, _, err := repo.ListTraffic(nil, 200, query)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	names := make([]string, 0, len(items))
	for _, id := range idsOf(items) {
		for name, candidate := range ids {
			if candidate == id {
				names = append(names, name)
			}
		}
	}
	return strings.Join(names, ",")
}

func textSearchPairs() []textPair {
	return []textPair{
		{
			name:     "jwt",
			host:     "api.example.com",
			request:  "GET /me HTTP/1.1\r\nHost: api.example.com\r\nAuthorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.sig\r\n\r\n",
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"name\":\"alice\"}"),
		},
		{
			name:     "login",
			host:     "auth.example.com",
			request:  "POST /login HTTP/1.1\r\nHost: auth.example.com\r\nX-Api-Key: k-77\r\n\r\nuser=alice&password=hunter2&pin=048213",
			response: []byte("HTTP/1.1 302 Found\r\nSet-Cookie: session=s3cr3t\r\n\r\nRedirecting"),
		},
		{
			name:     "reset",
			host:     "api.example.com",
			request:  "POST /reset HTTP/1.1\r\nHost: api.example.com\r\nX-Api-Key: k-99\r\n\r\n{}",
			response: []byte("HTTP/1.1 400 Bad Request\r\nContent-Type: application/json\r\n\r\n{\"error\":\"Password too short\"}"),
		},
		{
			name:    "pending",
			host:    "api.example.com",
			request: "GET /slow HTTP/1.1\r\nHost: api.example.com\r\n\r\n",
		},
	}
}

func TestTrafficRepo_ListTrafficTextSearch(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, textSearchPairs())

	tests := []struct {
		query string
		want  string // names newest first
	}{
		{query: `"hbGci"`, want: "jwt"},
		{query: `hbGci`, want: "jwt"},
		{query: `"HBGCI"`, want: "jwt"},
		{query: `"PASSWORD"`, want: "reset,login"},
		{query: `"assw"`, want: "reset,login"},
		{query: `"x-api-key: k-9"`, want: "reset"},
		{query: `"GET /"`, want: "pending,jwt"},
		{query: `"nothing like this"`, want: ""},
		{query: `48213`, want: "login"},
		{query: `request_body:48213`, want: "login"},
		{query: `"8213"`, want: "login"},

		{query: `response_body:"password"`, want: "reset"},
		{query: `request_body:"password"`, want: "login"},
		{query: `request_head:"password"`, want: ""},
		{query: `response_head:"password"`, want: ""},
		{query: `request_head:"X-Api-Key"`, want: "reset,login"},
		{query: `response_head:found`, want: "login"},
		{query: `response_head:"set-cookie"`, want: "login"},
		{query: `response_head:"HTTP/1.1 400"`, want: "reset"},
		{query: `request_body:"user=alice"`, want: "login"},
		{query: `response_body:"alice"`, want: "jwt"},
		{query: `request_head:"slow"`, want: "pending"},

		{query: `host = "api.example.com" AND request_head:"X-Api-Key"`, want: "reset"},
		{query: `host = "api.example.com" "password"`, want: "reset"},
		{query: `"password" AND "k-77"`, want: "login"},
		{query: `"password" OR "hbGci"`, want: "reset,login,jwt"},
		{query: `NOT "password"`, want: "pending,jwt"},
		{query: `-"password"`, want: "pending,jwt"},
		{query: `"password" AND NOT response_body:"password"`, want: "login"},
		{query: `host = "api.example.com" AND ("hbGci" OR response_head:"400")`, want: "reset,jwt"},
		// OR binds tighter than AND: api AND (hbGci OR s3cr3t).
		{query: `host = "api.example.com" AND "hbGci" OR "s3cr3t"`, want: "jwt"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			if got := listNames(t, repo, ids, test.query); got != test.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", test.want, got)
			}
		})
	}
}

func TestTrafficRepo_ListTrafficTextSearchSkipsBinaryBodies(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{
		{
			name:     "nul",
			request:  "POST /upload HTTP/1.1\r\nHost: example.com\r\n\r\nnulmarker\x00request",
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: image/png\r\n\r\n\x89PNG\x00nulmarker"),
		},
		{
			name:     "latin1",
			request:  "GET /latin1 HTTP/1.1\r\nHost: example.com\r\n\r\n",
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain; charset=iso-8859-1\r\n\r\nlatin1marker caf\xe9"),
		},
		{
			// image/png is in the project's content-type list, which the index
			// does not consult: a text body is indexed whatever its type.
			name:     "svg",
			request:  "GET /logo HTTP/1.1\r\nHost: example.com\r\n\r\n",
			response: []byte("HTTP/1.1 200 OK\r\nContent-Type: image/png\r\n\r\n<svg>textmarker</svg>"),
		},
	})

	tests := []struct {
		query string
		want  string
	}{
		{query: `"nulmarker"`, want: ""},
		{query: `"latin1marker"`, want: ""},
		{query: `"textmarker"`, want: "svg"},
		{query: `response_head:"image/png"`, want: "svg,nul"},
		{query: `request_head:"/upload"`, want: "nul"},
		{query: `response_head:"iso-8859-1"`, want: "latin1"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			if got := listNames(t, repo, ids, test.query); got != test.want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", test.want, got)
			}
		})
	}
}

func TestTrafficRepo_ListTrafficTextSearchLateResponse(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	id := captureRequest(t, repo, "example.com", "GET /later HTTP/1.1\r\nHost: example.com\r\n\r\n")
	ids := map[string]uuid.UUID{"later": id}

	if got := listNames(t, repo, ids, `response_body:"arrived"`); got != "" {
		t.Fatalf("\nwanted:\n%q\ngot:\n%q", "", got)
	}

	captureResponse(t, repo, id, []byte("HTTP/1.1 201 Created\r\n\r\nresponse arrived"))

	for _, query := range []string{`response_body:"arrived"`, `response_head:"201 Created"`, `request_head:"/later"`} {
		if got := listNames(t, repo, ids, query); got != "later" {
			t.Fatalf("%s\nwanted:\n%q\ngot:\n%q", query, "later", got)
		}
	}
}

func TestTrafficRepo_ListTrafficTextSearchArmoryEntries(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	run := armoryTestRun(t, repo, armoryTestTemplate(t, repo, "Template"))
	ids := captureTextPairs(t, repo, []textPair{
		{name: "fuzzed", request: "GET /?q=fuzzpayload HTTP/1.1\r\nHost: example.com\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nreflected fuzzpayload")},
	})
	if err := repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: run.ID, RequestID: ids["fuzzed"]}); err != nil {
		t.Fatalf("creating armory entry: %v", err)
	}

	if got := listNames(t, repo, ids, `response_body:"fuzzpayload"`); got != "fuzzed" {
		t.Fatalf("\nwanted:\n%q\ngot:\n%q", "fuzzed", got)
	}
}

func TestTrafficRepo_ListTrafficTextSearchPaging(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{
		{name: "a", request: "GET /a HTTP/1.1\r\nX-Trace: needle\r\n\r\n"},
		{name: "b", request: "GET /b HTTP/1.1\r\n\r\n"},
		{name: "c", request: "GET /c HTTP/1.1\r\nX-Trace: needle\r\n\r\n"},
		{name: "d", request: "GET /d HTTP/1.1\r\nX-Trace: NEEDLE\r\n\r\n"},
	})

	items, nextCursor, _, err := repo.ListTraffic(nil, 2, `"needle"`)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	if got, want := idsOf(items), []uuid.UUID{ids["d"], ids["c"]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
	}
	if nextCursor == nil || *nextCursor != ids["c"] {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", ids["c"], nextCursor)
	}

	older, olderNext, _, err := repo.ListTraffic(nextCursor, 2, `"needle"`)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	if got, want := idsOf(older), []uuid.UUID{ids["a"]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
	}
	if olderNext != nil {
		t.Fatalf("\nwanted:\nnil next_cursor\ngot:\n%v", olderNext)
	}
}

func TestTrafficRepo_ListTrafficTextSearchErrors(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	captureTextPairs(t, repo, textSearchPairs()[:1])

	tests := []struct {
		query    string
		message  string
		position int
	}{
		{query: `"hb"`, message: `text term "hb" is shorter than 3 characters; search for a longer term`, position: 1},
		{query: `hb`, message: `shorter than 3 characters`, position: 1},
		{query: `"éa"`, message: `shorter than 3 characters`, position: 1},
		{query: `response_body:"ab"`, message: `text term "ab" is shorter than 3 characters`, position: 15},
		{query: `host = "a" AND "hb"`, message: `shorter than 3 characters`, position: 16},
		{query: `response_body = "token"`, message: `operator = is not supported on response_body; use :, for example response_body:"text"`, position: 1},
		{query: `request_head != "token"`, message: `operator != is not supported on request_head; use :`, position: 1},
		{query: `request_body > "token"`, message: `operator > is not supported on request_body; use :`, position: 1},
		{query: `host:"api"`, message: `operator : is not supported on host; use =`, position: 1},
		{query: `status_code:"500"`, message: `operator : is not supported on status_code`, position: 1},
		{query: `metadata.extension:"workshop"`, message: `operator : is not supported on metadata keys; use = or !=`, position: 1},
		{query: `stauts:"abc"`, message: `unknown field "stauts"`, position: 1},
		{query: `response_body.x:"abc"`, message: `field "response_body" has no member "x"`, position: 1},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			items, nextCursor, _, err := repo.ListTraffic(nil, 200, test.query)
			var queryErr *domain.QueryError
			if !errors.As(err, &queryErr) {
				t.Fatalf("\nwanted:\n*domain.QueryError\ngot:\n%T %v", err, err)
			}
			if !strings.Contains(queryErr.Message, test.message) {
				t.Fatalf("\nwanted message containing:\n%s\ngot:\n%s", test.message, queryErr.Message)
			}
			if queryErr.Position != test.position {
				t.Fatalf("\nwanted position:\n%d\ngot:\n%d (%s)", test.position, queryErr.Position, queryErr.Message)
			}
			if items != nil || nextCursor != nil {
				t.Fatalf("\nwanted:\nno page\ngot:\n%v %v", items, nextCursor)
			}
		})
	}
}

// assertListNames checks that query lists the pairs named in want, newest
// first.
func assertListNames(t *testing.T, repo *Repository, ids map[string]uuid.UUID, step, query, want string) {
	t.Helper()
	if got := listNames(t, repo, ids, query); got != want {
		t.Fatalf("%s: %s\nwanted:\n%q\ngot:\n%q", step, query, want, got)
	}
}

func TestTrafficRepo_ListTrafficNoteSearch(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{
		{name: "noted", request: "GET /users/7 HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\n{}")},
		{name: "other", request: "GET /users/8 HTTP/1.1\r\n\r\n"},
	})

	assertListNames(t, repo, ids, "before note", `note:"idor"`, "")

	if err := repo.UpdateNote(ids["noted"], "IDOR candidate: user id in path"); err != nil {
		t.Fatalf("inserting note: %v", err)
	}
	assertListNames(t, repo, ids, "after insert", `note:"idor candidate"`, "noted")
	assertListNames(t, repo, ids, "after insert", `"idor candidate"`, "noted")
	assertListNames(t, repo, ids, "after insert", `request_head:"idor"`, "")
	assertListNames(t, repo, ids, "after insert", `request_head:"/users/7"`, "noted")
	assertListNames(t, repo, ids, "after insert", `response_head:"200 OK"`, "noted")

	if err := repo.UpdateNote(ids["noted"], "checked, not exploitable"); err != nil {
		t.Fatalf("updating note: %v", err)
	}
	assertListNames(t, repo, ids, "after update", `note:"idor"`, "")
	assertListNames(t, repo, ids, "after update", `note:"exploitable"`, "noted")

	if err := repo.DeleteNote(ids["noted"]); err != nil {
		t.Fatalf("deleting note: %v", err)
	}
	assertListNames(t, repo, ids, "after delete", `note:"exploitable"`, "")
	assertListNames(t, repo, ids, "after delete", `"exploitable"`, "")
	assertListNames(t, repo, ids, "after delete", `request_head:"/users/7"`, "noted")
}

func TestTrafficRepo_ListTrafficMetadataSearch(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{
		{name: "a", request: "GET /a HTTP/1.1\r\n\r\n"},
		{name: "b", request: "GET /b HTTP/1.1\r\n\r\n"},
		{name: "c", request: "GET /c HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nok")},
	})

	assertListNames(t, repo, ids, "before update", `metadata:"workshop"`, "")

	if err := repo.UpdateMetadata(map[string]any{"extension": "Workshop", "tags": []any{"needs-review"}}, ids["a"], ids["c"]); err != nil {
		t.Fatalf("updating metadata: %v", err)
	}
	assertListNames(t, repo, ids, "after update", `metadata:"workshop"`, "c,a")
	assertListNames(t, repo, ids, "after update", `metadata:"needs-review"`, "c,a")
	assertListNames(t, repo, ids, "after update", `"needs-review"`, "c,a")
	assertListNames(t, repo, ids, "after update", `metadata:"extension"`, "c,a")
	assertListNames(t, repo, ids, "after update", `note:"workshop"`, "")
	assertListNames(t, repo, ids, "after update", `request_head:"GET /b" AND metadata:"workshop"`, "")
	assertListNames(t, repo, ids, "after update", `response_head:"200 OK" AND metadata:"workshop"`, "c")

	if err := repo.UpdateMetadata(map[string]any{"extension": "repeater"}, ids["a"]); err != nil {
		t.Fatalf("replacing metadata: %v", err)
	}
	assertListNames(t, repo, ids, "after replace", `metadata:"workshop"`, "c")
	assertListNames(t, repo, ids, "after replace", `metadata:"repeater"`, "a")
	assertListNames(t, repo, ids, "after replace", `request_head:"/a HTTP"`, "a")
}

func TestTrafficRepo_ListTrafficMetadataSearchAtCapture(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("creating uuid: %v", err)
	}
	ids := map[string]uuid.UUID{"tagged": id}
	err = repo.InsertRequest(&domain.ProxyRequest{
		ID:          id,
		Scheme:      "https",
		Method:      "GET",
		Host:        "example.com",
		Path:        "/",
		Raw:         []byte("GET / HTTP/1.1\r\n\r\n"),
		Metadata:    map[string]any{"extension": "intercepted-by-lua", "prettified-request": "prettyrequestonly"},
		RequestedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	assertListNames(t, repo, ids, "after request", `metadata:"intercepted-by-lua"`, "tagged")
	assertListNames(t, repo, ids, "after request", `"prettyrequestonly"`, "")

	err = repo.InsertResponse(&domain.ProxyResponse{
		ID:          id,
		Status:      "200 OK",
		StatusCode:  200,
		ContentType: "text/plain",
		Length:      "0",
		Raw:         []byte("HTTP/1.1 200 OK\r\n\r\n"),
		Metadata: map[string]any{
			"extension":           "response-tagger",
			"prettified-request":  "prettyrequestonly",
			"prettified-response": "prettyresponseonly",
		},
		RespondedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("inserting response: %v", err)
	}
	assertListNames(t, repo, ids, "after response", `metadata:"response-tagger"`, "tagged")
	assertListNames(t, repo, ids, "after response", `metadata:"intercepted-by-lua"`, "")
	assertListNames(t, repo, ids, "after response", `"prettyrequestonly"`, "")
	assertListNames(t, repo, ids, "after response", `"prettyresponseonly"`, "")
	assertListNames(t, repo, ids, "after response", `metadata:"prettified"`, "")

	if err := repo.UpdateMetadata(map[string]any{"prettified-response": "prettyresponseonly", "kept": "visiblevalue"}, id); err != nil {
		t.Fatalf("updating metadata: %v", err)
	}
	assertListNames(t, repo, ids, "after update", `"prettyresponseonly"`, "")
	assertListNames(t, repo, ids, "after update", `metadata:"visiblevalue"`, "tagged")
}

func TestTrafficRepo_ListTrafficNoteChangesKeepMetadataSearch(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{{name: "a", request: "GET /a HTTP/1.1\r\n\r\n"}})
	if err := repo.UpdateMetadata(map[string]any{"extension": "workshop"}, ids["a"]); err != nil {
		t.Fatalf("updating metadata: %v", err)
	}
	if err := repo.UpdateNote(ids["a"], "a note"); err != nil {
		t.Fatalf("inserting note: %v", err)
	}
	assertListNames(t, repo, ids, "after note", `metadata:"workshop"`, "a")
	if err := repo.DeleteNote(ids["a"]); err != nil {
		t.Fatalf("deleting note: %v", err)
	}
	assertListNames(t, repo, ids, "after delete", `metadata:"workshop"`, "a")
}

// rebuildRequestTable rebuilds the request table the way a schema migration
// does: create a copy, fill it, drop the original, and rename the copy. The
// rows are copied oldest last, so every pair gets a different SQLite rowid.
func rebuildRequestTable(t *testing.T, repo *Repository) {
	t.Helper()
	var schema string
	if err := repo.dbConn.Get(&schema, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'request'`); err != nil {
		t.Fatalf("reading the request schema: %v", err)
	}
	var indexes []string
	if err := repo.dbConn.Select(&indexes, `SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'request' AND sql IS NOT NULL`); err != nil {
		t.Fatalf("reading the request indexes: %v", err)
	}
	statements := []string{
		`PRAGMA foreign_keys = OFF`,
		`PRAGMA legacy_alter_table = ON`,
		strings.Replace(schema, "request", "request_rebuilt", 1),
		`INSERT INTO request_rebuilt SELECT * FROM request ORDER BY id DESC`,
		`DROP TABLE request`,
		`ALTER TABLE request_rebuilt RENAME TO request`,
	}
	statements = append(statements, indexes...)
	statements = append(statements, `PRAGMA legacy_alter_table = OFF`, `PRAGMA foreign_keys = ON`)
	for _, statement := range statements {
		if _, err := repo.dbConn.Exec(statement); err != nil {
			t.Fatalf("rebuilding the request table: %s: %v", statement, err)
		}
	}
}

func TestTrafficRepo_ListTrafficTextSearchAfterRequestTableRebuild(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, textSearchPairs())

	rebuildRequestTable(t, repo)

	for query, want := range map[string]string{
		`"hbGci"`:                   "jwt",
		`response_body:"password"`:  "reset",
		`request_body:"user=alice"`: "login",
		`request_head:"slow"`:       "pending",
		`NOT "password"`:            "pending,jwt",
	} {
		t.Run(query, func(t *testing.T) {
			if got := listNames(t, repo, ids, query); got != want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
			}
		})
	}
	if !indexComplete(t, repo) {
		t.Fatalf("\nwanted:\ncomplete index after the rebuild\ngot:\nincomplete")
	}
}

func TestTrafficRepo_ListTrafficTextSearchAfterPairDeleted(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := captureTextPairs(t, repo, []textPair{{name: "deleted", request: "GET /old HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nleftover-secret")}})

	// Nothing deletes pairs yet; a future delete removes the request row and,
	// through its foreign key, the pair's index key.
	if _, err := repo.dbConn.Exec(`DELETE FROM request WHERE id = ?`, ids["deleted"]); err != nil {
		t.Fatalf("deleting the pair: %v", err)
	}
	for name, id := range captureTextPairs(t, repo, []textPair{{name: "newer", request: "GET /new HTTP/1.1\r\n\r\n", response: []byte("HTTP/1.1 200 OK\r\n\r\nfresh")}}) {
		ids[name] = id
	}

	if got := listNames(t, repo, ids, `"leftover-secret"`); got != "" {
		t.Fatalf("\nwanted:\nthe deleted pair's text to match nothing\ngot:\n%q", got)
	}
	if got := listNames(t, repo, ids, `"fresh"`); got != "newer" {
		t.Fatalf("\nwanted:\n%q\ngot:\n%q", "newer", got)
	}
}
