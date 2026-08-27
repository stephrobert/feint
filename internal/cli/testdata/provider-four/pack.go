// Package providerfour is the fourth provider, written as code so the
// milestone's promise stops being a sentence (#517).
//
// # It is not a cloud, and that is the point
//
// Nothing here imitates a real provider. The intended fourth pack is OVHcloud,
// and nothing of its API has ever been measured in this repository — so a fake
// pack shaped like OVH would be measuring a belief, which rule 4 forbids in
// exactly those words. What this measures is the contract: the services
// internal/core/machine offers a pack, and whether they suffice for a
// newcomer that never had the three real packs' habits.
//
// So Provider Four is imaginary and deliberately minimal. It has nodes,
// segments, barriers, anchors and spreaders — five words it made up — and it
// carries no trait a real cloud would recognise. Its shape was chosen from the
// dataplane #517 asks for, never from a cloud: a node boots on one home
// segment, joins more afterwards, and wears barriers whose rules may name
// another barrier. The day a real fourth pack starts, this is the recipe it
// reads, never the model it copies.
//
// # What it never names
//
// No runtime implementation, no Incus, no OVN, no machine.Driver — and the
// last one is not a discipline but a build error since #511: emulator.Env
// hands out no driver value and machine.Binding's driver field is unexported.
// What stays reachable without a driver value is held by internal/cli's
// TestNoPackReachesPastTheDeclaredDriverSurface and
// TestNoPackKnowsWhichRuntimeIsBehindTheDriver, which read this directory
// beside the three real packs, with no exemption of its own.
//
// # Where it lives, and why no instrument counts it
//
// Under testdata/, which Go's ./... patterns skip. Measured on 2026-08-27
// rather than assumed, which is what #517 asks for: `go build ./...` does not
// build this directory and `go list ./...` does not name it, so no coverage,
// evidence or drift artefact can count a fourth provider that has no upstream
// SDK — an entry there would be a measured lie. An explicit import still
// resolves, which is how it compiles at all, and `gofmt -l .` still walks in,
// so `mise run check` holds its shape like any other file.
//
// It is never mounted and has no routes: it proves the contract's shape, never
// wire fidelity. The polar star stays with scw, the exo CLI and Terraform.
package providerfour

import (
	"net/netip"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Name is what this provider calls itself, in the store and on every label the
// shared layer writes.
const Name = "four"

// The kinds Provider Four stores. Five words of its own, because a pack's
// vocabulary is its own business and reusing another cloud's would be the
// first place this fake started measuring something other than the contract.
const (
	KindNode     = "node"     // a machine
	KindSegment  = "segment"  // a private network
	KindBarrier  = "barrier"  // a rule set some nodes wear
	KindAnchor   = "anchor"   // a public address
	KindSpreader = "spreader" // a balancer
)

// The node states this provider publishes. RunningState and FailedState below
// are these; the words are the pack's, which is the whole reason those two are
// fields rather than constants of the core.
const (
	StateDown   = "down"
	StateUp     = "up"
	StateBroken = "broken"
)

// DefaultUser is the login Provider Four provisions on every image. One login
// for the whole cloud, which is the simple half of the fork Boot.User exists
// for.
const DefaultUser = "pilot"

// machinePrefix names this pack's machines on a shared host, beside
// feint-scw-, feint-osc- and feint-exo-. It is what Binding.ours checks a
// stored name against before any destructive verb, and what a sweep
// recognises: a fourth pack that picked a prefix already in use would be
// asking the shared layer to hand it another pack's machines.
const machinePrefix = "feint-four-"

// rulesetPrefix names this pack's rule sets on the host, through
// machine.FirewallName.
const rulesetPrefix = "four"

// The Runtime keys this pack keeps its host-side names under. Runtime and
// never Attrs: a view must not be able to serialise them, and the shared layer
// writes the first two itself.
const (
	runtimeNodeKey    = "four-node"
	runtimeAddressKey = "four-address"
	runtimeSegmentKey = "four-segment"
)

// segmentMarker labels a backing network with the segment it backs, so a sweep
// can tell an orphan from a live one.
const segmentMarker = "feint.segment"

// publicBlock is the range Provider Four hands anchors out of, and the only
// range it may ask the runtime to route.
//
// It guards Reconciler.Route and Reconciler.Unroute alike, because a stored
// address is untrusted input: PUT /_feint/state and `feint snapshot load`
// restore it verbatim, and routing an arbitrary value would send the host's
// traffic for that address into a container. RFC 2544's benchmarking range,
// picked so it cannot collide with the three real packs' documentation blocks.
var publicBlock = netip.MustParsePrefix("198.18.0.0/24")

// images is this pack's catalogue: the one table only this provider knows.
// An identifier that resolves to nothing is refused at boot rather than
// substituted, which is the shared layer's rule and not this pack's choice.
var images = map[string]machine.Image{
	"four-linux": {Ref: "debian:13", User: DefaultUser},
}

// Pack is Provider Four's control plane. It holds an environment and nothing
// else — in particular no runtime, which since #511 it could not hold if it
// wanted to.
type Pack struct {
	env *emulator.Env
}

// New returns a pack over env.
func New(env *emulator.Env) *Pack { return &Pack{env: env} }

// tenant is the isolation boundary every resource of this pack belongs to.
// Provider Four scopes by nothing but itself, which is the minimum a tenant
// can be and still be one.
func tenant() resource.Tenant { return resource.Tenant{Provider: Name} }

// binding is this pack's declaration of its machines: what it names them, what
// login they carry, where their host-side name and address are kept, and the
// two state words its API publishes.
//
// Everything here is a field, and that is the contract's own claim: what
// varies between providers is a value, never a re-implementation. The driver
// arrives through emulator.Env.Bind and never through this pack — the field it
// lands in is unexported, so no expression below can reach it.
func (p *Pack) binding() machine.Binding {
	return p.env.Bind(machine.Binding{
		Provider:     Name,
		Prefix:       machinePrefix,
		User:         DefaultUser,
		RuntimeKey:   runtimeNodeKey,
		AddressKey:   runtimeAddressKey,
		RunningState: StateUp,
		FailedState:  StateBroken,
		Declared:     p.env.BootImages,
		Log:          p.env.Log,
	})
}

// groups is this pack's firewall orchestrator. The five fields below are the
// whole of what Provider Four knows about its own barriers; the sequence that
// consumes them — write the set, replay it onto every wearer, re-expand the
// barriers that name it — is the shared skeleton's, written once for every
// pack that will ever exist.
func (p *Pack) groups() machine.GroupSync {
	return machine.GroupSync{
		Binding:   p.binding(),
		SpecOf:    p.barrierSpec,
		Wearers:   p.barrierWearers,
		WornIDs:   p.wornBarriers,
		Group:     p.barrier,
		Referrers: p.barrierReferrers,
	}
}

// reconciler is this pack's interface orchestrator: it starts a node on the
// plan below and replays the one order — the promised addresses, then the
// memberships, the firewall last.
//
// PublicBlock is on the reconciler rather than on the plan because the unroute
// of a node already gone has no resource left to plan from.
func (p *Pack) reconciler() machine.Reconciler {
	return machine.Reconciler{
		Groups:      p.groups(),
		PlanOf:      p.planOf,
		PublicBlock: publicBlock,
	}
}

// planOf declares one node's interface shape: what rides the launch, what
// joins afterwards, and which public addresses were promised.
//
// This is the whole of what a pack contributes to the boot. The order those
// three are delivered in is the runtime's property and lives in
// machine.Reconciler; a pack that wrote it here would be writing the recipe
// #510 took out of three packs' comments.
func (p *Pack) planOf(res *resource.Resource) machine.Plan {
	// Built as a literal, and that is not a matter of style: the declared
	// surface admits Plan.Memberships and no other member of Plan, so
	// `plan.Boot = …` is a discipline failure with nothing in the type to warn
	// a newcomer. The three real packs write it exactly this way, for exactly
	// this reason.
	plan := machine.Plan{
		Boot:    p.bootAttachments(res),
		Publics: p.anchorsOf(res.ID),
	}
	for _, id := range membershipsOf(res) {
		if att, ok := p.attachment(res, id); ok {
			plan.Memberships = append(plan.Memberships, att)
		}
	}
	return plan
}

// bootAttachments is the one interface that rides the launch: the node's home
// segment, when it has one.
//
// Carried on the start rather than attached afterwards because editing a live
// interface re-plugs it and the guest loses its lease — which is the shared
// layer's measurement and not this pack's, and the reason Plan carries two
// lists instead of one.
func (p *Pack) bootAttachments(res *resource.Resource) []machine.Attachment {
	home := attrString(res, "home_segment")
	if home == "" {
		return nil
	}
	att, ok := p.attachment(res, home)
	if !ok {
		return nil
	}
	return []machine.Attachment{att}
}

// attachment turns one segment membership into the interface the node carries
// on it: the backing network, the address this pack allocated in the segment's
// block, and that block's mask.
//
// A segment with no backing network yet yields nothing: the membership is
// still true in the store, and the interface arrives with the next boot's
// plan. That is the shared layer's rule for a missing network, and stating it
// here rather than attaching to an empty name is what keeps it true.
func (p *Pack) attachment(node *resource.Resource, segmentID string) (machine.Attachment, bool) {
	segment, found := p.env.Store.Get(Name, KindSegment, segmentID)
	if !found || segment.Runtime[runtimeSegmentKey] == "" {
		return machine.Attachment{}, false
	}
	block, err := netip.ParsePrefix(attrString(segment, "block"))
	if err != nil {
		return machine.Attachment{}, false
	}
	address := addressesOf(node)[segmentID]
	if address == "" {
		return machine.Attachment{}, false
	}
	return machine.Attachment{
		Network:   segment.Runtime[runtimeSegmentKey],
		Address:   address,
		PrefixLen: block.Bits(),
	}, true
}
