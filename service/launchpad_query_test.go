package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// hostileBody holds literal quoting, SQL, and FTS5 syntax, so a text search
// can find it only by matching it literally.
const hostileBody = `it's x" OR "1"="1 then ') OR 1=1 -- and; DROP TABLE request; -- /* c */ back\slash NEAR(alpha beta) tok* ^caret {request_body}:xyz "AND OR NOT" end`

// launchpadQueryProject is a real project with two launchpads and pairs
// linked to none, one, or both of them.
type launchpadQueryProject struct {
	database   *sqlx.DB
	server     *Server
	ids        map[string]uuid.UUID
	launchpads map[string]uuid.UUID
}

func setupLaunchpadQueryProject(t *testing.T) *launchpadQueryProject {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "project.marasi"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewProxyRepo(database)

	p := &launchpadQueryProject{
		database:   database,
		server:     newTestServer(&marasi.Proxy{LaunchpadRepo: repo, TrafficRepo: repo}, func() {}),
		ids:        map[string]uuid.UUID{},
		launchpads: map[string]uuid.UUID{},
	}
	for _, name := range []string{"one", "two"} {
		id, err := repo.CreateLaunchpad(name, "")
		if err != nil {
			t.Fatalf("creating launchpad %s: %v", name, err)
		}
		p.launchpads[name] = id
	}
	pairs := []struct {
		name       string
		statusCode int
		body       string
		metadata   map[string]any
		launchpads []string
	}{
		// Sent to Launchpad from captured traffic: linked, with no
		// launchpad_id in its metadata.
		{name: "orig", statusCode: 200, body: "session token alpha", launchpads: []string{"one"}},
		{name: "launched", statusCode: 502, body: "upstream token failure", launchpads: []string{"one"},
			metadata: map[string]any{"launchpad": true, "launchpad_id": p.launchpads["one"].String()}},
		{name: "shared", statusCode: 200, body: "shared token", launchpads: []string{"one", "two"}},
		{name: "other", statusCode: 503, body: "other token", launchpads: []string{"two"}},
		{name: "free", statusCode: 500, body: "free token"},
		{name: "hostile", statusCode: 200, body: hostileBody, launchpads: []string{"one"}},
		{name: "hostilefree", statusCode: 200, body: hostileBody},
	}
	for _, pair := range pairs {
		id := uuid.Must(uuid.NewV7())
		metadata := pair.metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		now := time.Now()
		if err := repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "GET", Host: "lp.example.com", Path: "/" + pair.name,
			Raw:      []byte("GET /" + pair.name + " HTTP/1.1\r\nHost: lp.example.com\r\n\r\n"),
			Metadata: metadata, RequestedAt: now,
		}); err != nil {
			t.Fatalf("inserting request %s: %v", pair.name, err)
		}
		if err := repo.InsertResponse(&domain.ProxyResponse{
			ID: id, Status: "status", StatusCode: pair.statusCode, ContentType: "text/plain", Length: "0",
			Raw:      []byte(fmt.Sprintf("HTTP/1.1 %d X\r\nContent-Type: text/plain\r\n\r\n%s", pair.statusCode, pair.body)),
			Metadata: metadata, RespondedAt: now.Add(time.Second),
		}); err != nil {
			t.Fatalf("inserting response %s: %v", pair.name, err)
		}
		for _, launchpad := range pair.launchpads {
			if err := repo.LinkRequestToLaunchpad(id, p.launchpads[launchpad]); err != nil {
				t.Fatalf("linking %s to %s: %v", pair.name, launchpad, err)
			}
		}
		p.ids[pair.name] = id
		time.Sleep(2 * time.Millisecond)
	}
	return p
}

// expand replaces <one> and <two> with the launchpad ids.
func (p *launchpadQueryProject) expand(text string) string {
	return strings.NewReplacer("<one>", p.launchpads["one"].String(), "<two>", p.launchpads["two"].String()).Replace(text)
}

// get requests path with q as its query and returns the status and body.
func (p *launchpadQueryProject) get(path, q string) (int, string) {
	response := requestLaunchpad(p.server, http.MethodGet, path+"?q="+url.QueryEscape(q), "")
	return response.Code, response.Body.String()
}

// names returns the fixture names of a response's items, in order.
func (p *launchpadQueryProject) names(t *testing.T, body string) string {
	t.Helper()
	var page struct {
		Items []struct {
			ID uuid.UUID `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	names := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		name := "?" + item.ID.String()
		for candidate, id := range p.ids {
			if id == item.ID {
				name = candidate
			}
		}
		names = append(names, name)
	}
	return strings.Join(names, ",")
}

// counts returns the row counts of request, launchpad, and launchpad_request.
func (p *launchpadQueryProject) counts(t *testing.T) [3]int {
	t.Helper()
	var counts [3]int
	for i, table := range []string{"request", "launchpad", "launchpad_request"} {
		if err := p.database.Get(&counts[i], "SELECT count(*) FROM "+table); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
	}
	return counts
}

func TestLaunchpadDetailQuery(t *testing.T) {
	p := setupLaunchpadQueryProject(t)

	t.Run("should return only the launchpad's entries that match q, oldest first", func(t *testing.T) {
		for _, test := range []struct {
			launchpad, q, want string
		}{
			{launchpad: "one", q: ``, want: "orig,launched,shared,hostile"},
			{launchpad: "one", q: `response_body:"token"`, want: "orig,launched,shared"},
			{launchpad: "one", q: `status_code >= 500`, want: "launched"},
			{launchpad: "one", q: `launchpad = "<two>"`, want: "shared"},
			{launchpad: "two", q: `"token"`, want: "shared,other"},
			{launchpad: "two", q: `response_body:"free"`, want: ""},
		} {
			code, body := p.get("/launchpad/"+p.launchpads[test.launchpad].String(), p.expand(test.q))
			if code != http.StatusOK {
				t.Fatalf("\n%s %s wanted:\n200\ngot:\n%d %s", test.launchpad, test.q, code, body)
			}
			if got := p.names(t, body); got != test.want {
				t.Fatalf("\n%s %s wanted:\n%s\ngot:\n%s", test.launchpad, test.q, test.want, got)
			}
			if !strings.HasPrefix(body, `{"id":"`+p.launchpads[test.launchpad].String()+`","name":"`+test.launchpad+`"`) {
				t.Fatalf("\nwanted:\nthe launchpad summary\ngot:\n%s", body)
			}
		}
	})

	t.Run("should return 400 invalid_query with the message and position", func(t *testing.T) {
		code, body := p.get("/launchpad/"+p.launchpads["one"].String(), `status_code = 200 AND stauts = 5`)
		want := "{\"error\":\"invalid_query\",\"message\":\"unknown field \\\"stauts\\\"\",\"position\":23}\n"
		if code != http.StatusBadRequest || body != want {
			t.Fatalf("\nwanted:\n400 %s\ngot:\n%d %s", want, code, body)
		}
	})

	t.Run("should report a missing launchpad before an invalid query", func(t *testing.T) {
		assertLaunchpadError(t, requestLaunchpad(p.server, http.MethodGet, "/launchpad/0193802f-f0e7-73d9-a764-06d21e367809?q=stauts", ""), http.StatusNotFound, "not_found")
	})
}

// TestTrafficQueryHostileInput proves every query reaches SQLite as constant
// SQL plus bound parameters: each hostile input either selects exactly the
// pairs its literal meaning selects or fails as an invalid query naming the
// problem and position, through the traffic list and a launchpad's entries,
// and no input changes the database.
func TestTrafficQueryHostileInput(t *testing.T) {
	p := setupLaunchpadQueryProject(t)
	before := p.counts(t)

	type outcome struct {
		// names lists the matching pairs oldest first. Ignored when message
		// is set.
		names string
		// message and position describe an expected invalid query.
		message  string
		position int
	}
	tests := []struct {
		name      string
		q         string
		traffic   outcome // GET /traffic?q=
		launchpad outcome // GET /launchpad/<one>?q=
	}{
		// (a) the launchpad field with hostile values.
		{name: "double quote id", q: `launchpad = "\""`, traffic: outcome{message: `"\"" is not a launchpad id`, position: 13}, launchpad: outcome{message: `"\"" is not a launchpad id`, position: 13}},
		{name: "single quote id", q: `launchpad = "'"`, traffic: outcome{message: `"'" is not a launchpad id`, position: 13}, launchpad: outcome{message: `"'" is not a launchpad id`, position: 13}},
		{name: "single quoted string id", q: `launchpad != '<one>'' OR 1=1 --'`, traffic: outcome{message: `unexpected "' OR 1=1 --'"`, position: 52}, launchpad: outcome{message: `unexpected "' OR 1=1 --'"`, position: 52}},
		{name: "backslash id", q: `launchpad = "\\"`, traffic: outcome{message: `"\\" is not a launchpad id`, position: 13}, launchpad: outcome{message: `"\\" is not a launchpad id`, position: 13}},
		{name: "stacked statement id", q: `launchpad = "<one>; DELETE FROM request; --"`, traffic: outcome{message: `is not a launchpad id`, position: 13}, launchpad: outcome{message: `is not a launchpad id`, position: 13}},
		{name: "comment id", q: `launchpad = "<one>/*"`, traffic: outcome{message: `is not a launchpad id`, position: 13}, launchpad: outcome{message: `is not a launchpad id`, position: 13}},
		{name: "sql tautology id", q: `launchpad = "') OR 1=1 --"`, traffic: outcome{message: `"') OR 1=1 --" is not a launchpad id`, position: 13}, launchpad: outcome{message: `"') OR 1=1 --" is not a launchpad id`, position: 13}},
		{name: "quote tautology id", q: `launchpad != "x\" OR \"1\"=\"1"`, traffic: outcome{message: `"x\" OR \"1\"=\"1" is not a launchpad id`, position: 14}, launchpad: outcome{message: `"x\" OR \"1\"=\"1" is not a launchpad id`, position: 14}},
		{name: "fts syntax id", q: `launchpad = "NEAR(*"`, traffic: outcome{message: `"NEAR(*" is not a launchpad id`, position: 13}, launchpad: outcome{message: `"NEAR(*" is not a launchpad id`, position: 13}},
		{name: "column filter id", q: `launchpad:"{request_body}:x"`, traffic: outcome{message: `operator : on launchpad takes only *`, position: 1}, launchpad: outcome{message: `operator : on launchpad takes only *`, position: 1}},
		{name: "non uuid id", q: `launchpad = "one"`, traffic: outcome{message: `"one" is not a launchpad id`, position: 13}, launchpad: outcome{message: `"one" is not a launchpad id`, position: 13}},
		{name: "uppercase uuid", q: `launchpad = "` + "<ONE>" + `"`, traffic: outcome{names: "orig,launched,shared,hostile"}, launchpad: outcome{names: "orig,launched,shared,hostile"}},
		{name: "unquoted uuid", q: `launchpad = 01937f5d-351a-7e68-936d-61a7a25661a3`, traffic: outcome{message: `01937 is not a valid number`, position: 13}, launchpad: outcome{message: `01937 is not a valid number`, position: 13}},
		{name: "unquoted word", q: `launchpad = nope`, traffic: outcome{message: `launchpad needs a quoted launchpad id`, position: 13}, launchpad: outcome{message: `launchpad needs a quoted launchpad id`, position: 13}},

		// (b) and (c) text terms with hostile literals, alone and with launchpad.
		{name: "single quote text", q: `"it's"`, traffic: outcome{names: "hostile,hostilefree"}, launchpad: outcome{names: "hostile"}},
		{name: "single quote text with launchpad", q: `launchpad:* AND "it's"`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "double quote tautology text", q: `response_body:"x\" OR \"1\"=\"1" AND launchpad != "<one>"`, traffic: outcome{names: "hostilefree"}, launchpad: outcome{names: ""}},
		{name: "double quote tautology text with launchpad", q: `launchpad = "<one>" response_body:"x\" OR \"1\"=\"1"`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "sql tautology text", q: `"') OR 1=1 --" AND launchpad:*`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "stacked statement text", q: `"; DROP TABLE request; --"`, traffic: outcome{names: "hostile,hostilefree"}, launchpad: outcome{names: "hostile"}},
		{name: "semicolon and dashes text", q: `NOT launchpad:* AND "and; DROP"`, traffic: outcome{names: "hostilefree"}, launchpad: outcome{names: ""}},
		{name: "comment text", q: `"/* c */" launchpad:*`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "backslash text", q: `"back\\slash" AND launchpad = "<one>"`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "near text", q: `"NEAR(alpha" launchpad:*`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "near text without match", q: `"NEAR(token" launchpad:*`, traffic: outcome{names: ""}, launchpad: outcome{names: ""}},
		{name: "prefix star text", q: `"tok*" AND launchpad:*`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "caret text", q: `"^caret" AND launchpad:*`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "column filter text", q: `"{request_body}:xyz" AND launchpad:*`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "column filter text without match", q: `"{request_body}:token"`, traffic: outcome{names: ""}, launchpad: outcome{names: ""}},
		{name: "quoted keywords text", q: `"\"AND OR NOT\"" AND launchpad:*`, traffic: outcome{names: "hostile"}, launchpad: outcome{names: "hostile"}},
		{name: "quoted keywords without match", q: `"token AND token" OR "token OR free"`, traffic: outcome{names: ""}, launchpad: outcome{names: ""}},
		{name: "unquoted fts syntax", q: `launchpad:* AND NEAR(`, traffic: outcome{message: `unexpected "("`, position: 21}, launchpad: outcome{message: `unexpected "("`, position: 21}},
		{name: "short quote text", q: `launchpad:* "'"`, traffic: outcome{message: `text term "'" is shorter than 3 characters`, position: 13}, launchpad: outcome{message: `text term "'" is shorter than 3 characters`, position: 13}},

		// The existing caps.
		{name: "length cap", q: strings.Repeat(" ", 8192) + `launchpad:*`, traffic: outcome{message: `query is longer than 8192 bytes`, position: 1}, launchpad: outcome{message: `query is longer than 8192 bytes`, position: 1}},
		{name: "depth cap", q: strings.Repeat(`launchpad:* AND `, 200) + `launchpad:*`, traffic: outcome{message: `query nests more than 200 conditions`, position: 1}, launchpad: outcome{message: `query nests more than 200 conditions`, position: 1}},
		{name: "depth within cap", q: strings.Repeat(`NOT launchpad:* OR `, 198) + `"it's"`, traffic: outcome{names: "free,hostile,hostilefree"}, launchpad: outcome{names: "hostile"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := strings.ReplaceAll(p.expand(test.q), "<ONE>", strings.ToUpper(p.launchpads["one"].String()))
			for _, route := range []struct {
				path string
				want outcome
			}{
				{path: "/traffic", want: test.traffic},
				{path: "/launchpad/" + p.launchpads["one"].String(), want: test.launchpad},
			} {
				code, body := p.get(route.path, q)
				if route.want.message != "" {
					assertInvalidQuery(t, route.path, code, body, route.want.message, route.want.position)
				} else {
					if code != http.StatusOK {
						t.Fatalf("\n%s wanted:\n200\ngot:\n%d %s", route.path, code, body)
					}
					if got := p.names(t, body); got != route.want.names {
						t.Fatalf("\n%s wanted:\n%s\ngot:\n%s", route.path, route.want.names, got)
					}
				}
				if after := p.counts(t); after != before {
					t.Fatalf("\n%s wanted row counts:\n%v\ngot:\n%v", route.path, before, after)
				}
			}
		})
	}
}

// assertInvalidQuery fails unless a response is a 400 invalid_query whose
// message contains message, at position.
func assertInvalidQuery(t *testing.T, path string, code int, body, message string, position int) {
	t.Helper()
	var payload struct {
		Error    string `json:"error"`
		Message  string `json:"message"`
		Position int    `json:"position"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if code != http.StatusBadRequest || payload.Error != "invalid_query" || !strings.Contains(payload.Message, message) || payload.Position != position {
		t.Fatalf("\n%s wanted:\n400 invalid_query containing %q at %d\ngot:\n%d %s", path, message, position, code, body)
	}
}
