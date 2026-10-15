package db

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// linkQueryFixture is a project with two findings, two test cases and some
// notes, and pairs linked to none, one, or several of them.
type linkQueryFixture struct {
	repo    *Repository
	ids     map[string]uuid.UUID
	targets map[string]uuid.UUID
}

func setupLinkQueryFixture(t *testing.T) *linkQueryFixture {
	t.Helper()
	repo, teardown := setupTestDB(t)
	t.Cleanup(teardown)

	f := &linkQueryFixture{repo: repo, ids: map[string]uuid.UUID{}, targets: map[string]uuid.UUID{}}
	for _, name := range []string{"tc1", "tc2"} {
		id := uuid.Must(uuid.NewV7())
		if err := repo.SaveTestCase(&domain.TestCase{ID: id, Title: name}); err != nil {
			t.Fatalf("saving test case %s: %v", name, err)
		}
		f.targets[name] = id
	}
	for _, name := range []string{"f1", "f2"} {
		id := uuid.Must(uuid.NewV7())
		if err := repo.SaveFinding(&domain.Finding{ID: id, Title: name}); err != nil {
			t.Fatalf("saving finding %s: %v", name, err)
		}
		f.targets[name] = id
	}

	pairs := []struct {
		name       string
		host       string
		statusCode int
		note       string
		links      []string
	}{
		{name: "a", host: "a.example.com", statusCode: 200, links: []string{"f1", "tc1"}},
		{name: "b", host: "b.example.com", statusCode: 500, links: []string{"f1", "f2"}, note: "needs a retest"},
		{name: "c", host: "a.example.com", statusCode: 200, links: []string{"tc1", "tc2"}},
		{name: "d", host: "d.example.com", statusCode: 404, note: "token leaks here"},
		{name: "e", host: "e.example.com", statusCode: 200},
	}
	for _, pair := range pairs {
		id := uuid.Must(uuid.NewV7())
		now := time.Now()
		err := repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "GET", Host: pair.host, Path: "/" + pair.name,
			Raw:      []byte("GET /" + pair.name + " HTTP/1.1\r\nHost: " + pair.host + "\r\n\r\n"),
			Metadata: map[string]any{}, RequestedAt: now,
		})
		if err != nil {
			t.Fatalf("inserting request %s: %v", pair.name, err)
		}
		err = repo.InsertResponse(&domain.ProxyResponse{
			ID: id, Status: "status", StatusCode: pair.statusCode, ContentType: "text/plain", Length: "0",
			Raw:      []byte(fmt.Sprintf("HTTP/1.1 %d X\r\nContent-Type: text/plain\r\n\r\nbody of %s", pair.statusCode, pair.name)),
			Metadata: map[string]any{}, RespondedAt: now.Add(time.Second),
		})
		if err != nil {
			t.Fatalf("inserting response %s: %v", pair.name, err)
		}
		for _, link := range pair.links {
			switch link[0] {
			case 'f':
				err = repo.LinkRequestToFinding(f.targets[link], id)
			default:
				err = repo.LinkRequestToTestCase(f.targets[link], id)
			}
			if err != nil {
				t.Fatalf("linking %s to %s: %v", pair.name, link, err)
			}
		}
		if pair.note != "" {
			if err := repo.UpdateNote(id, pair.note); err != nil {
				t.Fatalf("noting %s: %v", pair.name, err)
			}
		}
		f.ids[pair.name] = id
		time.Sleep(2 * time.Millisecond)
	}
	return f
}

// expand replaces <name> in a query with the id of the finding or test case
// called name.
func (f *linkQueryFixture) expand(query string) string {
	for name, id := range f.targets {
		query = strings.ReplaceAll(query, "<"+name+">", id.String())
	}
	return query
}

func (f *linkQueryFixture) names(items []*domain.RequestResponseSummary) string {
	var b strings.Builder
	for _, item := range items {
		for name, id := range f.ids {
			if id == item.ID {
				b.WriteString(name)
			}
		}
	}
	return b.String()
}

func TestTrafficRepo_ListTrafficLinkQuery(t *testing.T) {
	f := setupLinkQueryFixture(t)

	tests := []struct {
		query string
		want  string // names newest first
	}{
		{query: `finding = "<f1>"`, want: "ba"},
		{query: `finding = "<f2>"`, want: "b"},
		{query: `finding != "<f1>"`, want: "edc"},
		{query: `finding:*`, want: "ba"},
		{query: `NOT finding:*`, want: "edc"},
		{query: `finding = "01937f5d-351a-7e68-936d-61a7a25661a3"`, want: ""},
		{query: `test_case = "<tc1>"`, want: "ca"},
		{query: `test_case != "<tc1>"`, want: "edb"},
		{query: `test_case:*`, want: "ca"},
		{query: `NOT test_case:*`, want: "edb"},
		{query: `finding = "<f1>" AND test_case = "<tc1>"`, want: "a"},
		{query: `finding:* AND status_code >= 500`, want: "b"},
		{query: `finding = "<f2>" OR test_case = "<tc2>"`, want: "cb"},

		{query: `note:*`, want: "db"},
		{query: `NOT note:*`, want: "eca"},
		{query: `-note:*`, want: "eca"},
		{query: `note:* AND status_code = 404`, want: "d"},
		{query: `note:"token"`, want: "d"},
		{query: `note:"retest" OR finding = "<f1>"`, want: "ba"},

		// ORs whose branches can each be read through an index.
		{query: `host = "d.example.com" OR note:"retest"`, want: "db"},
		{query: `host = "a.example.com" OR status_code >= 500 OR note:*`, want: "dcba"},
		{query: `(host = "a.example.com" OR note:"token") AND status_code = 200`, want: "ca"},
		{query: `status_code = 200 AND (finding = "<f1>" OR test_case:*)`, want: "ca"},
		{query: `host = "e.example.com" OR (finding:* AND status_code = 500)`, want: "eb"},
		{query: `host = "e.example.com" OR "body of c" OR test_case = "<tc2>"`, want: "ec"},
		{query: `NOT (host = "a.example.com" OR note:*)`, want: "e"},
		// ORs with a branch no index can read keep their form.
		{query: `host = "e.example.com" OR NOT note:*`, want: "eca"},
		{query: `host != "a.example.com" OR finding = "<f1>"`, want: "edba"},
		{query: `host = "*.example.com" OR note:*`, want: "edcba"},
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

func TestTrafficRepo_ListTrafficLinkQueryPaging(t *testing.T) {
	f := setupLinkQueryFixture(t)
	query := f.expand(`host = "a.example.com" OR note:* OR finding = "<f2>"`)

	var got string
	var cursor *string
	for range 10 {
		items, next, _, err := listPage(f.repo, cursor, 1, query)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		got += f.names(items) + "|"
		if next == nil {
			break
		}
		cursor = next
	}
	if want := "d|c|b|a|"; got != want {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
	}

	after := f.ids["b"]
	count, err := f.repo.CountTraffic(domain.TrafficCountQuery{Text: query, After: &after})
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	if count.Count != 2 {
		t.Fatalf("\nwanted:\n2 matches after b\ngot:\n%d", count.Count)
	}
}

func TestTrafficRepo_CountTrafficTextAfter(t *testing.T) {
	f := setupLinkQueryFixture(t)
	// A note rewrites a's index entry after the others were written.
	if err := f.repo.UpdateNote(f.ids["a"], "late note"); err != nil {
		t.Fatalf("noting a: %v", err)
	}

	tests := []struct {
		query string
		after string
		want  int
	}{
		{query: `"body of"`, after: "a", want: 4},
		{query: `"body of"`, after: "c", want: 2},
		{query: `"body of"`, after: "e", want: 0},
		{query: `"body of a"`, after: "a", want: 0},
		{query: `NOT "body of b"`, after: "a", want: 3},
		{query: `note:"note"`, after: "a", want: 0},
		{query: `"body of" AND NOT note:*`, after: "b", want: 2},
		{query: `host = "a.example.com" OR "body of d"`, after: "a", want: 2},
	}
	for _, test := range tests {
		t.Run(test.query+" after "+test.after, func(t *testing.T) {
			after := f.ids[test.after]
			got, err := f.repo.CountTraffic(domain.TrafficCountQuery{Text: test.query, After: &after})
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if got.Count != test.want {
				t.Fatalf("\nwanted:\n%d\ngot:\n%d", test.want, got.Count)
			}
		})
	}
}

func TestTrafficRepo_ListTrafficLinkQueryErrors(t *testing.T) {
	f := setupLinkQueryFixture(t)

	tests := []struct {
		query    string
		message  string
		position int
	}{
		{query: `finding = "nope"`, message: `"nope" is not a finding id`, position: 11},
		{query: `finding = nope`, message: `finding needs a quoted finding id`, position: 11},
		{query: `finding = 5`, message: `finding needs a quoted finding id`, position: 11},
		{query: `finding > "<f1>"`, message: `operator > is not supported on finding; use =, !=, or finding:*`, position: 1},
		{query: `finding:"<f1>"`, message: `operator : on finding takes only *`, position: 1},
		{query: `finding.id = "<f1>"`, message: `field "finding" has no member "id"`, position: 1},
		{query: `status_code = 200 AND test_case = "x"`, message: `"x" is not a test case id`, position: 35},
		{query: `test_case = x`, message: `test_case needs a quoted test case id`, position: 13},
		{query: `test_case >= "<tc1>"`, message: `operator >= is not supported on test_case; use =, !=, or test_case:*`, position: 1},
		{query: `test_case:token`, message: `operator : on test_case takes only *`, position: 1},
		// Only an unquoted * means "has a note"; anything else is text.
		{query: `note:"*"`, message: `text term "*" is shorter than 3 characters`, position: 6},
		{query: `note:ab`, message: `text term "ab" is shorter than 3 characters`, position: 6},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			_, _, _, err := listPage(f.repo, nil, 200, f.expand(test.query))
			assertQueryError(t, err, test.message, test.position)
		})
	}
}

// v7At returns a UUIDv7 whose timestamp is at.
func v7At(t *testing.T, at time.Time) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	ms := uint64(at.UnixMilli())
	for i := range 6 {
		id[i] = byte(ms >> (8 * (5 - i)))
	}
	return id
}

func TestTrafficRepo_ListTrafficRequestedAtBounds(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	pairs := []struct {
		name string
		// minted is when the pair's id was made, requested when its request
		// was sent. A launch's id is made before its request is sent.
		minted, requested time.Time
	}{
		{name: "before", minted: base.Add(-2 * time.Minute), requested: base.Add(-2 * time.Minute)},
		{name: "launch", minted: base.Add(-30 * time.Minute), requested: base.Add(time.Minute)},
		{name: "at", minted: base, requested: base},
		{name: "after", minted: base.Add(time.Hour), requested: base.Add(time.Hour)},
	}
	ids := map[string]uuid.UUID{}
	for _, pair := range pairs {
		id := v7At(t, pair.minted)
		err := repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "GET", Host: "t.example.com", Path: "/" + pair.name,
			Raw: []byte("GET / HTTP/1.1\r\n\r\n"), Metadata: map[string]any{}, RequestedAt: pair.requested,
		})
		if err != nil {
			t.Fatalf("inserting %s: %v", pair.name, err)
		}
		ids[pair.name] = id
	}

	tests := []struct {
		query string
		want  string // names, newest id first
	}{
		{query: `requested_at >= "2026-10-01T12:00:00Z"`, want: "afteratlaunch"},
		{query: `requested_at > "2026-10-01T12:00:00Z"`, want: "afterlaunch"},
		{query: `requested_at = "2026-10-01T12:01:00Z"`, want: "launch"},
		{query: `requested_at >= "2026-10-01T12:00:00Z" AND requested_at < "2026-10-01T12:30:00Z"`, want: "atlaunch"},
		{query: `NOT requested_at >= "2026-10-01T12:00:00Z"`, want: "before"},
		{query: `path = "/before"`, want: "before"},
		{query: `requested_at >= "2026-10-01T12:00:00Z" OR path = "/before"`, want: "afteratbeforelaunch"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			items, _, _, err := listPage(repo, nil, 200, test.query)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			got := make([]uuid.UUID, len(items))
			for i, item := range items {
				got[i] = item.ID
			}
			if names := namesOf(ids, got); names != test.want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", test.want, names)
			}
		})
	}
}
