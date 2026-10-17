package db

import (
	"context"
	"fmt"

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
