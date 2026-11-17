package h2client

import (
	"os"
	"reflect"
	"testing"
)

func TestLoadJUnit(t *testing.T) {
	t.Run("should list every case the report ran with its verdict, in order", func(t *testing.T) {
		got, err := LoadJUnit("testdata/junit.xml")
		if err != nil {
			t.Fatalf("loading report: %v", err)
		}
		want := []Result{
			{Group: "generic/1", Desc: "Sends a client connection preface", Verdict: Passed},
			{Group: "http2/4.2", Desc: "Sends a DATA frame with 2^14 octets in length", Verdict: Passed},
			{Group: "http2/4.2", Desc: "Sends a large size DATA frame that exceeds the SETTINGS_MAX_FRAME_SIZE", Verdict: Failed},
			{Group: "http2/4.2", Desc: "Sends a large size HEADERS frame that exceeds the SETTINGS_MAX_FRAME_SIZE", Verdict: Failed},
			{Group: "http2/6.9.2", Desc: "Changes SETTINGS_INITIAL_WINDOW_SIZE after sending HEADERS frame", Verdict: Passed},
			{Group: "http2/6.9.2", Desc: "Sends a SETTINGS frame for window size to be negative", Verdict: Skipped},
			{Group: "http2/6.9.2", Desc: "Sends a SETTINGS_INITIAL_WINDOW_SIZE settings with an exceeded maximum window size value", Verdict: Passed},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should fail on a file that is not a JUnit report", func(t *testing.T) {
		file := t.TempDir() + "/junit.xml"
		if err := os.WriteFile(file, []byte("Error: Protocol negotiation failed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadJUnit(file); err == nil {
			t.Fatal("wanted an error")
		}
	})
}

func TestParseDryRun(t *testing.T) {
	t.Run("should list every case of h2spec's dry run with h2spec's own ID", func(t *testing.T) {
		data, err := os.ReadFile("testdata/dryrun.txt")
		if err != nil {
			t.Fatal(err)
		}
		tests, err := ParseDryRun(data)
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		if len(tests) != 146 {
			t.Fatalf("wanted 146 cases, got %d", len(tests))
		}
		pick := map[int]Test{0: tests[0], 1: tests[1], 145: tests[145]}
		want := map[int]Test{
			0:   {Group: "generic/1", Seq: 1, Desc: "Sends a client connection preface"},
			1:   {Group: "generic/2", Seq: 1, Desc: "Sends a PRIORITY frame on idle stream"},
			145: {Group: "hpack/6.3", Seq: 1, Desc: "Sends a dynamic table size update larger than the value of SETTINGS_HEADER_TABLE_SIZE"},
		}
		if !reflect.DeepEqual(pick, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, pick)
		}
		ids := map[string]bool{}
		for _, test := range tests {
			if ids[test.ID()] {
				t.Fatalf("case ID %s appears twice", test.ID())
			}
			ids[test.ID()] = true
		}
		if !ids["http2/6.9.2/2"] || !ids["hpack/2.3.3/1"] {
			t.Fatalf("missing an expected case among %d", len(ids))
		}
	})

	t.Run("should fail on output that names no case", func(t *testing.T) {
		if _, err := ParseDryRun([]byte("No matched tests found.\n")); err == nil {
			t.Fatal("wanted an error")
		}
	})
}

func TestCaseName(t *testing.T) {
	t.Run("should have no slash, so a case pattern's * matches it", func(t *testing.T) {
		test := Test{Group: "http2/6.9.1", Seq: 3}
		if got, want := test.Name(), "http2-6.9.1-3"; got != want {
			t.Fatalf("wanted %q, got %q", want, got)
		}
	})
}
