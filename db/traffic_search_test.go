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
	items, _, err := repo.ListTraffic(nil, 200, query)
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

	items, nextCursor, err := repo.ListTraffic(nil, 2, `"needle"`)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	if got, want := idsOf(items), []uuid.UUID{ids["d"], ids["c"]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
	}
	if nextCursor == nil || *nextCursor != ids["c"] {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", ids["c"], nextCursor)
	}

	older, olderNext, err := repo.ListTraffic(nextCursor, 2, `"needle"`)
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
			items, nextCursor, err := repo.ListTraffic(nil, 200, test.query)
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
