package machine

import (
	"context"

	"github.com/stephrobert/feint/internal/core/resource"
)

// The orchestration of security groups onto machines, written once (#509).
//
// #494 moved the per-rule-set mechanics into the shared layer (SyncRuleSet,
// ApplyRuleSets, DropRuleSet, firewall_binding.go). What stayed in the packs
// was the skeleton above the mechanics — sync a group then replay it on its
// wearers, collect one machine's sets and apply them in one call, re-expand
// the groups referencing a machine's groups once it has addresses — copied two
// to three times, line-for-line isomorphic. That layer is exactly where #475
// was born: a sequence written per pack is a sequence two packs never wrote,
// and a fourth pack would have rediscovered it alone.
//
// What stays in a pack is only what that provider knows: how a group and its
// rules translate into a FirewallSpec, who wears a group, which groups a
// resource wears, and which groups reference a group as a member source. Each
// of those is a field below, so no provider name enters this file and
// internal/cli's TestTheCoreNamesNoProvider remains the judge.
type GroupSync struct {
	// Binding is the pack's machine binding: the driver, the runtime key the
	// machine name lives under, and the rule-set mechanics of #494.
	Binding Binding

	// SpecOf translates one group and its rules into the runtime's rule set —
	// the pack's own vocabulary, member expansion included.
	//
	// fresh, when non-nil, is the copy a transition is still working on: the
	// boot that triggers a re-expansion runs before its own commit, so the
	// store's copy of that resource does not yet carry the address the
	// expansion is being run FOR. Reading the store alone here silently missed
	// the very machine that booted — measured on Outscale, unproven but
	// structurally identical on Exoscale, and now impossible to forget because
	// the skeleton threads fresh for every pack.
	// TestAFreshResourceWinsOverItsStaleStoreCopy fails without the threading.
	SpecOf func(group, fresh *resource.Resource) FirewallSpec

	// ForeignBlocks, optional, lists the CIDR blocks the wearers of this group
	// must not reach when the runtime cannot keep networks apart by
	// construction (bridge mode). The rejects ride the group's rule set
	// because a NIC carrying a rule set no longer obeys the network-level
	// isolation — the runtime orders rules by action, so a reject here wins
	// over any allow the group adds. What is foreign is the pack's own routing
	// model and stays behind this field; where the rejects go is runtime
	// knowledge and is decided here, once.
	//
	// Nil keeps today's behaviour for the packs that never embedded the
	// defence: bridge mode declares isolation:false for every pack, so their
	// nil is honest, and wiring their predicates here would change measured
	// bridge-mode rule sets without a witness asking for it.
	ForeignBlocks func(group *resource.Resource) []string

	// Wearers enumerates the resources wearing a group, for the replay after
	// the group or its rules change.
	Wearers func(group *resource.Resource) []*resource.Resource

	// WornIDs enumerates the identifiers of the groups one resource wears.
	WornIDs func(res *resource.Resource) []string

	// Group resolves a worn identifier to its stored group.
	Group func(id string) (*resource.Resource, bool)

	// Referrers, optional, lists the groups one of whose rules names one of
	// the given groups as a member source. Nil where the provider's rules
	// cannot name a group at all (Scaleway), in which case the re-expansion
	// half of AfterBoot is honestly empty rather than emptily looped.
	Referrers func(named map[string]bool) []*resource.Resource
}

// enforcer is the runtime's firewall half, nil when it has none — the
// assertion every pack wrote for itself, now out of their vocabulary entirely,
// which is what lets the enforcement test mark a wired pack by this type
// instead of by machine.Firewaller.
func (s GroupSync) enforcer() Firewaller {
	fw, _ := s.Binding.Driver.(Firewaller)
	return fw
}

// nativeIsolation reports whether the runtime keeps its networks apart by
// construction, in which case reject rules against foreign subnets are dead
// weight: the blocks would name subnets the machine cannot reach anyway.
func (s GroupSync) nativeIsolation() bool {
	peerer, ok := s.Binding.Driver.(Peerer)
	return ok && peerer.NativeIsolation()
}

// spec assembles the rule set of one group: the pack's translation, with the
// bridge-mode foreign rejects in front. In front and not by precedence — the
// runtime orders rules by action, not by position — but keeping them first
// keeps the written set byte-identical to what the packs measured.
func (s GroupSync) spec(group, fresh *resource.Resource) FirewallSpec {
	spec := s.SpecOf(group, fresh)
	if s.ForeignBlocks == nil || s.nativeIsolation() {
		return spec
	}
	blocks := s.ForeignBlocks(group)
	if len(blocks) == 0 {
		return spec
	}
	rejects := make([]FirewallRule, 0, 2*len(blocks))
	for _, block := range blocks {
		rejects = append(rejects,
			FirewallRule{Direction: "egress", Action: "reject", Destination: block},
			FirewallRule{Direction: "ingress", Action: "reject", Source: block},
		)
	}
	spec.Rules = append(rejects, spec.Rules...)
	return spec
}

// SyncGroup writes one group's rule set to the runtime and replays it onto
// every wearer. Called after any change to the group or to its rules.
//
// fresh, when non-nil, wins over the store's copy of the same resource, in the
// wearer replay as in the expansion — SpecOf says why.
func (s GroupSync) SyncGroup(ctx context.Context, group, fresh *resource.Resource) {
	fw := s.enforcer()
	if fw == nil {
		return
	}
	if !s.Binding.SyncRuleSet(ctx, fw, s.spec(group, fresh)) {
		return
	}
	for _, wearer := range s.Wearers(group) {
		if fresh != nil && wearer.ID == fresh.ID {
			wearer = fresh
		}
		s.applyMachine(ctx, wearer, fresh)
	}
}

// ApplyMachine writes every set one resource wears and attaches them to its
// machine in one call, because the runtime replaces the attachment list rather
// than merging it. Every set is written first, so an attach cannot name a set
// the runtime has never seen. The resource is its own fresh copy: its groups
// are being applied because of what it just became.
//
// The sets are written even when no machine is running: a rule set lives on
// the runtime for every wearer at once, and this resource may be the reason
// its translation changed — a NIC created on a stopped server changes the
// foreign blocks its group must reject. Only the attach half needs a machine.
//
// A set the runtime refused is not applied at all rather than applied minus
// the refused set: dropping one deny-carrying set from the attachment list
// would open traffic the API describes as closed, and the refusal is already
// in the log (SyncRuleSet). Two packs of three attached the remainder; the
// conservative form is the one that cannot lie in the permissive direction.
func (s GroupSync) ApplyMachine(ctx context.Context, res *resource.Resource) {
	s.applyMachine(ctx, res, res)
}

// applyMachine is ApplyMachine with the pass's fresh copy threaded through.
//
// One fresh per orchestration pass, everywhere: when a wearer replay re-writes
// a set the pass has already written with the booting resource's copy,
// re-translating it with the wearer's own store copy instead would overwrite
// the fresh expansion with a stale one — the booting machine's address,
// written a moment ago, silently erased by the very replay that was meant to
// deliver it. One pack's copy dodged this by skipping machineless wearers
// entirely, which hid the overwrite without removing it.
// TestTheWearerReplayKeepsTheFreshExpansion fails without the threading.
func (s GroupSync) applyMachine(ctx context.Context, res, fresh *resource.Resource) {
	fw := s.enforcer()
	if fw == nil {
		return
	}
	if fresh == nil {
		fresh = res
	}
	specs := make([]FirewallSpec, 0, 2)
	for _, id := range s.WornIDs(res) {
		group, found := s.Group(id)
		if !found {
			continue
		}
		spec := s.spec(group, fresh)
		if !s.Binding.SyncRuleSet(ctx, fw, spec) {
			return
		}
		specs = append(specs, spec)
	}
	s.Binding.ApplyRuleSets(ctx, fw, res.Runtime[s.Binding.RuntimeKey], specs...)
}

// AfterBoot runs once a machine exists or first publishes an address: its own
// sets attach, and every group whose rules point at a group this resource
// wears is re-expanded, because this machine's addresses are now part of what
// those groups mean. Without the second half, the tiering statement a stack
// writes — tier 2 accepts tier 1 and nobody else — never contains the machines
// it talks about (#475).
func (s GroupSync) AfterBoot(ctx context.Context, res *resource.Resource) {
	if s.enforcer() == nil {
		return
	}
	s.ApplyMachine(ctx, res)
	s.SyncReferrers(ctx, s.WornIDs(res), res)
}

// SyncReferrers re-syncs every group one of whose rules names one of the given
// groups as a member source. fresh carries the resource whose addresses
// changed, ahead of its own commit — SpecOf says why the store is not enough.
func (s GroupSync) SyncReferrers(ctx context.Context, groupIDs []string, fresh *resource.Resource) {
	if s.Referrers == nil || len(groupIDs) == 0 || s.enforcer() == nil {
		return
	}
	named := make(map[string]bool, len(groupIDs))
	for _, id := range groupIDs {
		named[id] = true
	}
	for _, group := range s.Referrers(named) {
		s.SyncGroup(ctx, group, fresh)
	}
}

// Drop removes one group's rule set from the runtime, for a group being
// deleted. Failure is logged rather than fatal — DropRuleSet says why.
func (s GroupSync) Drop(ctx context.Context, name string) {
	s.Binding.DropRuleSet(ctx, s.enforcer(), name)
}
