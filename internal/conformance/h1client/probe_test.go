package h1client

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadReport(t *testing.T) {
	t.Run("should list the tests that ran, in order, with their verdicts", func(t *testing.T) {
		report, err := LoadReport("testdata/results.json")
		if err != nil {
			t.Fatalf("loading report: %v", err)
		}
		var got [][2]string
		for _, r := range report.Ran() {
			got = append(got, [2]string{r.ID, r.Verdict})
		}
		want := [][2]string{
			{"COMP-BASELINE", "Pass"},
			{"SMUG-CL-TE-BOTH", "Pass"},
			{"SMUG-TECL-PIPELINE", "Pass"},
			{"MAL-EMPTY-REQUEST", "Pass"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should keep the request the tester sent", func(t *testing.T) {
		report, err := LoadReport("testdata/results.json")
		if err != nil {
			t.Fatalf("loading report: %v", err)
		}
		want := "GET / HTTP/1.1\r\nHost: 127.0.0.1:18087\r\n\r\n"
		if got := report.Ran()[0].RawRequest; got != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, got)
		}
	})

	t.Run("should fail for a file that is not a report", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "results.json")
		if err := os.WriteFile(file, []byte(`{"summary": {}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadReport(file); err == nil {
			t.Fatal("\nwanted:\nan error for a report with no results\ngot:\nnone")
		}
	})
}
