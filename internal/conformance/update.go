package conformance

import (
	"fmt"
	"slices"
	"strings"
)

// updateKnown returns a suite's deviations with a known entry for every new
// difference in its outcome, tagged with phase, and without the runs in
// which a known entry went away; an entry left with no runs is removed.
// Accepted entries are never touched: they are the allow-list, edited only
// by hand.
func updateKnown(deviations Deviations, outcome Outcome, phase int) Deviations {
	updated := make(Deviations, 0, len(deviations))
	for i, entry := range deviations {
		if entry.Kind == KindKnown {
			entry.Runs = slices.DeleteFunc(slices.Clone(entry.Runs), func(run RunName) bool {
				return slices.ContainsFunc(outcome.WentAway, func(gone WentAway) bool {
					return gone.index == i && gone.Run == run
				})
			})
			if len(entry.Runs) == 0 {
				continue
			}
		}
		updated = append(updated, entry)
	}

	// One entry per case and field, listing every run it was measured in.
	type key struct{ caseID, field string }
	added := map[key]*Entry{}
	var order []key
	for _, result := range outcome.Results {
		for _, difference := range result.New {
			k := key{difference.Case, difference.Field}
			if entry := added[k]; entry != nil {
				if !slices.Contains(entry.Runs, result.Run) {
					entry.Runs = append(entry.Runs, result.Run)
				}
				continue
			}
			added[k] = &Entry{
				Cases:  difference.Case,
				Runs:   []RunName{result.Run},
				Field:  difference.Field,
				Kind:   KindKnown,
				Reason: measuredReason(difference),
				Phase:  phase,
			}
			order = append(order, k)
		}
	}
	slices.SortStableFunc(order, func(a, b key) int {
		if c := strings.Compare(a.caseID, b.caseID); c != 0 {
			return c
		}
		return strings.Compare(a.field, b.field)
	})
	for _, k := range order {
		entry := added[k]
		slices.SortFunc(entry.Runs, func(a, b RunName) int {
			return slices.Index(ProxiedRuns, a) - slices.Index(ProxiedRuns, b)
		})
		updated = append(updated, *entry)
	}
	return updated
}

// measuredReason records what was measured, for the person who explains it.
func measuredReason(d Difference) string {
	if d.Level != "" {
		return fmt.Sprintf("measured by conformance-update-known: broke %q, sent %q", d.Want, d.Got)
	}
	if d.Want == "" && d.Got == "" {
		return "measured by conformance-update-known"
	}
	return fmt.Sprintf("measured by conformance-update-known: baseline %q, got %q", d.Want, d.Got)
}
