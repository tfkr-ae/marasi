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

func TestTrafficRepo_ListTraffic_Exclusion(t *testing.T) {
	t.Run("should fill every page to the limit when excluded pairs sit between matches", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		var kept []uuid.UUID
		for i := range 7 {
			kept = append(kept, insertTypedPair(t, repo, "application/json"))
			for range i % 3 {
				insertTypedPair(t, repo, "image/png")
			}
		}

		got := walkForward(t, repo, domain.TrafficQuery{Exclusion: []string{"image/png"}, Limit: 2})
		want := reversed(kept)
		if !reflect.DeepEqual(flatten(got), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, flatten(got))
		}
		for i, page := range got[:len(got)-1] {
			if len(page) != 2 {
				t.Fatalf("\nwanted:\npage %d with 2 items\ngot:\n%d", i, len(page))
			}
		}
	})

	t.Run("should keep pairs with no content type even when every type is excluded", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		_ = insertTypedPair(t, repo, "application/json")
		inFlight := insertTraffic(t, repo, "GET", "example.com", "/")
		_ = insertTypedPair(t, repo, "image/png")
		emptyType := insertTypedPair(t, repo, "")

		page, err := repo.ListTraffic(domain.TrafficQuery{Exclusion: []string{"application/*", "image/*", "text/plain"}, Limit: 200})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []uuid.UUID{emptyType, inFlight}
		if !reflect.DeepEqual(idsOf(page.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(page.Items))
		}
	})

	t.Run("should hide a whole major type and keep types that only share its prefix", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		_ = insertTypedPair(t, repo, "image/png")
		_ = insertTypedPair(t, repo, "image/svg+xml")
		prefixed := insertTypedPair(t, repo, "imagex/foo")
		json := insertTypedPair(t, repo, "application/json")

		page, err := repo.ListTraffic(domain.TrafficQuery{Exclusion: []string{"image/*"}, Limit: 200})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []uuid.UUID{json, prefixed}
		if !reflect.DeepEqual(idsOf(page.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(page.Items))
		}
	})

	t.Run("should match an exact exclusion regardless of its case and parameters", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		_ = insertTypedPair(t, repo, "text/html")
		css := insertTypedPair(t, repo, "text/css")

		page, err := repo.ListTraffic(domain.TrafficQuery{Exclusion: []string{"Text/HTML; charset=utf-8"}, Limit: 200})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := []uuid.UUID{css}; !reflect.DeepEqual(idsOf(page.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(page.Items))
		}
	})

	t.Run("should combine the exclusion with the query text", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		matching := insertTraffic(t, repo, "POST", "example.com", "/")
		insertTrafficResponse(t, repo, matching, 200)
		_ = insertTypedPair(t, repo, "image/png")
		other := insertTraffic(t, repo, "GET", "example.com", "/")
		insertTrafficResponse(t, repo, other, 200)

		page, err := repo.ListTraffic(domain.TrafficQuery{Text: `method = "POST" OR method = "GET"`, Exclusion: []string{"image/*"}, Limit: 200})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := []uuid.UUID{other, matching}; !reflect.DeepEqual(idsOf(page.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(page.Items))
		}
	})

	for _, value := range []string{"*/json", "ima*", "text/*html"} {
		t.Run("should reject the exclusion "+value+" and name it", func(t *testing.T) {
			repo, teardown := setupTestDB(t)
			defer teardown()

			_, err := repo.ListTraffic(domain.TrafficQuery{Exclusion: []string{"image/*", value}, Limit: 200})
			var queryErr *domain.QueryError
			if !errors.As(err, &queryErr) {
				t.Fatalf("\nwanted:\n*domain.QueryError\ngot:\n%v", err)
			}
			if !strings.Contains(queryErr.Message, `"`+value+`"`) {
				t.Fatalf("\nwanted:\na message naming %q\ngot:\n%s", value, queryErr.Message)
			}
		})
	}
}

func TestTrafficRepo_ListTraffic_AsOf(t *testing.T) {
	t.Run("should report the newest pair id as as_of when none is given", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		_ = insertTypedPair(t, repo, "text/plain")
		newest := insertTypedPair(t, repo, "text/plain")

		page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 1})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if page.AsOf == nil || *page.AsOf != newest {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", newest, page.AsOf)
		}
	})

	t.Run("should report a nil as_of for an empty project", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 200})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if page.AsOf != nil || len(page.Items) != 0 || page.NextCursor != nil || page.PrevCursor != nil {
			t.Fatalf("\nwanted:\nan empty page with no as_of or cursors\ngot:\n%+v", page)
		}
	})

	t.Run("should never include a pair inserted after as_of in pages carrying it", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		oldest := insertTypedPair(t, repo, "text/plain")
		middle := insertTypedPair(t, repo, "text/plain")
		newest := insertTypedPair(t, repo, "text/plain")

		first, err := repo.ListTraffic(domain.TrafficQuery{Limit: 2})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		_ = insertTypedPair(t, repo, "text/plain")

		again, err := repo.ListTraffic(domain.TrafficQuery{AsOf: first.AsOf, Limit: 2})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := []uuid.UUID{newest, middle}; !reflect.DeepEqual(idsOf(again.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(again.Items))
		}
		if again.PrevCursor != nil {
			t.Fatalf("\nwanted:\nnil prev_cursor\ngot:\n%v", *again.PrevCursor)
		}

		back, err := repo.ListTraffic(domain.TrafficQuery{AsOf: first.AsOf, Cursor: *again.NextCursor, Direction: domain.Backward, Limit: 5})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := []uuid.UUID{newest}; !reflect.DeepEqual(idsOf(back.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(back.Items))
		}

		next, err := repo.ListTraffic(domain.TrafficQuery{AsOf: first.AsOf, Cursor: *first.NextCursor, Limit: 2})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := []uuid.UUID{oldest}; !reflect.DeepEqual(idsOf(next.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(next.Items))
		}
		if next.AsOf == nil || *next.AsOf != *first.AsOf {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", *first.AsOf, next.AsOf)
		}
	})
}

func TestTrafficRepo_ListTraffic_Cursor(t *testing.T) {
	t.Run("should return exactly the rows before a forward page's cursor when walking backward", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		var ids []uuid.UUID
		for range 7 {
			ids = append(ids, insertTypedPair(t, repo, "text/plain"))
		}
		display := reversed(ids) // newest first

		first, err := repo.ListTraffic(domain.TrafficQuery{Limit: 3})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		second, err := repo.ListTraffic(domain.TrafficQuery{AsOf: first.AsOf, Cursor: *first.NextCursor, Limit: 3})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := display[3:6]; !reflect.DeepEqual(idsOf(second.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(second.Items))
		}
		if second.PrevCursor == nil {
			t.Fatal("\nwanted:\nprev_cursor\ngot:\nnil")
		}

		back, err := repo.ListTraffic(domain.TrafficQuery{AsOf: first.AsOf, Cursor: *second.PrevCursor, Direction: domain.Backward, Limit: 3})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := display[0:3]; !reflect.DeepEqual(idsOf(back.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(back.Items))
		}
		if back.PrevCursor != nil {
			t.Fatalf("\nwanted:\nnil prev_cursor at the start\ngot:\n%v", *back.PrevCursor)
		}
		if back.NextCursor == nil {
			t.Fatal("\nwanted:\nnext_cursor\ngot:\nnil")
		}

		short, err := repo.ListTraffic(domain.TrafficQuery{AsOf: first.AsOf, Cursor: *second.NextCursor, Direction: domain.Backward, Limit: 2})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := display[3:5]; !reflect.DeepEqual(idsOf(short.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(short.Items))
		}
		if short.PrevCursor == nil {
			t.Fatal("\nwanted:\nprev_cursor when more rows come before\ngot:\nnil")
		}

		again, err := repo.ListTraffic(domain.TrafficQuery{AsOf: first.AsOf, Cursor: *short.NextCursor, Limit: 3})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := display[5:7]; !reflect.DeepEqual(idsOf(again.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(again.Items))
		}
		if again.NextCursor != nil {
			t.Fatalf("\nwanted:\nnil next_cursor at the end\ngot:\n%v", *again.NextCursor)
		}
	})

	t.Run("should walk backward from the end when there is no cursor", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		var ids []uuid.UUID
		for range 5 {
			ids = append(ids, insertTypedPair(t, repo, "text/plain"))
		}

		page, err := repo.ListTraffic(domain.TrafficQuery{Direction: domain.Backward, Limit: 2})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := []uuid.UUID{ids[1], ids[0]}; !reflect.DeepEqual(idsOf(page.Items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(page.Items))
		}
		if page.NextCursor != nil {
			t.Fatalf("\nwanted:\nnil next_cursor at the end\ngot:\n%v", *page.NextCursor)
		}
		if page.PrevCursor == nil {
			t.Fatal("\nwanted:\nprev_cursor\ngot:\nnil")
		}
	})

	for _, cursor := range []string{"not a cursor", "0193802f-f0e7-73d9-a764-06d21e367809", "e30"} {
		t.Run("should reject the malformed cursor "+cursor, func(t *testing.T) {
			repo, teardown := setupTestDB(t)
			defer teardown()

			_, err := repo.ListTraffic(domain.TrafficQuery{Cursor: cursor, Limit: 200})
			if !errors.Is(err, domain.ErrInvalidCursor) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrInvalidCursor, err)
			}
		})
	}

	t.Run("should report the traffic index completeness", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		page, err := repo.ListTraffic(domain.TrafficQuery{Limit: 200})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if _, ok := page.Complete[domain.JobTrafficIndex]; !ok {
			t.Fatalf("\nwanted:\na %s flag\ngot:\n%v", domain.JobTrafficIndex, page.Complete)
		}
	})
}

// insertTypedPair inserts a responded pair with the given content type, one
// millisecond after the previous one so ids follow insertion order.
func insertTypedPair(t *testing.T, repo *Repository, contentType string) uuid.UUID {
	t.Helper()
	time.Sleep(time.Millisecond)
	id := insertTraffic(t, repo, "GET", "example.com", "/")
	resp := &domain.ProxyResponse{
		ID:          id,
		Status:      "200 OK",
		StatusCode:  200,
		ContentType: contentType,
		Length:      "0",
		Raw:         []byte{},
		Metadata:    map[string]any{},
		RespondedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := repo.InsertResponse(resp); err != nil {
		t.Fatalf("inserting response: %v", err)
	}
	return id
}

// walkForward pages through q from the start and returns each page's ids.
func walkForward(t *testing.T, repo *Repository, q domain.TrafficQuery) [][]uuid.UUID {
	t.Helper()
	var pages [][]uuid.UUID
	for {
		page, err := repo.ListTraffic(q)
		if err != nil {
			t.Fatalf("listing traffic: %v", err)
		}
		pages = append(pages, idsOf(page.Items))
		if page.NextCursor == nil {
			return pages
		}
		if len(pages) > 1000 {
			t.Fatal("walk did not end")
		}
		q.Cursor, q.AsOf = *page.NextCursor, page.AsOf
	}
}

func flatten(pages [][]uuid.UUID) []uuid.UUID {
	var ids []uuid.UUID
	for _, page := range pages {
		ids = append(ids, page...)
	}
	return ids
}

func reversed(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, len(ids))
	for i, id := range ids {
		out[len(ids)-1-i] = id
	}
	return out
}

// listPage lists one Forward page of text from cursor, a nil cursor starting
// at the newest pair, and returns its items, next cursor and index flag.
func listPage(repo *Repository, cursor *string, limit int, text string) ([]*domain.RequestResponseSummary, *string, bool, error) {
	q := domain.TrafficQuery{Text: text, Limit: limit}
	if cursor != nil {
		q.Cursor = *cursor
	}
	page, err := repo.ListTraffic(q)
	return page.Items, page.NextCursor, page.Complete[domain.JobTrafficIndex], err
}

func TestLaunchpadRepo_GetLaunchpadRequests_Exclusion(t *testing.T) {
	t.Run("should hide excluded entries and keep entries with no content type", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		launchpadID, err := repo.CreateLaunchpad("lp", "")
		if err != nil {
			t.Fatalf("creating launchpad: %v", err)
		}
		image := insertTypedPair(t, repo, "image/png")
		json := insertTypedPair(t, repo, "application/json")
		inFlight := insertTraffic(t, repo, "GET", "example.com", "/")
		_ = insertTypedPair(t, repo, "application/json") // not linked
		for _, id := range []uuid.UUID{image, json, inFlight} {
			if err := repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
				t.Fatalf("linking: %v", err)
			}
		}

		items, err := repo.GetLaunchpadRequests(launchpadID, "", []string{"image/*"}, "")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if want := []uuid.UUID{json, inFlight}; !reflect.DeepEqual(idsOf(items), want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, idsOf(items))
		}

		_, err = repo.GetLaunchpadRequests(launchpadID, "", []string{"ima*"}, "")
		var queryErr *domain.QueryError
		if !errors.As(err, &queryErr) {
			t.Fatalf("\nwanted:\n*domain.QueryError\ngot:\n%v", err)
		}
	})
}
