package db

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/tfkr-ae/marasi/domain"
)

// CountTraffic counts the pairs that match q, exactly up to
// domain.MaxTrafficCount. It reads at most one row past the cap, so a
// larger result costs no more than the cap. An invalid query or exclusion
// returns a *domain.QueryError.
func (repo *Repository) CountTraffic(q domain.TrafficCountQuery) (domain.TrafficCount, error) {
	filter, err := newTrafficFilterAfter(q.Text, q.Exclusion, q.After)
	if err != nil {
		return domain.TrafficCount{}, err
	}
	if q.AsOf != nil {
		filter.add(`id <= ?`, *q.AsOf)
	}

	var result domain.TrafficCount
	err = repo.inTx(func(tx *sqlx.Tx) error {
		result.Complete, err = searchIndexCompleteness(tx)
		if err != nil {
			return err
		}

		query := `SELECT count(*) FROM (SELECT 1 FROM request` + filter.where() + ` LIMIT ?)`
		return tx.Get(&result.Count, query, append(filter.args, domain.MaxTrafficCount+1)...)
	})
	if err != nil {
		return domain.TrafficCount{}, fmt.Errorf("counting traffic: %w", err)
	}
	if result.Count > domain.MaxTrafficCount {
		result.Count, result.Capped = domain.MaxTrafficCount, true
	}
	return result, nil
}
