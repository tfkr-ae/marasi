package db

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

func TestTrafficRepo_ListTraffic_OrderBy(t *testing.T) {
	t.Run("should page through host asc in byte order with ties oldest first", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		b1 := insertSortPair(t, repo, sortPair{host: "b.example.com"})
		a1 := insertSortPair(t, repo, sortPair{host: "a.example.com"})
		upper := insertSortPair(t, repo, sortPair{host: "Z.example.com"})
		b2 := insertSortPair(t, repo, sortPair{host: "b.example.com"})
		a2 := insertSortPair(t, repo, sortPair{host: "a.example.com:8443"})

		got := flatten(walkForward(t, repo, domain.TrafficQuery{OrderBy: "host", Limit: 2}))
		want := []uuid.UUID{upper, a1, a2, b1, b2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should reject a cursor made under another order_by", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		for range 3 {
			insertSortPair(t, repo, sortPair{status: code(200)})
		}
		page, err := repo.ListTraffic(domain.TrafficQuery{OrderBy: "host", Limit: 1})
		if err != nil {
			t.Fatalf("listing traffic: %v", err)
		}
		for _, orderBy := range []string{"", "host desc", "path", "length"} {
			_, err := repo.ListTraffic(domain.TrafficQuery{OrderBy: orderBy, Cursor: *page.NextCursor, Limit: 1})
			if !errors.Is(err, domain.ErrInvalidCursor) {
				t.Fatalf("%q\nwanted:\n%v\ngot:\n%v", orderBy, domain.ErrInvalidCursor, err)
			}
		}
	})

	t.Run("should match length comparisons numerically, a pair with no response only by != and NOT", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		inFlight := insertSortPair(t, repo, sortPair{})
		noHeader := insertSortPair(t, repo, sortPair{status: code(200)})
		twenty := insertSortPair(t, repo, sortPair{status: code(200), length: "20"})
		hundred := insertSortPair(t, repo, sortPair{status: code(200), length: "100"})

		for text, want := range map[string][]uuid.UUID{
			"length = 20":      {twenty},
			"length != 20":     {hundred, noHeader, inFlight},
			"length < 100":     {twenty, noHeader},
			"length <= 100":    {hundred, twenty, noHeader},
			"length > 20":      {hundred},
			"length >= 0":      {hundred, twenty, noHeader},
			"NOT length >= 0":  {inFlight},
			"NOT length = 100": {twenty, noHeader, inFlight},
		} {
			page, err := repo.ListTraffic(domain.TrafficQuery{Text: text, Limit: 10})
			if err != nil {
				t.Fatalf("%s: %v", text, err)
			}
			if got := idsOf(page.Items); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s\nwanted:\n%v\ngot:\n%v", text, want, got)
			}
		}
		_, err := repo.ListTraffic(domain.TrafficQuery{Text: `length = "big"`, Limit: 10})
		var queryErr *domain.QueryError
		if !errors.As(err, &queryErr) {
			t.Fatalf("\nwanted:\n*domain.QueryError\ngot:\n%v", err)
		}
	})
}

func TestTrafficRepo_ListTraffic_OrderByEveryKey(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	pairs := []sortPair{
		{method: "POST", host: "b.example.com", path: "/b?x=1", status: code(200), contentType: "text/html", length: "100"},
		{method: "GET", host: "a.example.com", path: "/a", status: code(404), contentType: "application/json", length: "20"},
		{method: "PUT", host: "a.example.com:8443", path: "/a", status: code(200), contentType: "text/html", length: "3"},
		{method: "GET", host: "c.example.com", path: "/", status: nil},
		{method: "DELETE", host: "b.example.com", path: "/b", status: code(500), contentType: "", length: ""},
		{method: "GET", host: "B.example.com", path: "/B", status: code(301), contentType: "text/plain", length: "20"},
		{method: "GET", host: "a.example.com", path: "/z", status: nil},
		{method: "PATCH", host: "d.example.com", path: "/a/b", status: code(200), contentType: "image/png", length: "100000"},
		{method: "GET", host: "a.example.com", path: "/a", status: code(204), contentType: "application/json", length: "0"},
	}
	ids := make([]uuid.UUID, len(pairs))
	for i, p := range pairs {
		ids[i] = insertSortPair(t, repo, p)
	}

	// want lists the pair indexes in display order, from the spec: values
	// in byte or numeric order with ties oldest first ascending and newest
	// first descending, and pairs with no value last, by id in the sort's
	// direction. Pair 4 responded with no type and no Content-Length: no
	// type, length 0.
	want := map[string][]int{
		"requested_at":      {0, 1, 2, 3, 4, 5, 6, 7, 8},
		"requested_at desc": {8, 7, 6, 5, 4, 3, 2, 1, 0},
		"method":            {4, 1, 3, 5, 6, 8, 7, 0, 2},
		"method desc":       {2, 0, 7, 8, 6, 5, 3, 1, 4},
		"host":              {5, 1, 6, 8, 2, 0, 4, 3, 7},
		"host desc":         {7, 3, 4, 0, 2, 8, 6, 1, 5},
		"path":              {3, 5, 1, 2, 8, 7, 4, 0, 6},
		"path desc":         {6, 0, 4, 7, 8, 2, 1, 5, 3},
		"status_code":       {0, 2, 7, 8, 5, 1, 4, 3, 6},
		"status_code desc":  {4, 1, 5, 8, 7, 2, 0, 6, 3},
		"content_type":      {1, 8, 7, 0, 2, 5, 3, 4, 6},
		"content_type desc": {5, 2, 0, 7, 8, 1, 6, 4, 3},
		"length":            {4, 8, 2, 1, 5, 0, 7, 3, 6},
		"length desc":       {7, 0, 5, 1, 2, 8, 4, 6, 3},
	}
	for orderBy, order := range want {
		wantIDs := make([]uuid.UUID, len(order))
		for i, index := range order {
			wantIDs[i] = ids[index]
		}
		for _, limit := range []int{1, 2, 4, 200} {
			t.Run(orderBy+" forward", func(t *testing.T) {
				got := flatten(walkForward(t, repo, domain.TrafficQuery{OrderBy: orderBy, Limit: limit}))
				if !reflect.DeepEqual(got, wantIDs) {
					t.Fatalf("limit %d\nwanted:\n%v\ngot:\n%v", limit, wantIDs, got)
				}
			})
			t.Run(orderBy+" backward", func(t *testing.T) {
				got := flatten(walkBackward(t, repo, domain.TrafficQuery{OrderBy: orderBy, Limit: limit}))
				if !reflect.DeepEqual(got, reversed(wantIDs)) {
					t.Fatalf("limit %d\nwanted:\n%v\ngot:\n%v", limit, reversed(wantIDs), got)
				}
			})
		}
	}
}

func TestTrafficRepo_ListTraffic_OrderByDuringCapture(t *testing.T) {
	for _, orderBy := range []string{"method", "host desc", "path", "status_code", "status_code desc", "content_type", "content_type desc", "length", "length desc"} {
		t.Run("should return each pair at most once and none after as_of under "+orderBy, func(t *testing.T) {
			repo, teardown := setupTestDB(t)
			defer teardown()

			var pending []uuid.UUID
			for i := range 6 {
				insertSortPair(t, repo, sortPair{host: fmt.Sprintf("h%d.example.com", i%3), status: code(200 + i), contentType: "text/html", length: fmt.Sprint(10 * i)})
				pending = append(pending, insertSortPair(t, repo, sortPair{host: fmt.Sprintf("h%d.example.com", i%2)}))
			}

			q := domain.TrafficQuery{OrderBy: orderBy, Limit: 2}
			seen := map[uuid.UUID]bool{}
			var late []uuid.UUID
			for pages := 0; ; pages++ {
				page, err := repo.ListTraffic(q)
				if err != nil {
					t.Fatalf("listing traffic: %v", err)
				}
				for _, item := range page.Items {
					if seen[item.ID] {
						t.Fatalf("\nwanted:\neach pair once\ngot:\n%s twice", item.ID)
					}
					seen[item.ID] = true
				}
				// Between pages, a pending pair gets its response and a
				// new pair arrives.
				if len(pending) > 0 {
					insertTrafficResponse(t, repo, pending[0], 201)
					pending = pending[1:]
				}
				late = append(late, insertSortPair(t, repo, sortPair{host: "a.example.com", status: code(100), contentType: "a/a", length: "1"}))
				if page.NextCursor == nil {
					break
				}
				if pages > 100 {
					t.Fatal("walk did not end")
				}
				q.Cursor, q.AsOf = *page.NextCursor, page.AsOf
			}
			for _, id := range late {
				if seen[id] {
					t.Fatalf("\nwanted:\nno pair newer than as_of\ngot:\n%s", id)
				}
			}
		})
	}
}

// walkBackward pages Backward through q from the end and returns each
// page's ids, last page first, each page reversed, so the result is the
// display order reversed.
func walkBackward(t *testing.T, repo *Repository, q domain.TrafficQuery) [][]uuid.UUID {
	t.Helper()
	q.Direction = domain.Backward
	var pages [][]uuid.UUID
	for {
		page, err := repo.ListTraffic(q)
		if err != nil {
			t.Fatalf("listing traffic: %v", err)
		}
		pages = append(pages, reversed(idsOf(page.Items)))
		if page.PrevCursor == nil {
			return pages
		}
		if len(pages) > 1000 {
			t.Fatal("walk did not end")
		}
		q.Cursor, q.AsOf = *page.PrevCursor, page.AsOf
	}
}

// sortPair is a pair for sort tests. A nil status means the pair gets no
// response. length is the response's Content-Length, "" for none.
type sortPair struct {
	method, host, path string
	status             *int
	contentType        string
	length             string
}

func code(n int) *int { return &n }

// insertSortPair inserts p one millisecond after the previous pair, so ids
// follow insertion order.
func insertSortPair(t *testing.T, repo *Repository, p sortPair) uuid.UUID {
	t.Helper()
	time.Sleep(time.Millisecond)
	if p.method == "" {
		p.method = "GET"
	}
	if p.host == "" {
		p.host = "example.com"
	}
	if p.path == "" {
		p.path = "/"
	}
	id := insertTraffic(t, repo, p.method, p.host, p.path)
	if p.status == nil {
		return id
	}
	resp := &domain.ProxyResponse{
		ID:          id,
		Status:      "OK",
		StatusCode:  *p.status,
		ContentType: p.contentType,
		Length:      p.length,
		Raw:         []byte{},
		Metadata:    map[string]any{},
		RespondedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := repo.InsertResponse(resp); err != nil {
		t.Fatalf("inserting response: %v", err)
	}
	return id
}
