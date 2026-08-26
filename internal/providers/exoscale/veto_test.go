package exoscale

import (
	"strings"
	"testing"
)

// The veto is a refusal with its reasons attached, not a switch. It has to
// name the decision's condition (upstream #573), the incident that fixed the
// decision (#525), and the client that remains — a wall with no door beside it
// gets worked around by copying the emulator, and CLAUDE.md has written that
// down twice.
func TestTheVetoNamesTheDecisionTheIssueAndTheRemainingClient(t *testing.T) {
	pack := New(nil)
	// Both engines, because OpenTofu resolves the same published provider from
	// the same registry namespace.
	for _, engine := range []string{"terraform", "opentofu"} {
		t.Run(engine, func(t *testing.T) {
			reason := pack.VetoEngine(engine)
			if reason == "" {
				t.Fatalf("the %s engine is not vetoed: #525's five requests to "+
					"api-ch-*.exoscale.com are one `feint up` away again", engine)
			}
			for _, want := range []string{"#573", "#525", "exo", "splits"} {
				if !strings.Contains(reason, want) {
					t.Errorf("the veto never says %q: %s", want, reason)
				}
			}
		})
	}
	// The accepting half: the veto refuses two engines, not every word a
	// future schema might put in `iac.engine`.
	if reason := pack.VetoEngine("pulumi"); reason != "" {
		t.Errorf("an engine this decision never measured is vetoed: %s", reason)
	}
}
