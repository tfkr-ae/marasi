package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// countingTrafficRepository records the query CountTraffic receives and
// answers with result, or err when set.
type countingTrafficRepository struct {
	domain.TrafficRepository
	query  *domain.TrafficCountQuery
	result domain.TrafficCount
	err    error
}

func (s *countingTrafficRepository) CountTrafficContext(_ context.Context, q domain.TrafficCountQuery) (domain.TrafficCount, error) {
	s.query = &q
	return s.result, s.err
}

func TestTrafficCount(t *testing.T) {
	serve := func(repo *countingTrafficRepository, target string) *httptest.ResponseRecorder {
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		return response
	}

	t.Run("should pass q, exclude, as_of and after to the repository and return the count", func(t *testing.T) {
		asOf := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")
		after := uuid.MustParse("01938032-0000-7243-b035-e6a9f4645904")
		repo := &countingTrafficRepository{result: domain.TrafficCount{
			Count: 100_000, Capped: true, Complete: map[domain.JobName]bool{domain.JobTrafficIndex: false},
		}}

		response := serve(repo, "/traffic/count?q="+url.QueryEscape(`method = "GET"`)+
			"&exclude=image/*&exclude=text/css&as_of="+asOf.String()+"&after="+after.String())

		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d %s", http.StatusOK, response.Code, response.Body)
		}
		want := domain.TrafficCountQuery{Text: `method = "GET"`, Exclusion: []string{"image/*", "text/css"}, AsOf: &asOf, After: &after}
		if repo.query == nil || !reflect.DeepEqual(*repo.query, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, repo.query)
		}
		if body, want := response.Body.String(), "{\"count\":100000,\"capped\":true,\"index\":{\"complete\":false}}\n"; body != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, body)
		}
	})

	t.Run("should return 400 bad_request for a malformed as_of or after", func(t *testing.T) {
		for _, target := range []string{"/traffic/count?as_of=nope", "/traffic/count?after=nope"} {
			repo := &countingTrafficRepository{}
			response := serve(repo, target)
			if response.Code != http.StatusBadRequest || repo.query != nil {
				t.Fatalf("\n%s wanted:\n400 without a count\ngot:\n%d %s", target, response.Code, response.Body)
			}
		}
	})

	t.Run("should return 400 invalid_query with the message and position of a query error", func(t *testing.T) {
		repo := &countingTrafficRepository{err: &domain.QueryError{Message: `unknown field "stauts"`, Position: 1}}

		response := serve(repo, "/traffic/count?q=stauts")

		want := "{\"error\":\"invalid_query\",\"message\":\"unknown field \\\"stauts\\\"\",\"position\":1}\n"
		if response.Code != http.StatusBadRequest || response.Body.String() != want {
			t.Fatalf("\nwanted:\n400 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})
}

// fieldCountingTrafficRepository records the query CountTrafficByField
// receives and answers with result, or err when set.
type fieldCountingTrafficRepository struct {
	domain.TrafficRepository
	query  *domain.TrafficFieldCountQuery
	result domain.TrafficFieldCounts
	err    error
}

func (s *fieldCountingTrafficRepository) CountTrafficByFieldContext(_ context.Context, q domain.TrafficFieldCountQuery) (domain.TrafficFieldCounts, error) {
	s.query = &q
	return s.result, s.err
}

func TestTrafficCountByField(t *testing.T) {
	serve := func(repo *fieldCountingTrafficRepository, target string) *httptest.ResponseRecorder {
		server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		return response
	}
	asOf := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")

	t.Run("should pass field, q, exclude and as_of to the repository and return every count with the as_of used", func(t *testing.T) {
		for _, field := range []string{"status_code", "length"} {
			repo := &fieldCountingTrafficRepository{result: domain.TrafficFieldCounts{
				Counts:   []*domain.TrafficFieldCount{{Value: "404", Count: 3}, {Value: "", Count: 1}},
				AsOf:     &asOf,
				Complete: map[domain.JobName]bool{domain.JobTrafficIndex: false},
			}}

			response := serve(repo, "/traffic/count/by-field?field="+field+"&q="+url.QueryEscape(`method = "GET"`)+
				"&exclude=image/*&exclude=text/css&as_of="+asOf.String())

			if response.Code != http.StatusOK {
				t.Fatalf("\nwanted:\n%d\ngot:\n%d %s", http.StatusOK, response.Code, response.Body)
			}
			want := domain.TrafficFieldCountQuery{Text: `method = "GET"`, Exclusion: []string{"image/*", "text/css"}, AsOf: &asOf, Field: field}
			if repo.query == nil || !reflect.DeepEqual(*repo.query, want) {
				t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, repo.query)
			}
			wantBody := `{"items":[{"value":"404","count":3},{"value":"","count":1}],"as_of":"` + asOf.String() + `","index":{"complete":false}}` + "\n"
			if body := response.Body.String(); body != wantBody {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", wantBody, body)
			}
		}
	})

	t.Run("should echo the as_of the repository used when the request omits it, and null with no pairs", func(t *testing.T) {
		repo := &fieldCountingTrafficRepository{result: domain.TrafficFieldCounts{
			Counts: []*domain.TrafficFieldCount{{Value: "200", Count: 1}}, AsOf: &asOf,
			Complete: map[domain.JobName]bool{domain.JobTrafficIndex: true},
		}}
		response := serve(repo, "/traffic/count/by-field?field=status_code")
		if repo.query == nil || repo.query.AsOf != nil {
			t.Fatalf("\nwanted:\na count without as_of\ngot:\n%+v", repo.query)
		}
		want := `{"items":[{"value":"200","count":1}],"as_of":"` + asOf.String() + `","index":{"complete":true}}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}

		repo = &fieldCountingTrafficRepository{result: domain.TrafficFieldCounts{Counts: []*domain.TrafficFieldCount{}}}
		response = serve(repo, "/traffic/count/by-field?field=length")
		want = `{"items":[],"as_of":null,"index":{"complete":false}}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})

	t.Run("should report the sitemap job when the query has a sitemap condition", func(t *testing.T) {
		repo := &fieldCountingTrafficRepository{result: domain.TrafficFieldCounts{
			Counts: []*domain.TrafficFieldCount{}, AsOf: &asOf,
			Complete: map[domain.JobName]bool{domain.JobSitemap: false},
		}}
		response := serve(repo, "/traffic/count/by-field?field=length&q="+url.QueryEscape(`sitemap = "https://a.test"`))
		want := `{"items":[],"as_of":"` + asOf.String() + `","index":{"complete":false},"sitemap":{"complete":false}}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("\nwanted:\n200 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})

	t.Run("should return 400 invalid_count_field for a field it cannot count by", func(t *testing.T) {
		repo := &fieldCountingTrafficRepository{err: fmt.Errorf("%w: %q; count by one of length, status_code", domain.ErrInvalidCountField, "host")}

		response := serve(repo, "/traffic/count/by-field?field=host")

		want := "{\"error\":\"invalid_count_field\",\"message\":\"invalid count field: \\\"host\\\"; count by one of length, status_code\"}\n"
		if response.Code != http.StatusBadRequest || response.Body.String() != want {
			t.Fatalf("\nwanted:\n400 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})

	t.Run("should return 400 invalid_query with the message and position of a query error", func(t *testing.T) {
		repo := &fieldCountingTrafficRepository{err: &domain.QueryError{Message: `unknown field "stauts"`, Position: 1}}

		response := serve(repo, "/traffic/count/by-field?field=status_code&q=stauts")

		want := "{\"error\":\"invalid_query\",\"message\":\"unknown field \\\"stauts\\\"\",\"position\":1}\n"
		if response.Code != http.StatusBadRequest || response.Body.String() != want {
			t.Fatalf("\nwanted:\n400 %s\ngot:\n%d %s", want, response.Code, response.Body)
		}
	})

	t.Run("should return 400 bad_request for a malformed as_of", func(t *testing.T) {
		repo := &fieldCountingTrafficRepository{}
		response := serve(repo, "/traffic/count/by-field?field=status_code&as_of=nope")
		if response.Code != http.StatusBadRequest || repo.query != nil {
			t.Fatalf("\nwanted:\n400 without a count\ngot:\n%d %s", response.Code, response.Body)
		}
	})
}
