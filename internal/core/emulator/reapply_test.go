package emulator_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// reapplyingPack implements the third half and records what it was handed.
type reapplyingPack struct {
	stubPack
	asked [][]string
}

func (p *reapplyingPack) ReconcileRules(_ context.Context, records []*resource.Resource) machine.Reconciled {
	var groups []string
	for _, res := range records {
		if res.Kind == "instance/security-group" {
			groups = append(groups, res.ID)
		}
	}
	p.asked = append(p.asked, groups)
	return machine.Reconciled{Reapplied: groups}
}

// TestAResetPutsTheRuleBackOnTheRuntimeAndNotOnlyInTheStore is cited by
// emulator.RuleReconciler and by machine.GroupSync.Reapply.
//
// ## The defect it closes
//
// An external audit deduced it and tools/conformance/scaleway/network.sh then
// measured it under --vm incus, with the only instrument whose verdict cannot
// be argued with:
//
//   - and a reset puts the rule back on the NIC, not only in the store
//     FAIL: the reset reported convergence and port 80 is still refused:
//     the rule came back in the store and not on the NIC
//
// A reset restores the control plane and asks the runtime what it HOLDS, and
// Survey answers NAMES. The rule set of a group whose rules changed after the
// checkpoint keeps its name, so the observation sees no difference at all: the
// reset reports convergence while the runtime goes on enforcing a state the
// checkpoint does not describe.
//
// ## The two properties this asserts, and why each would have been missed
//
//  1. the group is handed over although it is UNCHANGED by the restore. It is
//     not in `gone` and its machine is not missing, so both existing halves
//     skip it — and the whole point is that the record is identical and the
//     runtime is not;
//  2. it is handed over with NO surveyable runtime. Rule sets live on the
//     runtime whether or not any machine wears one, and the handler's guard
//     used to skip a pack entirely when nothing could be surveyed, which would
//     have taken this half with it.
func TestAResetPutsTheRuleBackOnTheRuntimeAndNotOnlyInTheStore(t *testing.T) {
	acme := &reapplyingPack{stubPack: stubPack{name: "acme"}}
	// The zero runtime, deliberately: see property 2.
	server, held := labServer(t, acme)

	put(t, held, "acme", "instance/security-group", "open-80")
	put(t, held, "acme", "instance/server", "web")
	if code, body := checkpointPost(t, server.URL+"/_feint/checkpoint/baseline"); code != http.StatusOK {
		t.Fatalf("checkpoint: %d %v", code, body)
	}

	// Nothing is added or removed between the checkpoint and the reset: the
	// rules of the group changed, which the store round-trips and the runtime
	// does not.
	code, body := checkpointPost(t, server.URL+"/_feint/reset/baseline")
	if code != http.StatusOK {
		t.Fatalf("reset: %d %v", code, body)
	}
	if len(acme.asked) != 1 {
		t.Fatalf("the pack was asked to re-apply its rules %d time(s); a reset must ask exactly once",
			len(acme.asked))
	}
	if len(acme.asked[0]) != 1 || acme.asked[0][0] != "open-80" {
		t.Fatalf("the pack was handed %v; the restored group is what its rule set is translated from",
			acme.asked[0])
	}
	// And the receipt says so, rather than leaving a reader to infer it from a
	// quiet success. Named apart from recreated: nothing was started.
	back, _ := body["reapplied"].([]any)
	if len(back) != 1 || back[0] != "open-80" {
		t.Errorf("the receipt reports reapplied=%v, so an operator cannot tell the rules were put back",
			body["reapplied"])
	}
}
