package service

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

// searchLaunchpads runs a traffic query over every launchpad's entries and
// returns the matching request ids per launchpad id, in each launchpad's
// entry order. Launchpads without a match are absent. An invalid query
// returns a *domain.QueryError positioned in query as the researcher typed it.
//
// One query over every launchpad member (launchpad:*) answers the common
// no-match case without visiting each launchpad. Otherwise each launchpad
// runs the query scoped to its own membership, so a pair maps to every
// launchpad that holds it, linked originals included (they carry no
// launchpad_id metadata).
func searchLaunchpads(launchpads domain.LaunchpadRepository, traffic domain.TrafficRepository, query string) (map[string][]uuid.UUID, error) {
	// Validate the text on its own, so positions refer to what the
	// researcher typed and an unbalanced parenthesis can't pair up with the
	// ones added around it below.
	if err := db.ValidateTrafficQuery(query, nil, ""); err != nil {
		return nil, err
	}
	matches := map[string][]uuid.UUID{}
	members := "launchpad:*"
	if strings.TrimSpace(query) != "" {
		members += " AND (" + query + ")"
	}
	page, err := traffic.ListTraffic(domain.TrafficQuery{Text: members, Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("searching launchpad members: %w", err)
	}
	if len(page.Items) == 0 {
		return matches, nil
	}
	all, err := launchpads.GetLaunchpads()
	if err != nil {
		return nil, fmt.Errorf("listing launchpads: %w", err)
	}
	for _, launchpad := range all {
		entries, _, err := launchpads.GetLaunchpadRequests(launchpad.ID, query, nil, "")
		if err != nil {
			return nil, fmt.Errorf("searching launchpad %s: %w", launchpad.ID, err)
		}
		if len(entries) == 0 {
			continue
		}
		ids := make([]uuid.UUID, 0, len(entries))
		for _, entry := range entries {
			ids = append(ids, entry.ID)
		}
		matches[launchpad.ID.String()] = ids
	}
	return matches, nil
}
