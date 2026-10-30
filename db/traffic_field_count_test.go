package db

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tfkr-ae/marasi/domain"
)

// fieldCounts flattens counts to value=count pairs in the order returned.
func fieldCounts(counts domain.TrafficFieldCounts) []domain.TrafficFieldCount {
	var flat []domain.TrafficFieldCount
	for _, c := range counts.Counts {
		flat = append(flat, *c)
	}
	return flat
}

// insertSizedResponse writes a response with the status code and length to
// the pair id.
func insertSizedResponse(t *testing.T, repo *Repository, id uuid.UUID, statusCode int, length string) {
	t.Helper()
	err := repo.InsertResponse(&domain.ProxyResponse{
		ID:          id,
		Status:      "OK",
		StatusCode:  statusCode,
		ContentType: "text/html",
		Length:      length,
		Raw:         []byte{},
		Metadata:    map[string]any{},
		RespondedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("inserting response: %v", err)
	}
}

func TestTrafficRepo_CountTrafficByField(t *testing.T) {
	t.Parallel()
	t.Run("should count the pairs that match the query by status code, with pending pairs under an empty value", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		for _, code := range []int{404, 404, 404, 403, 500} {
			insertTrafficResponse(t, repo, insertTraffic(t, repo, "GET", "target.example", "/"), code)
		}
		insertTraffic(t, repo, "GET", "target.example", "/")
		insertTrafficResponse(t, repo, insertTraffic(t, repo, "GET", "other.example", "/"), 404)

		got, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Text: `host = "target.example"`, Field: "status_code"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []domain.TrafficFieldCount{{Value: "404", Count: 3}, {Value: "403", Count: 1}, {Value: "500", Count: 1}, {Value: "", Count: 1}}
		if !slices.Equal(fieldCounts(got), want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, fieldCounts(got))
		}
	})

	t.Run("should count the pairs that match the query by length, as numbers", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		for _, length := range []string{"1245", "1245", "98", "1250", "1245", "98"} {
			insertSizedResponse(t, repo, insertTraffic(t, repo, "GET", "target.example", "/"), 404, length)
		}
		insertTraffic(t, repo, "GET", "target.example", "/")
		insertSizedResponse(t, repo, insertTraffic(t, repo, "POST", "target.example", "/"), 404, "1245")

		got, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Text: `method = "GET"`, Field: "length"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		// 98 sorts before 1245 and 1250 as a number, not as text.
		want := []domain.TrafficFieldCount{{Value: "1245", Count: 3}, {Value: "98", Count: 2}, {Value: "1250", Count: 1}, {Value: "", Count: 1}}
		if !slices.Equal(fieldCounts(got), want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, fieldCounts(got))
		}
	})

	t.Run("should return no counts, not nil, when no pair matches", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		insertTrafficResponse(t, repo, insertTraffic(t, repo, "GET", "target.example", "/"), 200)

		got, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Text: `host = "absent.example"`, Field: "status_code"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if got.Counts == nil || len(got.Counts) != 0 {
			t.Fatalf("\nwanted:\nan empty, non-nil slice\ngot:\n%#v", got.Counts)
		}
	})

	t.Run("should refuse a field it cannot count by", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		for _, field := range []string{"host", "", "Status_Code", "nullif(status_code, -1)"} {
			_, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Field: field})
			if !errors.Is(err, domain.ErrInvalidCountField) {
				t.Fatalf("field %q:\nwanted:\n%v\ngot:\n%v", field, domain.ErrInvalidCountField, err)
			}
		}
	})

	t.Run("should refuse an invalid query as the traffic list does, not count it as empty", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		insertTrafficResponse(t, repo, insertTraffic(t, repo, "GET", "target.example", "/"), 200)

		for _, text := range []string{`status_code = "ok"`, `nosuchfield = 1`, `status_code =`} {
			_, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Text: text, Field: "status_code"})
			var queryErr *domain.QueryError
			if !errors.As(err, &queryErr) {
				t.Fatalf("query %q:\nwanted:\na *domain.QueryError\ngot:\n%v", text, err)
			}
			if want := ValidateTrafficQuery(text, nil, ""); want == nil || want.Error() != err.Error() {
				t.Fatalf("query %q:\nwanted:\nthe traffic list's error %v\ngot:\n%v", text, want, err)
			}
		}
	})

	t.Run("should count a text term over every captured pair and report the jobs it depends on", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		insertTrafficResponse(t, repo, insertTraffic(t, repo, "GET", "target.example", "/"), 200)
		needle := uuid.Must(uuid.NewV7())
		err := repo.InsertRequest(&domain.ProxyRequest{
			ID: needle, Scheme: "https", Method: "GET", Host: "target.example", Path: "/needle",
			Raw:         []byte("GET /needle HTTP/1.1\r\nHost: target.example\r\n\r\n"),
			Metadata:    map[string]any{},
			RequestedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("inserting request: %v", err)
		}
		insertTrafficResponse(t, repo, needle, 403)

		got, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Text: `request_head:"needle"`, Field: "status_code"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []domain.TrafficFieldCount{{Value: "403", Count: 1}}
		if !slices.Equal(fieldCounts(got), want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, fieldCounts(got))
		}
		if _, ok := got.Complete[domain.JobTrafficIndex]; !ok {
			t.Fatalf("\nwanted:\na %s flag\ngot:\n%v", domain.JobTrafficIndex, got.Complete)
		}
	})

	t.Run("should count only pairs at or below as_of that survive the exclusion", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		insertSizedResponse(t, repo, insertTraffic(t, repo, "GET", "target.example", "/"), 404, "10")
		insertTypedPair(t, repo, "image/png")
		asOf := insertTraffic(t, repo, "GET", "target.example", "/")
		insertTrafficResponse(t, repo, insertTraffic(t, repo, "GET", "target.example", "/"), 500)

		got, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Exclusion: []string{"image/*"}, AsOf: &asOf, Field: "status_code"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []domain.TrafficFieldCount{{Value: "404", Count: 1}, {Value: "", Count: 1}}
		if !slices.Equal(fieldCounts(got), want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, fieldCounts(got))
		}
	})
}

// The Armory groups a run's traffic by counting the pairs with its
// armory_run_id metadata, so that query must find a run's pairs through an
// index rather than read every pair in the project.
func TestTrafficRepo_CountTrafficByFieldForAnArmoryRun(t *testing.T) {
	t.Parallel()
	repo, teardown := setupTestDB(t)
	defer teardown()

	run := uuid.Must(uuid.NewV7())
	for _, code := range []int{404, 404, 403} {
		insertTrafficResponse(t, repo, testRequest(t, repo, map[string]any{"armory_run_id": run}), code)
	}
	testRequest(t, repo, map[string]any{"armory_run_id": run})
	insertTrafficResponse(t, repo, testRequest(t, repo, map[string]any{"armory_run_id": uuid.Must(uuid.NewV7())}), 404)
	insertTrafficResponse(t, repo, testRequest(t, repo, map[string]any{"armory_run_id": 7}), 404)
	for range 200 {
		insertTrafficResponse(t, repo, testRequest(t, repo, nil), 200)
	}
	text := `metadata.armory_run_id = "` + run.String() + `"`

	t.Run("should count only the run's pairs", func(t *testing.T) {
		got, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Text: text, Field: "status_code"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []domain.TrafficFieldCount{{Value: "404", Count: 2}, {Value: "403", Count: 1}, {Value: "", Count: 1}}
		if !slices.Equal(fieldCounts(got), want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, fieldCounts(got))
		}
	})

	t.Run("should count a number id only when the query names a number", func(t *testing.T) {
		got, err := repo.CountTrafficByField(domain.TrafficFieldCountQuery{Text: `metadata.armory_run_id = 7`, Field: "status_code"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []domain.TrafficFieldCount{{Value: "404", Count: 1}}
		if !slices.Equal(fieldCounts(got), want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, fieldCounts(got))
		}
	})

	for _, narrowed := range []string{text, text + ` AND status_code = 403`, text + ` AND length = 0`} {
		for field, key := range countFields {
			t.Run("should read "+narrowed+" by "+field+" through the run index", func(t *testing.T) {
				match, err := newTrafficMatch(narrowed, nil)
				if err != nil {
					t.Fatal(err)
				}
				plan := queryPlan(t, repo, trafficFieldCountSQL(match, key), match.args...)
				if !strings.Contains(plan, "idx_request_armory_run_id") || strings.Contains(plan, "SCAN request") {
					t.Fatalf("the count reads\n%s\nwant it to search idx_request_armory_run_id", plan)
				}
			})
		}
	}
}
