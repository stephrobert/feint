package cli

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/drift"
)

// TestTheCoverageHeadlineNamesItsPopulation is cited by renderCoverage.
//
// The generated headline read "Of the 548 operations upstream declares", and
// the Scaleway scan covers 11 of the 56 products in that SDK
// (`coverage/scaleway-baseline.json`). So 548 is the surface of a third of the
// provider, printed on the README's front page as the provider's surface, with
// the scoping stated only at `docs/limits.md:3316`.
//
// That is this repository's cardinal failure — a verdict that does not name its
// population — and it had been shipping in the one document most readers see
// first. A reader with `scaleway_rdb_instance` could not learn from it that
// their stack is outside the scan entirely, and `docs/adoption.md` says what
// then happens: the client reaches the real endpoint with whatever credentials
// the shell holds.
//
// The assertion is on the sentence rather than on a number, because the numbers
// move with every upstream scan and the population clause must not.
func TestTheCoverageHeadlineNamesItsPopulation(t *testing.T) {
	reports := []drift.CoverageFile{{
		Provider:    "scaleway",
		Total:       548,
		Implemented: 204,
		Declined:    344,
		Products: []drift.ProductView{
			{Product: "instance"}, {Product: "vpc"}, {Product: "lb"},
		},
	}}

	out := renderCoverage(reports, map[string]map[string]int{
		"scaleway": {"instance": 120, "vpc": 50, "lb": 34},
	})

	if !strings.Contains(out, "in the 3 product(s)\nthis scan covers") {
		t.Errorf("the headline does not name the population it counted:\n%s", out)
	}
	// And the bare form must be gone, not merely supplemented: a sentence that
	// still says "upstream declares: 37% served" one line above the scoped one
	// leaves the misreading available.
	if strings.Contains(out, "operations upstream declares:") {
		t.Errorf("the unscoped headline is still printed:\n%s", out)
	}
}
