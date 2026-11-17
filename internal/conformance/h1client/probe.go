package h1client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Report is Http11Probe's JSON output (--output).
type Report struct {
	Results []Result `json:"results"`
}

// Result is one Http11Probe test.
type Result struct {
	ID              string `json:"id"`
	Description     string `json:"description"`
	Category        string `json:"category"`
	RFCReference    string `json:"rfcReference"`
	Scored          bool   `json:"scored"`
	Verdict         string `json:"verdict"` // Pass, Fail, Warn, Error or Skip
	StatusCode      *int   `json:"statusCode"`
	ConnectionState string `json:"connectionState"`
	Error           string `json:"error"`
	// RawRequest is what the test sent, as ASCII and cut at 8192 bytes; a
	// sequence test lists its steps under "── label ──" lines.
	RawRequest string `json:"rawRequest"`
}

// Skipped is the verdict of a test the run did not select. It opened no
// connection.
const Skipped = "Skip"

// LoadReport reads an Http11Probe JSON report.
func LoadReport(file string) (Report, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("decoding %s: %w", file, err)
	}
	if len(report.Results) == 0 {
		return Report{}, errors.New(file + " lists no results")
	}
	return report, nil
}

// Ran returns the tests that ran, in the order they ran. Http11Probe runs
// them one at a time, each on one connection of its own.
func (r Report) Ran() []Result {
	var ran []Result
	for _, result := range r.Results {
		if result.Verdict != Skipped {
			ran = append(ran, result)
		}
	}
	return ran
}
