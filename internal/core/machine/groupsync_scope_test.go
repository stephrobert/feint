package machine

import (
	"context"
	"slices"
	"testing"
)

// The scope a pack declares travels from its plan to the binding the driver
// applies, and it is read from both halves of that plan (#574).
//
// Both halves on purpose: which half an interface rides in is a fact about
// *when* it is created — with the launch or after it — and says nothing about
// whether a security group reaches it. A reader that only walked Memberships
// would be right for the provider that motivated this and wrong for the next
// one, which is the same defect one interface to the left.
func TestTheUnfilteredScopeIsReadFromBothHalvesOfThePlan(t *testing.T) {
	b := newGroupSyncBench()
	group := b.group("g-1", "")
	res := b.machine("m-1", "10.0.0.7", "g-1")

	sync := b.sync()
	sync.PlanOf = func(*testResource) Plan {
		return Plan{
			Boot: []Attachment{
				{Network: "fnt-public"},
				{Network: "fnt-bootpriv", Unfiltered: true},
			},
			Memberships: []Attachment{
				{Network: "fnt-privnet", Unfiltered: true},
				// Declared twice, the way a membership re-declared after a
				// boot is; the driver reads this as a set.
				{Network: "fnt-privnet", Unfiltered: true},
				{Network: "fnt-managed"},
			},
		}
	}
	sync.SyncGroup(context.Background(), group, res)

	binding := appliedBinding(t, b.rec, res.Runtime["machine"])
	want := []string{"fnt-bootpriv", "fnt-privnet"}
	if len(binding.Unfiltered) != len(want) {
		t.Fatalf("the applied binding declares %v, want exactly %v", binding.Unfiltered, want)
	}
	for _, network := range want {
		if !slices.Contains(binding.Unfiltered, network) {
			t.Errorf("the applied binding does not declare %s out of scope: %v", network, binding.Unfiltered)
		}
	}
	// The accepting half: a network the pack did not declare stays covered, so
	// the scope cannot quietly become "nothing is filtered anywhere".
	for _, network := range []string{"fnt-public", "fnt-managed"} {
		if slices.Contains(binding.Unfiltered, network) {
			t.Errorf("%s was never declared out of scope, and the binding excuses it: %v", network, binding.Unfiltered)
		}
	}
	if len(binding.Names) == 0 {
		t.Error("the machine wears a restrictive group and the binding names no rule set at all")
	}
}

// A pack that declares no plan to the orchestrator declares no exemption
// either: the binding covers every interface, which is what the two packs that
// do filter their private NICs send, and what every pack sent before #574.
func TestAPackThatDeclaresNoPlanExemptsNothing(t *testing.T) {
	b := newGroupSyncBench()
	group := b.group("g-1", "")
	res := b.machine("m-1", "10.0.0.7", "g-1")

	b.sync().SyncGroup(context.Background(), group, res)

	if binding := appliedBinding(t, b.rec, res.Runtime["machine"]); len(binding.Unfiltered) != 0 {
		t.Fatalf("a pack declaring no plan must exempt nothing, got %v", binding.Unfiltered)
	}
}

// appliedBinding is the last firewall binding one machine received, read off
// the shared contract recorder.
func appliedBinding(t *testing.T, rec *Recorder, machine string) FirewallBinding {
	t.Helper()
	var found *FirewallBinding
	for _, e := range rec.Events() {
		if e.Kind == "ApplyFirewall" && e.Resource == machine {
			binding := e.Args.(FirewallBinding)
			found = &binding // the last write is what the runtime holds
		}
	}
	if found == nil {
		t.Fatalf("the runtime never received a firewall binding for %s", machine)
	}
	return *found
}
