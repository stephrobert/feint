package machine

import (
	"context"
	"fmt"
	"strings"

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

	// EnforcesNothing declares a pack that hands no rules to the runtime at
	// all, so its empty translation is a statement rather than an omission.
	//
	// The zero value is the loud one, and that is the whole design (#543). A
	// pack that forgot to wire the four fields above and a pack that means to
	// enforce nothing are indistinguishable from here, and the one that must
	// not be silent is the one that forgot: it used to compile, pass every
	// unit test and every conformance leg under `--vm off`, and then panic on
	// the operator's first poweron under a real runtime. So enforcing is
	// assumed and not-enforcing is declared — the GroupSync half of the
	// sentence emulator.FirewallEnforcer already asks a pack to say out loud.
	EnforcesNothing bool
}

// required names the fields the orchestration dereferences, in the order a
// pack reads them off the struct.
//
// Referrers and ForeignBlocks are absent because they are documented optional
// and nil-checked at their call sites: a provider whose rules cannot name a
// group has nothing for the first, and a runtime that keeps networks apart by
// construction has nothing for the second.
func (s GroupSync) required() []struct {
	name string
	set  bool
} {
	return []struct {
		name string
		set  bool
	}{
		{"SpecOf", s.SpecOf != nil},
		{"Wearers", s.Wearers != nil},
		{"WornIDs", s.WornIDs != nil},
		{"Group", s.Group != nil},
	}
}

// check reports the required fields this orchestrator does not carry, naming
// the pack and every one of them.
//
// It says nothing about EnforcesNothing: wired() asks that first, because a
// pack that hands over nothing has no field to be missing, and folding the two
// questions into one answer made the predicate impossible to falsify — a
// mutation removing either half left the other covering for it, which is a
// guard no test can kill.
//
// Why an error rather than a nil check at each call site: the four fields are
// dereferenced from five places, two of them inside a loop, so a nil check per
// site is five chances to miss one and a stack trace when somebody does. What
// an operator needs is the pack and the field, in one sentence, the first time
// the pack is asked to do anything.
//
// internal/cli's TestAPackThatWiredNoGroupSyncIsToldWhichFieldIsMissing fails
// without this.
func (s GroupSync) check() error {
	var missing []string
	for _, field := range s.required() {
		if !field.set {
			missing = append(missing, field.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	provider := s.Binding.Provider
	if provider == "" {
		provider = "an unnamed"
	}
	return fmt.Errorf("the %s pack builds a machine.GroupSync without %s: "+
		"a pack that hands its groups to the runtime wires all of %s, and a pack that hands over "+
		"nothing says so with EnforcesNothing",
		provider, strings.Join(missing, ", "), strings.Join(fieldNames(s.required()), ", "))
}

func fieldNames(fields []struct {
	name string
	set  bool
},
) []string {
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		out = append(out, field.name)
	}
	return out
}

// wired reports whether the firewall step can run at all, and says why not
// when it cannot.
//
// The order of the two questions is the correction #543 asks for, and it is
// the whole of it. Asking `enforcer() == nil` first is what the layer used to
// do, and it made the omission mode-dependent: under `--vm off` — the default,
// and what every conformance leg and `mise run check` run — the firewall step
// returned before it could touch a nil field, so a pack that had wired nothing
// was green everywhere; under `--vm incus-ovn` the first poweron dereferenced
// a nil func. The population that would have caught it is the one nobody runs
// by default, which is CLAUDE.md's own rule pointing the wrong way.
//
// Asking check() first makes the omission report itself in both modes, at the
// same moment, in the same words. What the runtime changes is only whether
// there is anything to enforce afterwards.
//
// It degrades rather than refusing, because a machine runtime failing must
// never break the control plane: the machine boots, its rules do not reach the
// host, and the operator is told why. Reconciler.PlanOf is the deliberate
// opposite — plan.go says why.
//
// internal/cli's TestABootUnderAnEnforcingRuntimeReportsAnUnwiredGroupSync and
// TestAnUnwiredGroupSyncIsReportedUnderEveryRuntime fail without this.
func (s GroupSync) wired() bool {
	// The declaration first, and silently: a pack that says it enforces
	// nothing has nothing missing, and reporting it would be a refusal nobody
	// can satisfy — which is the shape somebody works around rather than
	// answers. A pack that declares this and wires the fields anyway still
	// enforces nothing; the declaration is the pack speaking about itself.
	if s.EnforcesNothing {
		return false
	}
	if err := s.check(); err != nil {
		s.Binding.logger().Error("the firewall step is skipped: this pack's security groups reach no machine",
			"provider", s.Binding.Provider, "error", err)
		return false
	}
	return s.enforcer() != nil
}

// enforcer is the runtime's firewall half, nil when it has none — the
// assertion every pack wrote for itself, now out of their vocabulary entirely,
// which is what lets the enforcement test mark a wired pack by this type
// instead of by machine.Firewaller.
func (s GroupSync) enforcer() Firewaller {
	fw, _ := s.Binding.driver.(Firewaller)
	return fw
}

// nativeIsolation reports whether the runtime keeps its networks apart by
// construction, in which case reject rules against foreign subnets are dead
// weight: the blocks would name subnets the machine cannot reach anyway.
func (s GroupSync) nativeIsolation() bool {
	peerer, ok := s.Binding.driver.(Peerer)
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
	if !s.wired() {
		return
	}
	fw := s.enforcer()
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
	if !s.wired() {
		return
	}
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
	// No second wiring guard here, and that absence is deliberate rather than
	// an oversight. Every caller — SyncGroup, ApplyMachine, AfterBoot — gates
	// on wired() first, so a repeat would be a guard no mutation can redden:
	// the falsification for #543 planted exactly that one and reported STILL
	// GREEN, because the entry gate covered for it. A guard whose removal
	// leaves every test passing is a comment. What holds a future caller
	// instead is the entry points being the only doors, which
	// internal/cli's TestNoPackReachesPastTheDeclaredDriverSurface already
	// keeps closed.
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
	// wired() rather than `enforcer() == nil`, and that swap is #543: this
	// line is the guard that made a missing field visible under one runtime
	// and invisible under the other, because it returned before anything could
	// dereference one. It now reports first and asks about the runtime second.
	if !s.wired() {
		return
	}
	s.applyMachine(ctx, res, res)
	s.SyncReferrers(ctx, s.WornIDs(res), res)
}

// SyncReferrers re-syncs every group one of whose rules names one of the given
// groups as a member source. fresh carries the resource whose addresses
// changed, ahead of its own commit — SpecOf says why the store is not enough.
func (s GroupSync) SyncReferrers(ctx context.Context, groupIDs []string, fresh *resource.Resource) {
	if s.Referrers == nil || len(groupIDs) == 0 || !s.wired() {
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
