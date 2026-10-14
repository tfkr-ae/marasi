package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

func TestValidateTrafficQuery(t *testing.T) {
	t.Run("should report each invalid query as ListTraffic does", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		for _, test := range invalidTrafficQueries() {
			t.Run(test.query, func(t *testing.T) {
				_, listErr := repo.ListTraffic(domain.TrafficQuery{Text: test.query, Limit: 1})
				var want *domain.QueryError
				if !errors.As(listErr, &want) {
					t.Fatalf("\nwanted:\nListTraffic to return *domain.QueryError\ngot:\n%T %v", listErr, listErr)
				}

				err := ValidateTrafficQuery(test.query, nil)

				var got *domain.QueryError
				if !errors.As(err, &got) {
					t.Fatalf("\nwanted:\n*domain.QueryError\ngot:\n%T %v", err, err)
				}
				if *got != *want {
					t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", *want, *got)
				}
			})
		}
	})

	t.Run("should validate with no database at all", func(t *testing.T) {
		if err := ValidateTrafficQuery(`host = "a" AND status_code >= 400`, []string{"image/*", "text/css"}); err != nil {
			t.Fatalf("\nwanted:\nno error\ngot:\n%v", err)
		}
		err := ValidateTrafficQuery(`stauts = 5`, nil)
		var queryErr *domain.QueryError
		if !errors.As(err, &queryErr) || queryErr.Position != 1 || queryErr.Message != `unknown field "stauts"` {
			t.Fatalf("\nwanted:\nunknown field \"stauts\" at position 1\ngot:\n%T %v", err, err)
		}
	})

	t.Run("should name an invalid exclusion value", func(t *testing.T) {
		err := ValidateTrafficQuery(``, []string{"text/html", "*/json"})

		var queryErr *domain.QueryError
		if !errors.As(err, &queryErr) {
			t.Fatalf("\nwanted:\n*domain.QueryError\ngot:\n%T %v", err, err)
		}
		if !strings.Contains(queryErr.Message, `"*/json"`) {
			t.Fatalf("\nwanted message naming:\n\"*/json\"\ngot:\n%s", queryErr.Message)
		}
	})
}
