package fixture

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

// appColours are the five highlight colours marasi-app writes to
// metadata.highlight.
var appColours = []string{"#EF4444", "#EAB308", "#22C55E", "#3B82F6", "#A855F7"}

// annotate adds what a tester adds after capture, through the same
// repository calls the service makes: notes, old-style metadata highlights,
// launchpads, findings and test cases.
func (b *builder) annotate(m *Manifest) error {
	rng := rand.New(rand.NewPCG(b.seed, 0xa110))
	n := len(b.plans)
	ids := func(from, to, count int) []uuid.UUID {
		seen := map[int]bool{}
		var out []uuid.UUID
		for len(out) < count && len(seen) < to-from {
			i := from + rng.IntN(to-from)
			if !seen[i] {
				seen[i] = true
				out = append(out, b.plans[i].id)
			}
		}
		return out
	}

	// Notes on 0.75% of pairs, 50 to 2,000 characters each; one old note
	// carries the rare note term.
	noted := ids(0, n, n*3/400)
	for k, id := range noted {
		text := sentence(rng, 8+rng.IntN(300))
		text = text[:min(len(text), 50+rng.IntN(1950))]
		if err := b.repo.UpdateNote(id, text); err != nil {
			return err
		}
		b.progress("notes", k+1, len(noted))
	}
	old := b.plans[n/10].id
	if err := b.repo.UpdateNote(old, "Check the "+rareNoteTerm+" redirect again"); err != nil {
		return err
	}

	// Old-style metadata.highlight on 0.5% of pairs: half in clusters, half
	// scattered over the oldest half.
	var highlighted []uuid.UUID
	for len(highlighted) < n/400 {
		start := rng.IntN(n - 50)
		for i := start; i < start+10+rng.IntN(40); i++ {
			highlighted = append(highlighted, b.plans[i].id)
		}
	}
	highlighted = append(highlighted, ids(0, n/2, n/400)...)
	for k, id := range highlighted {
		metadata, err := b.repo.GetMetadata(id)
		if err != nil {
			return err
		}
		colour := appColours[rng.IntN(len(appColours))]
		if rng.Float64() < 0.1 {
			colour = []string{"#123456", "red", "yellow", "#f0f"}[rng.IntN(4)]
		}
		metadata["highlight"] = colour
		if err := b.repo.UpdateMetadata(metadata, id); err != nil {
			return err
		}
		b.progress("highlights", k+1, len(highlighted))
	}

	// Launchpads: one with 1% of pairs, one with 100 old pairs, and 20 small ones.
	link := func(name string, pairs []uuid.UUID) (uuid.UUID, error) {
		id, err := b.repo.CreateLaunchpad(name, "")
		if err != nil {
			return uuid.Nil, err
		}
		for _, pair := range pairs {
			if err := b.repo.LinkRequestToLaunchpad(pair, id); err != nil {
				return uuid.Nil, err
			}
		}
		return id, nil
	}
	var err error
	if m.BigLaunchpad, err = link("Big launchpad", ids(0, n, n/100)); err != nil {
		return err
	}
	if m.OldLaunchpad, err = link("Old launchpad", ids(0, n*3/10, 100)); err != nil {
		return err
	}
	for k := range 20 {
		if _, err := link(fmt.Sprintf("Launchpad %d", k+1), ids(0, n, 1+rng.IntN(max(1, n/1000)))); err != nil {
			return err
		}
	}
	b.progress("launchpads", 1, 1)

	// Test cases: 200 with up to 1,000 pairs each at the reference size.
	clock := newIDClock(rand.New(rand.NewPCG(b.seed, 0x7e57)))
	created := m.Newest.Add(time.Hour)
	maxPairs := max(10, 1000*n/ReferencePairs)
	var testCases []uuid.UUID
	for k := range 200 {
		count := 1 + rng.IntN(maxPairs)
		if k == 0 {
			count = maxPairs
			m.LargeTestCasePairs = count
		}
		tc := &domain.TestCase{
			ID: clock.at(created), Title: "Test case " + sentence(rng, 3), Category: word(rng),
			Tags: []string{word(rng)}, Requests: ids(0, n, count),
		}
		if err := b.repo.SaveTestCase(tc); err != nil {
			return err
		}
		testCases = append(testCases, tc.ID)
	}
	m.LargeTestCase = testCases[0]

	// Findings: about 50, with 1 to 200 pairs each; the first has exactly 50.
	for k := range 50 {
		count := 1 + rng.IntN(200)
		if k == 0 {
			count = 50
		}
		f := &domain.Finding{
			ID: clock.at(created), Title: "Finding " + sentence(rng, 3), Severity: []string{"Low", "Medium", "High", "Critical"}[rng.IntN(4)],
			Requests: ids(0, n, count),
		}
		if rng.Float64() < 0.5 {
			f.TestCaseID = &testCases[rng.IntN(len(testCases))]
		}
		if err := b.repo.SaveFinding(f); err != nil {
			return err
		}
		if k == 0 {
			m.SmallFinding = f.ID
		}
	}
	b.progress("reporting", 1, 1)
	return nil
}
