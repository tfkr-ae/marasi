package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// hostileBody holds literal SQL, FTS5, and quoting syntax, so a query that
// searches for it as text can only find it by matching it literally.
const hostileBody = `it's x" OR "1"="1 then ') OR 1=1 -- and; DROP TABLE request; -- /* c */ back\slash NEAR(alpha beta) tok* ^caret {request_body}:xyz "AND OR NOT" end`

// launchpadQueryPair is one captured pair for launchpad query tests.
type launchpadQueryPair struct {
	name       string
	statusCode int
	body       string
	metadata   map[string]any
	// launchpads names the launchpads the pair is linked to.
	launchpads []string
}

// launchpadQueryFixture is a project with two launchpads and pairs linked to
// none, one, or both of them.
type launchpadQueryFixture struct {
	repo       *Repository
	ids        map[string]uuid.UUID
	launchpads map[string]uuid.UUID
}

func setupLaunchpadQueryFixture(t *testing.T) *launchpadQueryFixture {
	t.Helper()
	repo, teardown := setupTestDB(t)
	t.Cleanup(teardown)

	f := &launchpadQueryFixture{repo: repo, ids: map[string]uuid.UUID{}, launchpads: map[string]uuid.UUID{}}
	for _, name := range []string{"one", "two"} {
		id, err := repo.CreateLaunchpad(name, "")
		if err != nil {
			t.Fatalf("creating launchpad %s: %v", name, err)
		}
		f.launchpads[name] = id
	}
	one := f.launchpads["one"].String()
	pairs := []launchpadQueryPair{
		// Sent to Launchpad from captured traffic: linked, but no
		// launchpad_id in its metadata.
		{name: "orig", statusCode: 200, body: "session token alpha", launchpads: []string{"one"}},
		{name: "launched", statusCode: 502, body: "upstream token failure", launchpads: []string{"one"},
			metadata: map[string]any{"launchpad": true, "launchpad_id": one}},
		{name: "shared", statusCode: 200, body: "shared token", launchpads: []string{"one", "two"}},
		{name: "other", statusCode: 503, body: "other token", launchpads: []string{"two"}},
		{name: "free", statusCode: 500, body: "free token"},
		{name: "hostile", statusCode: 200, body: hostileBody, launchpads: []string{"one"}},
		{name: "hostilefree", statusCode: 200, body: hostileBody},
	}
	for _, pair := range pairs {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}
		metadata := pair.metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		now := time.Now()
		err = repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "GET", Host: "lp.example.com", Path: "/" + pair.name,
			Raw:      []byte("GET /" + pair.name + " HTTP/1.1\r\nHost: lp.example.com\r\n\r\n"),
			Metadata: metadata, RequestedAt: now,
		})
		if err != nil {
			t.Fatalf("inserting request %s: %v", pair.name, err)
		}
		err = repo.InsertResponse(&domain.ProxyResponse{
			ID: id, Status: "status", StatusCode: pair.statusCode, ContentType: "text/plain", Length: "0",
			Raw:      []byte(fmt.Sprintf("HTTP/1.1 %d X\r\nContent-Type: text/plain\r\n\r\n%s", pair.statusCode, pair.body)),
			Metadata: metadata, RespondedAt: now.Add(time.Second),
		})
		if err != nil {
			t.Fatalf("inserting response %s: %v", pair.name, err)
		}
		for _, launchpad := range pair.launchpads {
			if err := repo.LinkRequestToLaunchpad(id, f.launchpads[launchpad]); err != nil {
				t.Fatalf("linking %s to %s: %v", pair.name, launchpad, err)
			}
		}
		f.ids[pair.name] = id
		time.Sleep(2 * time.Millisecond)
	}
	return f
}

// expand replaces <one> and <two> in a query with the launchpad ids.
func (f *launchpadQueryFixture) expand(query string) string {
	return strings.NewReplacer("<one>", f.launchpads["one"].String(), "<two>", f.launchpads["two"].String()).Replace(query)
}

// names returns the fixture names of items, in order, joined by commas.
func (f *launchpadQueryFixture) names(items []*domain.RequestResponseSummary) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		for name, id := range f.ids {
			if id == item.ID {
				names = append(names, name)
			}
		}
	}
	return strings.Join(names, ",")
}

// tableCounts returns the row counts of request, launchpad, and
// launchpad_request.
func tableCounts(t *testing.T, repo *Repository) [3]int {
	t.Helper()
	var counts [3]int
	for i, table := range []string{"request", "launchpad", "launchpad_request"} {
		if err := repo.dbConn.Get(&counts[i], "SELECT count(*) FROM "+table); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
	}
	return counts
}

func TestTrafficRepo_ListTrafficLaunchpadQuery(t *testing.T) {
	t.Parallel()
	f := setupLaunchpadQueryFixture(t)

	tests := []struct {
		query string
		want  string // names newest first
	}{
		{query: `launchpad = "<one>"`, want: "hostile,shared,launched,orig"},
		{query: `launchpad = "<two>"`, want: "other,shared"},
		{query: `launchpad != "<one>"`, want: "hostilefree,free,other"},
		{query: `launchpad:*`, want: "hostile,other,shared,launched,orig"},
		{query: `NOT launchpad:*`, want: "hostilefree,free"},
		{query: `launchpad = "01937f5d-351a-7e68-936d-61a7a25661a3"`, want: ""},
		{query: `launchpad != "01937f5d-351a-7e68-936d-61a7a25661a3"`, want: "hostilefree,hostile,free,other,shared,launched,orig"},

		{query: `launchpad:* AND response_body:"token"`, want: "other,shared,launched,orig"},
		{query: `launchpad = "<one>" AND status_code >= 500`, want: "launched"},
		{query: `launchpad != "<one>" AND response_body:"token" AND status_code >= 500`, want: "free,other"},
		{query: `launchpad != "<one>" AND launchpad != "<two>"`, want: "hostilefree,free"},
		{query: `launchpad = "<two>" OR status_code = 502`, want: "other,shared,launched"},
		{query: `launchpad = "<one>" AND launchpad = "<two>"`, want: "shared"},
		{query: `launchpad:* "token" -status_code = 200`, want: "other,launched"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			items, _, _, err := listPage(f.repo, nil, 200, f.expand(test.query))
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if got := f.names(items); got != test.want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", test.want, got)
			}
		})
	}
}

func TestTrafficRepo_ListTrafficLaunchpadQueryErrors(t *testing.T) {
	t.Parallel()
	f := setupLaunchpadQueryFixture(t)

	tests := []struct {
		query    string
		message  string
		position int
	}{
		{query: `launchpad = "nope"`, message: `"nope" is not a launchpad id`, position: 13},
		{query: `status_code = 200 AND launchpad != "<one>x"`, message: `is not a launchpad id`, position: 36},
		{query: `launchpad = nope`, message: `launchpad needs a quoted launchpad id`, position: 13},
		{query: `launchpad = 5`, message: `launchpad needs a quoted launchpad id`, position: 13},
		{query: `launchpad = "*"`, message: `"*" is not a launchpad id`, position: 13},
		{query: `launchpad > "<one>"`, message: `operator > is not supported on launchpad; use =, !=, or launchpad:*`, position: 1},
		{query: `launchpad <= "<one>"`, message: `operator <= is not supported on launchpad`, position: 1},
		{query: `launchpad:"<one>"`, message: `operator : on launchpad takes only *`, position: 1},
		{query: `launchpad:token`, message: `operator : on launchpad takes only *`, position: 1},
		{query: `launchpad.id = "<one>"`, message: `field "launchpad" has no member "id"`, position: 1},
		{query: `launchpad.id:*`, message: `field "launchpad" has no member "id"`, position: 1},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			_, _, _, err := listPage(f.repo, nil, 200, f.expand(test.query))
			assertQueryError(t, err, test.message, test.position)
		})
	}
}

func TestLaunchpadRepo_GetLaunchpadRequestsQuery(t *testing.T) {
	t.Parallel()
	f := setupLaunchpadQueryFixture(t)

	tests := []struct {
		launchpad string
		query     string
		want      string // names in entry order
	}{
		{launchpad: "one", query: ``, want: "orig,launched,shared,hostile"},
		{launchpad: "one", query: `   `, want: "orig,launched,shared,hostile"},
		{launchpad: "one", query: `response_body:"token"`, want: "orig,launched,shared"},
		{launchpad: "one", query: `status_code >= 500`, want: "launched"},
		{launchpad: "one", query: `"token" AND NOT status_code = 502`, want: "orig,shared"},
		{launchpad: "one", query: `launchpad = "<two>"`, want: "shared"},
		{launchpad: "one", query: `launchpad != "<two>"`, want: "orig,launched,hostile"},
		{launchpad: "one", query: `launchpad:*`, want: "orig,launched,shared,hostile"},
		{launchpad: "one", query: `metadata.launchpad_id = "<one>"`, want: "launched"},
		{launchpad: "two", query: `status_code >= 500 OR response_body:"shared"`, want: "shared,other"},
		{launchpad: "two", query: `launchpad != "<one>"`, want: "other"},
		{launchpad: "two", query: `response_body:"free"`, want: ""},
	}
	for _, test := range tests {
		t.Run(test.launchpad+" "+test.query, func(t *testing.T) {
			items, _, err := f.repo.GetLaunchpadRequests(f.launchpads[test.launchpad], f.expand(test.query), nil, "")
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if got := f.names(items); got != test.want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", test.want, got)
			}
		})
	}

	t.Run("should return an invalid query with its position", func(t *testing.T) {
		items, _, err := f.repo.GetLaunchpadRequests(f.launchpads["one"], `status_code = 200 AND stauts = 5`, nil, "")
		assertQueryError(t, err, `unknown field "stauts"`, 23)
		if items != nil {
			t.Fatalf("\nwanted:\nno items\ngot:\n%v", items)
		}
	})
}

// assertQueryError fails unless err is a *domain.QueryError whose message
// contains message, at position.
func assertQueryError(t *testing.T, err error, message string, position int) {
	t.Helper()
	var queryErr *domain.QueryError
	if !errors.As(err, &queryErr) {
		t.Fatalf("\nwanted:\n*domain.QueryError\ngot:\n%T %v", err, err)
	}
	if !strings.Contains(queryErr.Message, message) {
		t.Fatalf("\nwanted message containing:\n%s\ngot:\n%s", message, queryErr.Message)
	}
	if queryErr.Position != position {
		t.Fatalf("\nwanted position:\n%d\ngot:\n%d (%s)", position, queryErr.Position, queryErr.Message)
	}
}
