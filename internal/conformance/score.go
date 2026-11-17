package conformance

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Score is a suite's score.json: for each conformance run, target and
// client approach, the cases that match the baseline out of the cases run,
// and the leg rules that passed and broke.
type Score struct {
	Suite string     `json:"suite"`
	Rows  []ScoreRow `json:"rows"`
}

// ScoreRow is the score of one conformance run, target and client approach.
type ScoreRow struct {
	Run      RunName  `json:"run"`
	Target   string   `json:"target"`
	Approach Approach `json:"approach,omitempty"`
	Matched  int      `json:"matched"` // cases with no difference from the baseline
	Cases    int      `json:"cases"`   // cases run and scored
	// LegRules counts the leg rules checked on the row's cases; nil for a
	// suite with no leg rules.
	LegRules *LegScore `json:"leg_rules,omitempty"`
}

// LegScore counts leg-rule checks, one per rule and case. A broken rule
// counts whether or not the deviations file lists it.
type LegScore struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`           // broken MUST rules
	Warned int `json:"warned,omitempty"` // broken SHOULD rules
}

func (r ScoreRow) key() string {
	return string(r.Run) + "\x00" + r.Target + "\x00" + string(r.Approach)
}

// scoreRows scores every proxied result that has no error.
func scoreRows(results []Result) []ScoreRow {
	rows := map[string]*ScoreRow{}
	for _, result := range results {
		if result.Run == RunDirect || result.Err != nil {
			continue
		}
		differing := result.differing()
		row := func(id string) *ScoreRow {
			r := ScoreRow{Run: result.Run, Target: result.Target, Approach: CaseApproach(id)}
			if rows[r.key()] == nil {
				rows[r.key()] = &r
			}
			return rows[r.key()]
		}
		for _, id := range result.Cases {
			r := row(id)
			r.Cases++
			if !differing[id] {
				r.Matched++
			}
		}
		for _, check := range result.LegChecks {
			r := row(check.Case)
			if r.LegRules == nil {
				r.LegRules = &LegScore{}
			}
			switch {
			case !check.Broken:
				r.LegRules.Passed++
			case check.Rule.Level == LevelShould:
				r.LegRules.Warned++
			default:
				r.LegRules.Failed++
			}
		}
	}
	scored := make([]ScoreRow, 0, len(rows))
	for _, row := range rows {
		scored = append(scored, *row)
	}
	sortRows(scored)
	return scored
}

// sortRows orders rows by conformance run, then target, then approach.
func sortRows(rows []ScoreRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if c := cmp.Compare(slices.Index(ProxiedRuns, a.Run), slices.Index(ProxiedRuns, b.Run)); c != 0 {
			return c < 0
		}
		if c := strings.Compare(a.Target, b.Target); c != 0 {
			return c < 0
		}
		return a.Approach < b.Approach
	})
}

// MergeScore replaces the rows of score that rows score again and keeps the rest.
func MergeScore(score Score, rows []ScoreRow) Score {
	merged := map[string]ScoreRow{}
	for _, row := range score.Rows {
		merged[row.key()] = row
	}
	for _, row := range rows {
		merged[row.key()] = row
	}
	score.Rows = score.Rows[:0:0]
	for _, row := range merged {
		score.Rows = append(score.Rows, row)
	}
	sortRows(score.Rows)
	return score
}

// ReadScore reads a score file. A missing file is an empty score for suite.
func ReadScore(file, suite string) (Score, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return Score{Suite: suite, Rows: []ScoreRow{}}, nil
	}
	if err != nil {
		return Score{}, fmt.Errorf("reading score: %w", err)
	}
	var score Score
	if err := json.Unmarshal(data, &score); err != nil {
		return Score{}, fmt.Errorf("decoding score %s: %w", file, err)
	}
	return score, nil
}

// WriteScore writes score as indented JSON.
func WriteScore(file string, score Score) error {
	if score.Rows == nil {
		score.Rows = []ScoreRow{}
	}
	return writeJSON(file, score)
}

// writeScores writes the score of this run to the reports, and with
// UpdateKnown merges it into the committed score.
func (s Settings) writeScores(suite string, outcome Outcome) error {
	rows := scoreRows(outcome.Results)
	if len(rows) == 0 {
		return nil
	}
	if err := WriteScore(filepath.Join(s.OutDir, suite, "score.json"), Score{Suite: suite, Rows: rows}); err != nil {
		return err
	}
	if !s.UpdateKnown {
		return nil
	}
	score, err := ReadScore(s.ScoreFile(suite), suite)
	if err != nil {
		return err
	}
	return WriteScore(s.ScoreFile(suite), MergeScore(score, rows))
}
