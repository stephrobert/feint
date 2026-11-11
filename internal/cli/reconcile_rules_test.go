package cli

import (
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// TestAPackThatEnforcesRulesReappliesThemOnReset is cited by
// emulator.RuleReconciler.
//
// A pack that pushes its rules onto the runtime owns a second promise it is
// easy not to notice: bringing them BACK. A reset restores the control plane
// and asks the runtime what it holds, and Survey answers names. A rule set
// whose content changed after the checkpoint keeps its name, so the
// observation sees no difference, the reset reports convergence, and the
// runtime goes on enforcing the rules of a state nobody asked to return to.
// The network suite measured that with a TCP connection before this interface
// existed.
//
// The subject is read from the pack — "does it declare that it enforces" —
// rather than from a list of provider names, so a fourth pack inherits the
// obligation without anyone editing this file. That is rule 5 applied to a
// test, and the same shape as its neighbour
// TestAPackThatDrivesMachinesReconcilesBothWays.
func TestAPackThatEnforcesRulesReappliesThemOnReset(t *testing.T) {
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}

	examined := 0
	for _, pack := range srv.Packs() {
		enforcer, declares := pack.(emulator.FirewallEnforcer)
		if !declares || !enforcer.EnforcesFirewall() {
			// A pack whose rules stay metadata has nothing to put back.
			continue
		}
		examined++
		if _, reapplies := pack.(emulator.RuleReconciler); !reapplies {
			t.Errorf("%s pushes its rules onto the runtime and cannot put them back: a reset "+
				"would report convergence while the runtime still enforces the rules of a "+
				"state the checkpoint does not describe", pack.Name())
		}
	}

	// A denominator, because a loop over nothing passes silently.
	if examined < 3 {
		t.Fatalf("only %d pack(s) declare that they enforce rules; this build has three", examined)
	}
}
