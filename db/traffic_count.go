package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// CountTraffic counts the pairs that match q, exactly up to
// domain.MaxTrafficCount. It reads at most one row past the cap, so a
// larger result costs no more than the cap. An invalid query or exclusion
// returns a *domain.QueryError.
func (repo *Repository) CountTraffic(q domain.TrafficCountQuery) (domain.TrafficCount, error) {
	return repo.CountTrafficContext(context.Background(), q)
}

// CountTrafficContext is CountTraffic for a count that stops when ctx ends.
func (repo *Repository) CountTrafficContext(ctx context.Context, q domain.TrafficCountQuery) (domain.TrafficCount, error) {
	match, err := newTrafficMatchAfter(q.Text, q.Exclusion, q.After)
	if err != nil {
		return domain.TrafficCount{}, err
	}
	if err := repo.indexReadyFor(match); err != nil {
		return domain.TrafficCount{}, err
	}
	if q.AsOf != nil {
		match.add(`id <= ?`, *q.AsOf)
	}

	var result domain.TrafficCount
	err = repo.inTxContext(ctx, func(tx *sqlx.Tx) error {
		complete, err := listCompleteness(tx, match)
		if err != nil {
			return err
		}
		result.Complete = complete

		return tx.GetContext(ctx, &result.Count, trafficCountSQL(match), append(match.args, domain.MaxTrafficCount+1)...)
	})
	if err != nil {
		return domain.TrafficCount{}, fmt.Errorf("counting traffic: %w", err)
	}
	if result.Count > domain.MaxTrafficCount {
		result.Count, result.Capped = domain.MaxTrafficCount, true
	}
	return result, nil
}

// trafficCountSQL counts the pairs match selects, up to the bound in its
// last argument, which follows match's arguments.
func trafficCountSQL(match trafficMatch) string {
	return `SELECT count(*) FROM (SELECT 1 FROM request` + match.where() + ` LIMIT ?)`
}

// countFields are the expressions CountTrafficByField counts by, the ones a
// traffic query compares for the same field. A pair with no value has NULL.
var countFields = map[string]string{
	"length":      trafficFields["length"].column,
	"status_code": trafficFields["status_code"].column,
}

// CountTrafficByField counts the pairs that match q by the value of
// q.Field, exactly. Pairs with no value are counted under "". A field it
// cannot count by wraps domain.ErrInvalidCountField; an invalid query or
// exclusion returns a *domain.QueryError.
func (repo *Repository) CountTrafficByField(q domain.TrafficFieldCountQuery) (domain.TrafficFieldCounts, error) {
	return repo.CountTrafficByFieldContext(context.Background(), q)
}

// CountTrafficByFieldContext is CountTrafficByField for a count that stops
// when ctx ends.
func (repo *Repository) CountTrafficByFieldContext(ctx context.Context, q domain.TrafficFieldCountQuery) (domain.TrafficFieldCounts, error) {
	key, ok := countFields[q.Field]
	if !ok {
		return domain.TrafficFieldCounts{}, fmt.Errorf("%w: %q; count by one of %s", domain.ErrInvalidCountField, q.Field, strings.Join(domain.CountFields, ", "))
	}
	match, err := newTrafficMatch(q.Text, q.Exclusion)
	if err != nil {
		return domain.TrafficFieldCounts{}, err
	}
	if err := repo.indexReadyFor(match); err != nil {
		return domain.TrafficFieldCounts{}, err
	}

	var rows []struct {
		Value sql.NullInt64 `db:"value"`
		Count int           `db:"count"`
	}
	result := domain.TrafficFieldCounts{Counts: []*domain.TrafficFieldCount{}}
	err = repo.inTxContext(ctx, func(tx *sqlx.Tx) error {
		complete, err := listCompleteness(tx, match)
		if err != nil {
			return err
		}
		result.Complete = complete

		result.AsOf = q.AsOf
		if result.AsOf == nil {
			newest, err := newestPairID(ctx, tx)
			if err != nil || newest == nil {
				return err
			}
			result.AsOf = newest
		}
		match.add(`id <= ?`, *result.AsOf)
		return tx.SelectContext(ctx, &rows, trafficFieldCountSQL(match, key), match.args...)
	})
	if err != nil {
		return domain.TrafficFieldCounts{}, fmt.Errorf("counting traffic by %s: %w", q.Field, err)
	}
	for _, row := range rows {
		count := &domain.TrafficFieldCount{Count: row.Count}
		if row.Value.Valid {
			count.Value = strconv.FormatInt(row.Value.Int64, 10)
		}
		result.Counts = append(result.Counts, count)
	}
	return result, nil
}

// trafficFieldCountSQL counts the pairs match selects by the value of key,
// most common first, with no value last among equal counts.
func trafficFieldCountSQL(match trafficMatch, key string) string {
	return `SELECT ` + key + ` AS value, count(*) AS count FROM request` + match.where() +
		` GROUP BY value ORDER BY count DESC, value IS NULL, value ASC`
}
