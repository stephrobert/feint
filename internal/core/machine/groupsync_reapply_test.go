package machine

import (
	"slices"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
)

// TestReapplyWritesTheWornSetsAndOnlyThose is cited by GroupSync.Reapply.
//
// ## What Reapply is for
//
// A reset restores the control plane and asks the runtime what it HOLDS, and
// Survey answers NAMES. A rule set whose content changed after the checkpoint
// keeps its name, so the observation cannot see the difference: the reset
// reports convergence while the runtime goes on enforcing rules the checkpoint
// does not describe. tools/conformance/scaleway/network.sh measured exactly
// that with a TCP connection, and this is the shared gesture that answers it.
//
// ## Why the wearer filter is asserted, and in the same test
//
// The first version wrote every restored group, and the runtime leg caught it
// in one pass: rule sets left over from earlier suites, whose machines were
// long swept, appeared on the host and the verdict reported them as
// differences the reset had created. A reset puts back what the checkpoint
// held; it does not furnish the host with objects the checkpoint never had,
// and a set attached to no interface enforces nothing by definition.
//
// The two halves are asserted together on purpose. A guard that refused every
// group would pass the second half alone, and would silently turn the whole
// re-application off — which is the defect this file exists to close.
func TestReapplyWritesTheWornSetsAndOnlyThose(t *testing.T) {
	b := newGroupSyncBench()
	worn := b.group("worn", "")
	orphan := b.group("orphan", "")
	b.machine("m-web", "10.42.0.9", "worn")

	done := b.sync().Reapply(t.Context(), []*resource.Resource{worn, orphan})

	// The worn set reached the runtime, with its rules.
	spec := ensuredSpec(t, b.rec, FirewallName("bench", "worn"))
	if len(spec.Rules) == 0 {
		t.Errorf("the re-applied set carries no rule: %+v", spec)
	}
	if !slices.Equal(done.Reapplied, []string{"worn"}) {
		t.Errorf("reapplied %v; the group a machine wears is the one to put back", done.Reapplied)
	}

	// And the orphan set did not, which is the half the host pays for.
	for _, e := range b.rec.Events() {
		if e.Kind == "EnsureFirewall" && e.Resource == FirewallName("bench", "orphan") {
			t.Fatalf("a reset created a rule set nobody wears: the checkpoint never held it, "+
				"and the verdict above reports it as a difference the reset itself made (%v)", e)
		}
	}
	if !slices.Equal(done.Skipped, []string{"orphan"}) {
		t.Errorf("skipped %v; a group nobody could act on is reported, never passed over in silence",
			done.Skipped)
	}
}

// TestReapplyOnNoRuntimeSaysSoRatherThanClaimingSuccess holds the degraded
// mode, which is the DEFAULT here: --vm off is what an ordinary run uses.
//
// Reporting these as Reapplied would have a reset tell an operator that rules
// are enforced again on a runtime that enforces nothing, which is the exact
// lie this emulator exists to avoid. Failed would be wrong too: nothing
// refused anything, and docs/limits.md documents the mode.
func TestReapplyOnNoRuntimeSaysSoRatherThanClaimingSuccess(t *testing.T) {
	b := newGroupSyncBench()
	group := b.group("worn", "")
	b.machine("m-web", "10.42.0.9", "worn")

	sync := b.sync()
	sync.Binding = Binding{Provider: "bench", Prefix: "feint-bench-", RuntimeKey: "machine"}

	done := sync.Reapply(t.Context(), []*resource.Resource{group})
	if len(done.Reapplied) != 0 || len(done.Failed) != 0 {
		t.Errorf("a binding with no runtime reported %+v", done)
	}
	if !slices.Equal(done.Skipped, []string{"worn"}) {
		t.Errorf("skipped %v", done.Skipped)
	}
}
