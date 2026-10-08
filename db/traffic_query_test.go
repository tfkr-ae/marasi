package db

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// queryPair describes one captured request/response pair for query tests.
type queryPair struct {
	name        string
	scheme      string
	method      string
	host        string
	path        string
	requestedAt time.Time
	metadata    map[string]any
	// statusCode 0 leaves the pair in flight, without a response.
	statusCode  int
	contentType string
}

// seedQueryPairs captures the pairs oldest first through the normal write
// operations and returns their ids by name.
func seedQueryPairs(t *testing.T, repo *Repository, pairs []queryPair) map[string]uuid.UUID {
	t.Helper()
	ids := make(map[string]uuid.UUID, len(pairs))
	for _, pair := range pairs {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("creating uuid: %v", err)
		}
		metadata := pair.metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		err = repo.InsertRequest(&domain.ProxyRequest{
			ID:          id,
			Scheme:      pair.scheme,
			Method:      pair.method,
			Host:        pair.host,
			Path:        pair.path,
			Raw:         []byte(pair.method + " " + pair.path + " HTTP/1.1\r\nHost: " + pair.host + "\r\n\r\n"),
			Metadata:    metadata,
			RequestedAt: pair.requestedAt,
		})
		if err != nil {
			t.Fatalf("inserting request %s: %v", pair.name, err)
		}
		if pair.statusCode != 0 {
			err = repo.InsertResponse(&domain.ProxyResponse{
				ID:          id,
				Status:      "status",
				StatusCode:  pair.statusCode,
				ContentType: pair.contentType,
				Length:      "0",
				Raw:         []byte("HTTP/1.1 200 OK\r\n\r\n"),
				Metadata:    metadata,
				RespondedAt: pair.requestedAt.Add(time.Second),
			})
			if err != nil {
				t.Fatalf("inserting response %s: %v", pair.name, err)
			}
		}
		ids[pair.name] = id
		time.Sleep(2 * time.Millisecond)
	}
	return ids
}

func queryTestPairs() []queryPair {
	plus4 := time.FixedZone("+04", 4*3600)
	minus5 := time.FixedZone("-05", -5*3600)
	return []queryPair{
		{
			name: "a", scheme: "https", method: "GET", host: "api.example.com", path: "/api/users?id=1",
			requestedAt: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
			metadata:    map[string]any{"extension": "workshop", "count": 5, "flag": true, "prettified-request": "x"},
			statusCode:  200, contentType: "application/json",
		},
		{
			name: "b", scheme: "https", method: "POST", host: "www.example.com", path: "/API/users",
			requestedAt: time.Date(2024, 1, 1, 14, 30, 0, 0, plus4), // 10:30Z
			metadata:    map[string]any{"extension": "other"},
			statusCode:  500, contentType: "text/html",
		},
		{
			name: "c", scheme: "http", method: "GET", host: "example.com", path: "/login",
			requestedAt: time.Date(2024, 1, 1, 6, 0, 0, 0, minus5), // 11:00Z
			statusCode:  404, contentType: "text/plain",
		},
		{
			name: "d", scheme: "https", method: "PUT", host: "Example.com", path: "/api/v2",
			requestedAt: time.Date(2024, 1, 1, 11, 30, 0, 500_000_000, time.UTC).Local(),
			metadata:    map[string]any{"nested": map[string]any{"k": "v"}},
			statusCode:  503, contentType: "application/json; charset=utf-8",
		},
		{
			name: "e", scheme: "https", method: "GET", host: "other.org", path: "/",
			requestedAt: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		},
	}
}

func TestTrafficRepo_ListTrafficQuery(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := seedQueryPairs(t, repo, queryTestPairs())

	tests := []struct {
		query string
		want  string // names newest first
	}{
		{query: ``, want: "edcba"},
		{query: `   `, want: "edcba"},

		{query: `host = "example.com"`, want: "c"},
		{query: `host = example.com`, want: "c"},
		{query: `host = "*.example.com"`, want: "ba"},
		{query: `host = *.example.com`, want: "ba"},
		{query: `host = "*example.com"`, want: "cba"},
		{query: `host = "*xample*"`, want: "dcba"},
		{query: `host = "*"`, want: "edcba"},
		{query: `host != "*.example.com"`, want: "edc"},
		{query: `host = "EXAMPLE.COM"`, want: ""},
		{query: `host = "%example%"`, want: ""},
		{query: `host = "_xample.com"`, want: ""},
		{query: `path = "/api/*"`, want: "da"},
		{query: `path = "/API/*"`, want: "b"},
		{query: `path = "*users*"`, want: "ba"},
		{query: `path = "/"`, want: "e"},
		{query: `method = "GET"`, want: "eca"},
		{query: `method = GET`, want: "eca"},
		{query: `method = "get"`, want: ""},
		{query: `method != "GET"`, want: "db"},
		{query: `scheme = "http"`, want: "c"},
		{query: `content_type = "application/json*"`, want: "da"},
		{query: `content_type = "*html"`, want: "b"},

		{query: `status_code = 500`, want: "b"},
		{query: `status_code != 500`, want: "edca"},
		{query: `status_code < 500`, want: "ca"},
		{query: `status_code <= 404`, want: "ca"},
		{query: `status_code > 404`, want: "db"},
		{query: `status_code >= 500`, want: "db"},

		{query: `requested_at >= "2024-01-01T10:30:00Z"`, want: "edcb"},
		{query: `requested_at < "2024-01-01T14:30:00+04:00"`, want: "a"},
		{query: `requested_at = "2024-01-01T10:30:00Z"`, want: "b"},
		{query: `requested_at != "2024-01-01T10:30:00Z"`, want: "edca"},
		{query: `requested_at > "2024-01-01T11:30:00Z" AND requested_at <= "2024-01-01T11:30:00.5Z"`, want: "d"},
		{query: `requested_at > timestamp("2024-01-01T11:00:00Z")`, want: "ed"},
		{query: `responded_at > "2024-01-01T10:00:30Z"`, want: "dcb"},
		{query: `NOT responded_at > "2024-01-01T10:00:30Z"`, want: "ea"},

		{query: `metadata.extension = "workshop"`, want: "a"},
		{query: `metadata.extension = workshop`, want: "a"},
		{query: `metadata.extension != "workshop"`, want: "edcb"},
		{query: `metadata.count = 5`, want: "a"},
		{query: `metadata.count = "5"`, want: ""},
		{query: `metadata.flag = true`, want: "a"},
		{query: `metadata.nested.k = "v"`, want: "d"},
		{query: `metadata."extension" = "other"`, want: "b"},
		{query: `metadata.missing = "x"`, want: ""},

		{query: `method = "GET" AND host = "other.org"`, want: "e"},
		{query: `method = "GET" host = "other.org"`, want: "e"},
		{query: `method = "GET" OR method = "PUT"`, want: "edca"},
		{query: `NOT method = "GET"`, want: "db"},
		{query: `-method = "GET"`, want: "db"},
		{query: `(method = "GET" OR method = "POST") AND status_code = 500`, want: "b"},
		{query: `NOT (method = "GET" OR method = "POST")`, want: "d"},
		// OR binds tighter than AND, so these read as POST AND (500 OR 404).
		{query: `method = "POST" AND status_code = 500 OR status_code = 404`, want: "b"},
		{query: `status_code = 404 OR status_code = 500 AND method = "POST"`, want: "b"},
		{query: `method = "POST" status_code = 500 OR status_code = 404`, want: "b"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			items, nextCursor, _, err := repo.ListTraffic(nil, 200, test.query)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			want := make([]uuid.UUID, 0, len(test.want))
			for _, name := range test.want {
				want = append(want, ids[string(name)])
			}
			if got := idsOf(items); !reflect.DeepEqual(got, want) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", namesOf(ids, want), namesOf(ids, got))
			}
			if nextCursor != nil {
				t.Fatalf("\nwanted:\nnil next_cursor\ngot:\n%v", nextCursor)
			}
		})
	}
}

func TestTrafficRepo_ListTrafficQueryPaging(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	ids := seedQueryPairs(t, repo, queryTestPairs())

	items, nextCursor, _, err := repo.ListTraffic(nil, 2, `method = "GET"`)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	if got, want := idsOf(items), []uuid.UUID{ids["e"], ids["c"]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
	}
	if nextCursor == nil || *nextCursor != ids["c"] {
		t.Fatalf("\nwanted:\n%v\ngot:\n%v", ids["c"], nextCursor)
	}

	older, olderNext, _, err := repo.ListTraffic(nextCursor, 2, `method = "GET"`)
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

// parserTokenName matches the AIP-160 library's token type names, which mean
// nothing to someone writing a query.
var parserTokenName = regexp.MustCompile(`\b(WS|TEXT|STRING|NUM|HEX)\b|trailing token`)

func TestTrafficRepo_ListTrafficQueryErrors(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	seedQueryPairs(t, repo, queryTestPairs()[:1])

	tests := []struct {
		query    string
		message  string
		position int
	}{
		{query: `stauts = 5`, message: `unknown field "stauts"`, position: 1},
		{query: `   stauts = 5`, message: `unknown field "stauts"`, position: 4},
		{query: `host = "a" AND stauts = 5`, message: `unknown field "stauts"`, position: 16},
		{query: `foo.bar = "x"`, message: `unknown field "foo.bar"`, position: 1},
		{query: `host.name = "x"`, message: `field "host" has no member "name"`, position: 1},
		{query: `host > "a"`, message: `operator > is not supported on host; use = or !=`, position: 1},
		{query: `host = 5`, message: `host needs a text value`, position: 8},
		{query: `status_code = "x"`, message: `status_code needs a whole number`, position: 15},
		{query: `status_code = 1.5`, message: `status_code needs a whole number`, position: 15},
		{query: `requested_at > "yesterday"`, message: `"yesterday" is not an RFC 3339 timestamp`, position: 16},
		{query: `requested_at > 5`, message: `requested_at needs an RFC 3339 timestamp`, position: 16},
		{query: `metadata = "x"`, message: `compare a metadata key`, position: 1},
		{query: `metadata.k > 5`, message: `operator > is not supported on metadata keys`, position: 1},
		{query: `foo(bar)`, message: `unknown function "foo"`, position: 1},
		{query: `(host = "a"`, message: `expected ")"`, position: 12},
		{query: `host = "a")`, message: `unexpected ")"`, position: 11},
		{query: `host = "a" AND`, message: `expected a condition after AND`, position: 12},
		{query: `host = "a" AND `, message: `expected a condition after AND`, position: 12},
		{query: `host = "a" OR`, message: `expected a condition after OR`, position: 12},
		{query: `host = "a" AND host = "b" AND`, message: `expected a condition after AND`, position: 27},
		{query: `NOT`, message: `expected a condition after NOT`, position: 1},
		{query: `host = "a" AND AND host = "b"`, message: `unexpected "AND"`, position: 16},
		{query: `status_code = 99999999999999999999`, message: `99999999999999999999 is not a valid number`, position: 15},
		{query: `status_code = 0xZZ`, message: `0x is not a valid number`, position: 15},
		{query: `status_code = 1e99`, message: `unexpected "e99"`, position: 16},
		{query: `host =`, message: `unexpected end of query`, position: 7},
		{query: `host = "unterminated`, message: `unterminated string`, position: 8},
		{query: `host = "é" AND status_code = "x"`, message: `status_code needs a whole number`, position: 30},
		{query: `host = "é" AND stauts = 5`, message: `unknown field "stauts"`, position: 16},
		{query: strings.Repeat(" ", maxQueryLength) + `host = "a"`, message: `query is longer than`, position: 1},
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
			if leaked := parserTokenName.FindString(queryErr.Message); leaked != "" {
				t.Fatalf("\nwanted:\nno parser token name in the message\ngot:\n%s (%s)", leaked, queryErr.Message)
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

func namesOf(ids map[string]uuid.UUID, got []uuid.UUID) string {
	var b strings.Builder
	for _, id := range got {
		for name, candidate := range ids {
			if candidate == id {
				b.WriteString(name)
			}
		}
	}
	return b.String()
}
