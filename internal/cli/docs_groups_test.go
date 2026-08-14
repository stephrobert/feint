package cli

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/drift"
)

// The fold exists so a 50-tag surface does not become a 50-row table, and its
// one hard rule is that folding must never hide a decision: a group big enough
// to be a block — Exoscale's 146 dbaas refusals — must always be named. These
// are the tests the guards in docs.go cite.

// exoscaleShaped is the real proportion that motivated the rule: one dominant
// declined block, one started group, and a tail of small ones.
func exoscaleShaped() []drift.GroupCount {
	return []drift.GroupCount{
		{Group: "dbaas", Total: 146, Declined: 146},
		{Group: "compute", Total: 111, Implemented: 42, Declined: 9, Unknown: 60},
		{Group: "sos", Total: 2, Declined: 2},
		{Group: "quotas", Total: 2, Implemented: 2},
		{Group: "audit-trail", Total: 1, Unknown: 1},
	}
}

// Named by foldSmallGroups's own comment: this is the test that fails if a
// group at or above the floor stops being named.
func TestFoldSmallGroupsNeverFoldsABlockDecision(t *testing.T) {
	named, rest, folded := foldSmallGroups(exoscaleShaped(), 262)

	names := make([]string, 0, len(named))
	for _, g := range named {
		names = append(names, g.Group)
	}
	if strings.Join(names, ",") != "dbaas,compute" {
		t.Fatalf("expected dbaas and compute named, largest first, got %v", names)
	}
	if folded != 3 {
		t.Fatalf("expected 3 folded groups, got %d", folded)
	}
	// Folding is about naming, never about numbers: the residual keeps every
	// count, so the columns still sum to the provider totals.
	if rest.Total != 5 || rest.Implemented != 2 || rest.Declined != 2 || rest.Unknown != 1 {
		t.Fatalf("the residual lost counts: %+v", rest)
	}
}

func TestFoldSmallGroupsLeavesALoneSmallGroupNamed(t *testing.T) {
	groups := []drift.GroupCount{
		{Group: "instance", Total: 150, Implemented: 48, Declined: 102},
		{Group: "marketplace", Total: 2, Declined: 2},
	}
	named, _, folded := foldSmallGroups(groups, 152)
	if folded != 0 || len(named) != 2 {
		t.Fatalf("a residual of one hides a name to save no space: named %v, folded %d", named, folded)
	}
}

func TestGroupRowsFallBackToProductsForAnOlderArtefact(t *testing.T) {
	rep := drift.CoverageFile{
		Provider: "scaleway",
		Products: []drift.ProductView{{Product: "instance", Total: 3, Implemented: 2, Declined: 1}},
	}
	rows := groupRows(rep)
	if len(rows) != 1 || rows[0].Group != "instance" || rows[0].Total != 3 {
		t.Fatalf("an artefact written before groups existed must still render its products: %+v", rows)
	}
}

// One line is one decision: a group refused for one reason collapses to one
// line however many operations it covers, and a group refused for two distinct
// reasons keeps two, because those are two decisions.
func TestDeclineBlocksFoldByDecisionNotByOperation(t *testing.T) {
	declined := []emulator.Decline{
		{Operation: "exoscale/v2.create-dbaas-a", Reason: "managed databases are refused"},
		{Operation: "exoscale/v2.create-dbaas-b", Reason: "managed databases are refused"},
		{Operation: "exoscale/v2.create-dbaas-c", Reason: "managed databases are refused"},
		{Operation: "exoscale/v2.assume-iam-role", Reason: "credentials are accepted on purpose"},
		{Operation: "exoscale/v2.list-iam-roles", Reason: "roles describe an access control nothing applies"},
	}
	groups := map[string]string{
		"exoscale/v2.create-dbaas-a":  "dbaas",
		"exoscale/v2.create-dbaas-b":  "dbaas",
		"exoscale/v2.create-dbaas-c":  "dbaas",
		"exoscale/v2.assume-iam-role": "iam",
		"exoscale/v2.list-iam-roles":  "iam",
	}
	sectionOf := func(op string) string {
		if g := groups[op]; g != "" {
			return g
		}
		return productOf(op)
	}
	blocks := declineBlocks(declined, sectionOf)

	if len(blocks) != 3 {
		t.Fatalf("expected 3 decisions, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].group != "dbaas" || blocks[0].n != 3 {
		t.Fatalf("expected the dbaas block first with its count intact: %+v", blocks[0])
	}
	if blocks[1].group != "iam" || blocks[1].n != 1 || blocks[2].group != "iam" || blocks[2].n != 1 {
		t.Fatalf("two reasons are two decisions and both must survive: %+v", blocks[1:])
	}
}
