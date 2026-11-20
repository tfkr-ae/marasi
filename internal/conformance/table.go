package conformance

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// WriteTable prints one row per new deviation, then the known ones and those on the allow-list
// deviations, the entries that went away or are stale, and one summary line
// per suite, run and target.
func WriteTable(w io.Writer, outcome Outcome) {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "RUN\tSUITE\tTARGET\tCASE\tFIELD\tBASELINE\tGOT")
	for _, result := range outcome.Results {
		for _, d := range result.New {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				result.Run, result.Suite, result.Target, d.Case, d.Field, dash(d.Want), dash(d.Got))
		}
	}
	_ = table.Flush()

	if hasMatches(outcome.Results, func(r Result) []Match { return r.Known }) {
		fmt.Fprintln(w)
		known := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(known, "RUN\tSUITE\tTARGET\tKNOWN\tFIELD\tREASON")
		for _, result := range outcome.Results {
			for _, m := range result.Known {
				fmt.Fprintf(known, "%s\t%s\t%s\t%s\t%s\t%s\n", result.Run, result.Suite, result.Target, m.Case, m.Field, m.Entry.Reason)
			}
		}
		_ = known.Flush()
	}
	if hasMatches(outcome.Results, func(r Result) []Match { return r.OnAllowList }) {
		fmt.Fprintln(w)
		allowList := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(allowList, "RUN\tSUITE\tTARGET\tON ALLOW-LIST\tFIELD\tREASON")
		for _, result := range outcome.Results {
			for _, m := range result.OnAllowList {
				fmt.Fprintf(allowList, "%s\t%s\t%s\t%s\t%s\t%s\n", result.Run, result.Suite, result.Target, m.Case, m.Field, m.Entry.Reason)
			}
		}
		_ = allowList.Flush()
	}

	if len(outcome.WentAway) > 0 || len(outcome.Stale) > 0 {
		fmt.Fprintln(w)
	}
	for _, gone := range outcome.WentAway {
		fmt.Fprintf(w, "%s  %s  went away: known entry %s %s; remove it or run update-known\n",
			gone.Run, gone.Suite, gone.Entry.Cases, gone.Entry.Field)
	}
	for _, stale := range outcome.Stale {
		fmt.Fprintf(w, "%s  stale: %s entry %s %s matched nothing although a run and target it lists ran\n",
			stale.Suite, stale.Entry.Kind, stale.Entry.Cases, stale.Entry.Field)
	}

	fmt.Fprintln(w)
	for _, result := range outcome.Results {
		if result.Err != nil {
			fmt.Fprintf(w, "%s  %s  %s  error: %v\n", result.Run, result.Suite, result.Target, result.Err)
			continue
		}
		if result.Run == RunDirect {
			fmt.Fprintf(w, "%s  %s  %s  %d cases recorded\n", result.Run, result.Suite, result.Target, len(result.Cases))
			continue
		}
		counts := fmt.Sprintf("%d cases, %d match, %d new", len(result.Cases), result.Matched(), len(result.New))
		if len(result.Known) > 0 {
			counts += fmt.Sprintf(", %d known", len(result.Known))
		}
		if len(result.OnAllowList) > 0 {
			counts += fmt.Sprintf(", %d on the allow-list", len(result.OnAllowList))
		}
		if len(result.Warnings) > 0 {
			counts += fmt.Sprintf(", %d leg-rule warnings", len(result.Warnings))
		}
		if len(result.Notes) > 0 {
			counts += ", " + strings.Join(result.Notes, ", ")
		}
		fmt.Fprintf(w, "%s  %s  %s  %s\n", result.Run, result.Suite, result.Target, counts)
	}
}

func dash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func hasMatches(results []Result, matches func(Result) []Match) bool {
	for _, result := range results {
		if len(matches(result)) > 0 {
			return true
		}
	}
	return false
}
