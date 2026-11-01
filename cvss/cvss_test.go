package cvss

import (
	"strings"
	"testing"
)

func TestBase(t *testing.T) {
	t.Run("should score known vectors as the specification does", func(t *testing.T) {
		for _, tc := range []struct {
			vector string
			score  float64
		}{
			{"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N", 4.3},
			{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", 9.8},
			{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H", 10.0},
			{"CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:C/C:L/I:L/A:N", 6.1},
			{"CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:H", 7.8},
			{"CVSS:3.1/AV:N/AC:H/PR:N/UI:N/S:U/C:H/I:N/A:N", 5.9},
			{"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:C/C:L/I:L/A:N", 6.4},
			{"CVSS:3.1/AV:N/AC:L/PR:H/UI:N/S:C/C:H/I:H/A:H", 9.1},
			{"CVSS:3.1/AV:A/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N", 6.5},
			{"CVSS:3.1/AV:P/AC:H/PR:H/UI:R/S:U/C:N/I:N/A:N", 0},
		} {
			normalized, score, err := Base(tc.vector)
			if err != nil || normalized != tc.vector || score != tc.score {
				t.Errorf("\nwanted:\n%s %v\ngot:\n%s %v %v", tc.vector, tc.score, normalized, score, err)
			}
		}
	})

	t.Run("should add the prefix, trim, and write metrics in the specification's order", func(t *testing.T) {
		want := "CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N"
		for _, vector := range []string{
			"AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N",
			"  CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N\n",
			"A:N/I:N/C:L/S:U/UI:N/PR:L/AC:L/AV:N",
		} {
			normalized, score, err := Base(vector)
			if err != nil || normalized != want || score != 4.3 {
				t.Errorf("\nwanted:\n%s 4.3\ngot:\n%s %v %v", want, normalized, score, err)
			}
		}
	})

	t.Run("should refuse a vector that is not a CVSS 3.1 base vector and name the problem", func(t *testing.T) {
		for _, tc := range []struct {
			vector, message string
		}{
			{"AV:X/foo", `"foo" is not a metric`},
			{"AV:X/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N", "AV:X is not a value metric AV takes"},
			{"AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N", "base metric A is missing"},
			{"AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N/AV:L", "metric AV appears twice"},
			{"AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N/E:H", "E is not a CVSS 3.1 base metric"},
			{"CVSS:3.0/AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N", "CVSS:3.0 is not CVSS 3.1"},
			{"AV:N//AC:L", `"" is not a metric`},
			{"CVSS:3.1/", `"" is not a metric`},
		} {
			_, _, err := Base(tc.vector)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Errorf("\nwanted:\nerror containing %q for %q\ngot:\n%v", tc.message, tc.vector, err)
			}
		}
	})
}

func TestRoundUp(t *testing.T) {
	t.Run("should round up to one decimal without floating-point drift", func(t *testing.T) {
		for _, tc := range []struct{ in, want float64 }{
			{4.02, 4.1}, {4.0, 4.0}, {4.000001, 4.0}, {9.99, 10.0}, {0.01, 0.1},
		} {
			if got := roundUp(tc.in); got != tc.want {
				t.Errorf("\nwanted:\nroundUp(%v) = %v\ngot:\n%v", tc.in, tc.want, got)
			}
		}
	})
}
