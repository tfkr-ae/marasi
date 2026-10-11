package autobahn

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

// SuiteResult is one mode and peer under one run, compared to the baseline.
type SuiteResult struct {
	Run        string      // direct, proxy, checkpoint-forward, checkpoint-edit, extension
	Mode       string      // fuzzingclient or fuzzingserver
	Peer       string      // strict or lenient
	Cases      int         // cases the run reported
	Deviations []Deviation // cases that differ from the baseline
	Timing     Timing      // 9.x durations, tracked but not compared
	Err        error       // harness failure; the suite produced no report
}

// Suite names the mode and peer, for example fuzzingclient/strict.
func (r SuiteResult) Suite() string {
	return r.Mode + "/" + r.Peer
}

// WriteTable prints one row per deviation, then one summary line per suite.
func WriteTable(w io.Writer, results []SuiteResult) {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "RUN\tSUITE\tCASE\tDIFFERS\tBASELINE\tGOT")
	for _, result := range results {
		for _, deviation := range result.Deviations {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n",
				result.Run, result.Suite(), deviation.Case, strings.Join(deviation.Fields, ","),
				describe(deviation.Want, deviation.Fields, "no baseline"),
				describe(deviation.Got, deviation.Fields, "missing"))
		}
	}
	_ = table.Flush()
	fmt.Fprintln(w)
	for _, result := range results {
		if result.Err != nil {
			fmt.Fprintf(w, "%s  %s  error: %v\n", result.Run, result.Suite(), result.Err)
			continue
		}
		timing := ""
		if result.Timing.Run > 0 {
			timing = fmt.Sprintf(", 9.x %dms (baseline %dms)", result.Timing.Run, result.Timing.Baseline)
		}
		fmt.Fprintf(w, "%s  %s  %d cases, %d deviations%s\n", result.Run, result.Suite(), result.Cases, len(result.Deviations), timing)
	}
}

// describe renders a result as "behavior behaviorClose code clean", or "-"
// when fields says this side has no result.
func describe(result Result, fields []string, absent string) string {
	if len(fields) == 1 && fields[0] == absent {
		return "-"
	}
	closeCode := "-"
	if result.RemoteCloseCode != nil {
		closeCode = strconv.Itoa(*result.RemoteCloseCode)
	}
	clean := "unclean"
	if result.WasClean {
		clean = "clean"
	}
	return strings.Join([]string{result.Behavior, result.BehaviorClose, closeCode, clean}, " ")
}
