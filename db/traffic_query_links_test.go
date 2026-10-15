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
