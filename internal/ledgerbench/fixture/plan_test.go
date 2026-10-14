package fixture

import (
	"bytes"
	"sort"
	"strings"
	"testing"
)

// The plan is the fixture's distribution. These checks hold it to the
// ticket's table, at a size where the shares are stable.
func TestPlanDistribution(t *testing.T) {
	const pairs = 200_000
	plans, hosts, _ := planPairs(1, pairs, End)

	share := func(count int) float64 { return 100 * float64(count) / pairs }
	within := func(t *testing.T, what string, got, low, high float64) {
		t.Helper()
		if got < low || got > high {
			t.Errorf("\nwanted:\n%s between %.1f%% and %.1f%%\ngot:\n%.2f%%", what, low, high, got)
		}
	}

	t.Run("should give ids in requested_at order", func(t *testing.T) {
		for i := 1; i < len(plans); i++ {
			if bytes.Compare(plans[i-1].id[:], plans[i].id[:]) >= 0 || plans[i].at.Before(plans[i-1].at) {
				t.Fatalf("\nwanted:\nids and times increasing\ngot:\npair %d out of order", i)
			}
		}
	})

	t.Run("should spread pairs over hosts with a dominant host and a long tail", func(t *testing.T) {
		counts := map[int32]int{}
		for _, p := range plans {
			counts[p.host]++
		}
		var sorted []int
		for _, c := range counts {
			sorted = append(sorted, c)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(sorted)))
		top10 := 0
		for _, c := range sorted[:10] {
			top10 += c
		}
		within(t, "top host", share(sorted[0]), 20, 30)
		within(t, "top 10 hosts", share(top10), 50, 70)
		if len(counts) < len(hosts.names)/2 {
			t.Errorf("\nwanted:\nmost of %d hosts used\ngot:\n%d", len(hosts.names), len(counts))
		}
	})

	t.Run("should mix status classes like an engagement", func(t *testing.T) {
		classes := map[string]int{}
		for _, p := range plans {
			switch {
			case p.status < 0:
				classes["none"]++
			case p.status < 200:
				classes["1xx"]++
			default:
				classes[string(rune('0'+p.status/100))+"xx"]++
			}
		}
		within(t, "2xx", share(classes["2xx"]), 66, 76)
		within(t, "3xx", share(classes["3xx"]), 6, 10)
		within(t, "4xx", share(classes["4xx"]), 15, 22)
		within(t, "5xx", share(classes["5xx"]), 0.5, 1.5)
		within(t, "no response", share(classes["none"]), 0.5, 1)
	})

	t.Run("should use about 100 content types in the ticket's proportions", func(t *testing.T) {
		groups := map[string]int{}
		distinct := map[int]bool{}
		binary := 0
		for _, p := range plans {
			if p.status < 0 {
				continue
			}
			distinct[p.ctype] = true
			ct := contentTypes[p.ctype]
			groups[ct.group]++
			if ct.kind == bodyBinary || p.codings != nil {
				binary++
			}
		}
		if len(distinct) < 90 || len(distinct) > 110 {
			t.Errorf("\nwanted:\nabout 100 content types\ngot:\n%d", len(distinct))
		}
		within(t, "JSON", share(groups["json"]), 35, 45)
		within(t, "HTML", share(groups["html"]), 9, 15)
		within(t, "JS", share(groups["js"]), 11, 19)
		within(t, "CSS", share(groups["css"]), 3, 8)
		within(t, "images", share(groups["image"]), 11, 19)
		within(t, "fonts", share(groups["font"]), 1.5, 4.5)
		within(t, "binary responses", share(binary), 15, 25)
	})

	t.Run("should give paths of depth 1 to 12 with a mean of 3 to 4", func(t *testing.T) {
		total := 0
		for _, p := range plans {
			path, _, _ := strings.Cut(p.path, "?")
			depth := strings.Count(strings.TrimSuffix(path, "/"), "/")
			if depth < 1 && path != "/" || depth > 12 {
				t.Fatalf("\nwanted:\ndepth 1 to 12\ngot:\n%q", p.path)
			}
			total += max(depth, 1)
		}
		mean := float64(total) / pairs
		if mean < 3 || mean > 4 {
			t.Errorf("\nwanted:\nmean depth 3 to 4\ngot:\n%.2f", mean)
		}
	})

	t.Run("should size responses log-normally with a median of 1 to 2 KB", func(t *testing.T) {
		var sizes []int
		for _, p := range plans {
			if p.status == 200 && p.burst == 0 {
				sizes = append(sizes, p.respSize)
			}
		}
		sort.Ints(sizes)
		at := func(q float64) int { return sizes[int(q*float64(len(sizes)-1))] }
		if m := at(0.5); m < 1000 || m > 2000 {
			t.Errorf("\nwanted:\nmedian 1 to 2 KB\ngot:\n%d", m)
		}
		if p90 := at(0.9); p90 < 20_000 || p90 > 40_000 {
			t.Errorf("\nwanted:\np90 about 30 KB\ngot:\n%d", p90)
		}
		if p99 := at(0.99); p99 < 250_000 || p99 > 750_000 {
			t.Errorf("\nwanted:\np99 about 500 KB\ngot:\n%d", p99)
		}
	})
}
