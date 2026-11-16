package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// writeReports writes the Markdown summary of one suite and run to dir,
// and appends it to Settings.Summary when that is set.
func (s Settings) writeReports(suite string, run RunName, dir string, results []Result, wentAway []WentAway) error {
	var summary strings.Builder
	WriteSummary(&summary, suite, run, results, wentAway)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating report directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(summary.String()), 0o644); err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	if s.Summary != nil {
		if _, err := io.WriteString(s.Summary, summary.String()); err != nil {
			return fmt.Errorf("writing summary: %w", err)
		}
	}
	return nil
}

// WriteSummary writes the Markdown summary of one suite and run: the score,
// then new deviations, known deviations that went away, known deviations
// and allowed deviations, most urgent first.
func WriteSummary(w io.Writer, suite string, run RunName, results []Result, wentAway []WentAway) {
	fmt.Fprintf(w, "## %s: %s\n\n", suite, run)

	fmt.Fprintln(w, "### Score")
	fmt.Fprintln(w)
	rows := scoreRows(results)
	if len(rows) == 0 {
		fmt.Fprintln(w, "No case was scored.")
	} else {
		writeRow(w, "Target", "Approach", "Matched", "Cases")
		writeRow(w, "---", "---", "---:", "---:")
		for _, row := range rows {
			writeRow(w, row.Target, approachLabel(row.Approach), strconv.Itoa(row.Matched), strconv.Itoa(row.Cases))
		}
	}
	for _, result := range results {
		if result.Err != nil {
			fmt.Fprintf(w, "\n**Error** in %s: %s\n", result.Target, cell(result.Err.Error()))
		}
		for _, note := range result.Notes {
			fmt.Fprintf(w, "\n%s: %s\n", result.Target, note)
		}
	}

	section(w, "New deviations", func() bool {
		wrote := false
		for _, result := range results {
			for _, d := range result.New {
				if !wrote {
					writeRow(w, "Target", "Case", "Field", "Baseline", "Got")
					writeRow(w, "---", "---", "---", "---", "---")
					wrote = true
				}
				writeRow(w, result.Target, d.Case, d.Field, d.Want, d.Got)
			}
		}
		return wrote
	})
	section(w, "Known deviations that went away", func() bool {
		if len(wentAway) == 0 {
			return false
		}
		writeRow(w, "Cases", "Field", "Phase", "Reason")
		writeRow(w, "---", "---", "---:", "---")
		for _, gone := range wentAway {
			writeRow(w, gone.Entry.Cases, gone.Entry.Field, strconv.Itoa(gone.Entry.Phase), gone.Entry.Reason)
		}
		return true
	})
	section(w, "Known deviations", func() bool {
		wrote := false
		for _, result := range results {
			for _, m := range result.Known {
				if !wrote {
					writeRow(w, "Target", "Case", "Field", "Baseline", "Got", "Phase", "Reason")
					writeRow(w, "---", "---", "---", "---", "---", "---:", "---")
					wrote = true
				}
				writeRow(w, result.Target, m.Case, m.Field, m.Want, m.Got, strconv.Itoa(m.Entry.Phase), m.Entry.Reason)
			}
		}
		return wrote
	})
	section(w, "Allowed deviations", func() bool {
		wrote := false
		for _, result := range results {
			for _, m := range result.Allowed {
				if !wrote {
					writeRow(w, "Target", "Case", "Field", "Baseline", "Got", "Reason", "RFC")
					writeRow(w, "---", "---", "---", "---", "---", "---", "---")
					wrote = true
				}
				writeRow(w, result.Target, m.Case, m.Field, m.Want, m.Got, m.Entry.Reason, m.Entry.RFC)
			}
		}
		return wrote
	})
}

// section writes a heading and body, or "None." when body writes nothing.
func section(w io.Writer, heading string, body func() bool) {
	fmt.Fprintf(w, "\n### %s\n\n", heading)
	if !body() {
		fmt.Fprintln(w, "None.")
	}
}

func writeRow(w io.Writer, cells ...string) {
	for i := range cells {
		cells[i] = cell(cells[i])
	}
	fmt.Fprintf(w, "| %s |\n", strings.Join(cells, " | "))
}

func cell(value string) string {
	value = strings.ReplaceAll(value, "|", `\|`)
	return strings.ReplaceAll(value, "\n", " ")
}

func approachLabel(approach Approach) string {
	if approach == ApproachNone {
		return "-"
	}
	return string(approach)
}

// caseDiff is the JSON diff of one failing case, with both recordings.
type caseDiff struct {
	Suite       string          `json:"suite"`
	Run         RunName         `json:"run"`
	Target      string          `json:"target"`
	Case        string          `json:"case"`
	Differences []Difference    `json:"differences"`
	Baseline    json.RawMessage `json:"baseline"`
	Got         json.RawMessage `json:"got"`
}

// writeCaseDiffs writes a JSON diff for each case with a new deviation.
func writeCaseDiffs(dir string, result *Result, baseline, got Recordings) error {
	failing := map[string]bool{}
	for _, d := range result.New {
		failing[d.Case] = true
	}
	for _, id := range result.Cases {
		if !failing[id] {
			continue
		}
		diff := caseDiff{Suite: result.Suite, Run: result.Run, Target: result.Target, Case: id,
			Baseline: orNull(baseline[id]), Got: orNull(got[id])}
		for _, d := range result.New {
			if d.Case == id {
				diff.Differences = append(diff.Differences, d)
			}
		}
		for _, m := range slices.Concat(result.Known, result.Allowed) {
			if m.Case == id {
				diff.Differences = append(diff.Differences, m.Difference)
			}
		}
		name := strings.NewReplacer("/", "_", string(filepath.Separator), "_").Replace(id) + ".json"
		if err := writeJSON(filepath.Join(dir, name), diff); err != nil {
			return err
		}
	}
	return nil
}

func orNull(recording json.RawMessage) json.RawMessage {
	if recording == nil {
		return json.RawMessage("null")
	}
	return recording
}
