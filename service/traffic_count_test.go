package service

import (
	"context"
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
