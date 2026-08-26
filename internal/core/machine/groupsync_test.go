package machine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
)

// The witnesses of #509: the skeleton — sync-then-apply, the wearer replay,
// the fresh copy, the after-boot re-expansion, the bridge-mode rejects — is
// asserted here once, against the shared contract recorder, so the discipline
// holds for every pack at once instead of living in whichever pack happened to
// write the test. The fresh test transposes internal/providers/outscale's
// TestAMemberSourcedRuleExpandsToTheMembersAddresses, which held the only copy
// of that discipline before this file.

// groupSyncBench builds a GroupSync over a recorder and a tiny in-memory
// world: groups and machines are plain resources, a group's spec expands the
// addresses of the wearers of the group its one rule names — the shape of
// every member-sourced rule, without any provider's vocabulary.
type groupSyncBench struct {
	rec *Recorder
	// resources by id: groups and machines alike, standing in for the store.
	resources map[string]*testResource
	// wearing maps a machine id to the group ids it wears.
	wearing map[string][]string
	// references maps a group id to the group id its one rule names, "" for a
	// plain rule.
	references map[string]string
}

type testResource = resource.Resource

func newGroupSyncBench() *groupSyncBench {
	return &groupSyncBench{
		rec:        NewRecorder(),
		resources:  map[string]*testResource{},
		wearing:    map[string][]string{},
		references: map[string]string{},
	}
}

func (b *groupSyncBench) binding() Binding {
	return Binding{
		Driver:     b.rec,
		Provider:   "bench",
		Prefix:     "feint-bench-",
		RuntimeKey: "machine",
		AddressKey: "address",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (b *groupSyncBench) sync() GroupSync {
	return GroupSync{
		Binding: b.binding(),
		SpecOf: func(group, fresh *testResource) FirewallSpec {
			spec := FirewallSpec{
				Name:           FirewallName("bench", group.ID),
				DefaultIngress: "drop",
				DefaultEgress:  "drop",
			}
			target := b.references[group.ID]
			if target == "" {
				spec.Rules = append(spec.Rules, FirewallRule{Direction: "ingress", Action: "allow", PortFrom: 22, PortTo: 22})
				return spec
			}
			// The member expansion: one /32 per address a wearer of the named
			// group answers on — with the transition's own copy winning over
			// the store's, which is the whole fresh discipline.
			for _, wearer := range b.wearersOf(target) {
				if fresh != nil && wearer.ID == fresh.ID {
					wearer = fresh
				}
				if address, _ := wearer.Attrs["address"].(string); address != "" {
					spec.Rules = append(spec.Rules, FirewallRule{
						Direction: "ingress", Action: "allow", Source: address + "/32",
					})
				}
			}
			return spec
		},
		Wearers: func(group *testResource) []*testResource { return b.wearersOf(group.ID) },
		WornIDs: func(res *testResource) []string { return b.wearing[res.ID] },
		Group: func(id string) (*testResource, bool) {
			res, found := b.resources[id]
			if !found {
				return nil, false
			}
			return res.Clone(), true
		},
		Referrers: func(named map[string]bool) []*testResource {
			var out []*testResource
			for id, target := range b.references {
				if named[target] {
					out = append(out, b.resources[id])
				}
			}
			return out
		},
	}
}

// wearersOf clones, the way the real store does: a resource read here is a
// copy, and a transition's uncommitted changes are invisible in it — the exact
// staleness the fresh discipline exists for. A bench handing out live pointers
// would prove the discipline by accident, on an instrument that cannot see its
// absence.
func (b *groupSyncBench) wearersOf(groupID string) []*testResource {
	var out []*testResource
	for id, worn := range b.wearing {
		for _, w := range worn {
			if w == groupID {
				out = append(out, b.resources[id].Clone())
			}
		}
	}
	return out
}

func (b *groupSyncBench) group(id, references string) *testResource {
	res := &testResource{ID: id, Attrs: map[string]any{}}
	b.resources[id] = res
	b.references[id] = references
	return res
}

func (b *groupSyncBench) machine(id, address string, worn ...string) *testResource {
	res := &testResource{ID: id, Attrs: map[string]any{}, Runtime: map[string]string{}}
	if address != "" {
		res.Attrs["address"] = address
		res.Runtime["machine"] = "feint-bench-" + id
	}
	b.resources[id] = res
	b.wearing[id] = worn
	return res
}

func ruleSources(spec FirewallSpec) []string {
	var out []string
	for _, rule := range spec.Rules {
		if rule.Source != "" {
			out = append(out, rule.Source)
		}
	}
	return out
}

func ensuredSpec(t *testing.T, rec *Recorder, name string) FirewallSpec {
	t.Helper()
	var found *FirewallSpec
	for _, e := range rec.Events() {
		if e.Kind == "EnsureFirewall" && e.Resource == name {
			spec := e.Args.(FirewallSpec)
			found = &spec // the last write is what the runtime holds
		}
	}
	if found == nil {
		t.Fatalf("the runtime never received the rule set %s", name)
	}
	return *found
}

// TestAFreshResourceWinsOverItsStaleStoreCopy is the transposition of
// internal/providers/outscale's
// TestAMemberSourcedRuleExpandsToTheMembersAddresses to the shared
// skeleton: a re-expansion triggered by a boot runs before that boot's own
// commit, so the store's copy of the booting resource carries neither its
// address nor its machine name yet. The skeleton must hand the transition's
// own copy to the pack's translation AND substitute it in the wearer replay —
// reading the store alone silently missed the very machine that booted, in the
// one pack of three that had guarded against it.
func TestAFreshResourceWinsOverItsStaleStoreCopy(t *testing.T) {
	b := newGroupSyncBench()
	web := b.group("web", "")
	_ = web
	data := b.group("data", "web")
	// The store's copy of the web machine: no address, no runtime name — the
	// state before the transition commits.
	b.machine("m-web", "", "web")

	// The transition's own copy: booted, addressed, named.
	fresh := &testResource{
		ID:      "m-web",
		Attrs:   map[string]any{"address": "10.42.0.9"},
		Runtime: map[string]string{"machine": "feint-bench-m-web"},
	}

	b.sync().SyncGroup(context.Background(), data, fresh)

	spec := ensuredSpec(t, b.rec, FirewallName("bench", "data"))
	sources := ruleSources(spec)
	if len(sources) != 1 || sources[0] != "10.42.0.9/32" {
		t.Fatalf("the member rule expanded to %v, want the fresh copy's 10.42.0.9/32: the store's stale copy won", sources)
	}
}

// TestTheWearerReplaySubstitutesTheFreshCopy is the second half of the fresh
// discipline: the wearer replay must act on the transition's copy of the
// booting resource, whose Runtime alone names the machine — the store's copy
// would apply the sets to nothing.
func TestTheWearerReplaySubstitutesTheFreshCopy(t *testing.T) {
	b := newGroupSyncBench()
	group := b.group("g", "")
	b.machine("m", "", "g") // store copy: never booted, no machine name

	fresh := &testResource{
		ID:      "m",
		Attrs:   map[string]any{"address": "10.42.0.7"},
		Runtime: map[string]string{"machine": "feint-bench-m"},
	}
	b.sync().SyncGroup(context.Background(), group, fresh)

	applied := false
	for _, e := range b.rec.Events() {
		if e.Kind == "ApplyFirewall" && e.Resource == "feint-bench-m" {
			applied = true
		}
	}
	if !applied {
		t.Fatalf("the wearer replay never reached the booting machine: the store's machineless copy was replayed instead; events: %v", b.rec.Sequence())
	}
}

// TestTheWearerReplayKeepsTheFreshExpansion guards the overwrite the wearer
// replay can commit: after a referrer's set is written with the booting
// resource's fresh copy, replaying that referrer's wearers re-writes the same
// set — and re-translating it with each wearer's own store copy would erase
// the fresh expansion a moment after delivering it. One fresh per pass,
// threaded into every translation of the pass. Outscale's copy dodged this by
// skipping machineless wearers, which hid the overwrite without removing it —
// this test holds the property for a wearer with a machine, where no skip can
// save it.
func TestTheWearerReplayKeepsTheFreshExpansion(t *testing.T) {
	b := newGroupSyncBench()
	b.group("web", "")
	data := b.group("data", "web")
	// The data machine runs, committed long ago: the store copy is complete,
	// so nothing skips it in the wearer replay.
	b.machine("m-data", "10.42.0.10", "data")
	// The web machine's store copy: mid-boot, address not committed yet.
	b.machine("m-web", "", "web")

	fresh := &testResource{
		ID:      "m-web",
		Attrs:   map[string]any{"address": "10.42.0.9"},
		Runtime: map[string]string{"machine": "feint-bench-m-web"},
	}
	b.sync().SyncGroup(context.Background(), data, fresh)

	spec := ensuredSpec(t, b.rec, FirewallName("bench", "data"))
	sources := ruleSources(spec)
	if len(sources) != 1 || sources[0] != "10.42.0.9/32" {
		t.Fatalf("the wearer replay erased the fresh expansion: the set the runtime holds expanded to %v, want 10.42.0.9/32", sources)
	}
}

// TestAfterBootAppliesTheMachineBeforeReexpandingItsReferrers holds the order
// of the two halves: the machine's own sets attach first, then the groups
// naming its groups re-expand. The re-expansion re-applies wearers, so run
// first it would attach a set the first half then rewrites — the order is the
// property, and it lived in per-pack comments before this test.
func TestAfterBootAppliesTheMachineBeforeReexpandingItsReferrers(t *testing.T) {
	b := newGroupSyncBench()
	b.group("web", "")
	b.group("data", "web")
	webVM := b.machine("m-web", "10.42.0.9", "web")
	b.machine("m-data", "10.42.0.10", "data")

	b.sync().AfterBoot(context.Background(), webVM)

	seq := b.rec.Sequence()
	ownApply := -1
	referrerSync := -1
	for i, e := range b.rec.Events() {
		if e.Kind == "ApplyFirewall" && e.Resource == "feint-bench-m-web" && ownApply == -1 {
			ownApply = i
		}
		if e.Kind == "EnsureFirewall" && e.Resource == FirewallName("bench", "data") && referrerSync == -1 {
			referrerSync = i
		}
	}
	if ownApply == -1 || referrerSync == -1 {
		t.Fatalf("AfterBoot skipped one of its two halves; sequence: %v", seq)
	}
	if ownApply > referrerSync {
		t.Fatalf("the referrers re-expanded before the machine's own sets attached; sequence: %v", seq)
	}
	spec := ensuredSpec(t, b.rec, FirewallName("bench", "data"))
	sources := ruleSources(spec)
	if len(sources) != 1 || sources[0] != "10.42.0.9/32" {
		t.Fatalf("the re-expansion never learned the booted machine's address: %v", sources)
	}
}

// TestForeignRejectsRideTheRuleSetOnlyWhereNetworksAreBornJoined: the
// bridge-mode defence is a field, applied by the skeleton exactly where it
// means something. Under native isolation the blocks name subnets the machine
// cannot reach anyway, and writing them would change every measured OVN rule
// set for nothing.
func TestForeignRejectsRideTheRuleSetOnlyWhereNetworksAreBornJoined(t *testing.T) {
	for _, joined := range []bool{true, false} {
		b := newGroupSyncBench()
		b.rec.Joined = joined
		group := b.group("g", "")
		b.machine("m", "10.42.0.5", "g")

		s := b.sync()
		s.ForeignBlocks = func(*testResource) []string { return []string{"10.99.0.0/24"} }
		s.SyncGroup(context.Background(), group, nil)

		spec := ensuredSpec(t, b.rec, FirewallName("bench", "g"))
		rejected := false
		for _, rule := range spec.Rules {
			if rule.Action == "reject" {
				rejected = true
			}
		}
		if joined && !rejected {
			t.Fatalf("networks born joined and no reject in the set: the bridge-mode defence was lost in the move")
		}
		if !joined && rejected {
			t.Fatalf("native isolation and a reject in the set: dead weight the packs never wrote")
		}
		if joined && spec.Rules[0].Action != "reject" {
			t.Fatalf("the rejects must come first, as the packs measured them; rules: %+v", spec.Rules)
		}
	}
}

// refusingFirewaller wraps the recorder and refuses to write one named rule
// set, for the conservative-application control below.
type refusingFirewaller struct {
	*Recorder
	refuse string
}

func (r refusingFirewaller) EnsureFirewall(ctx context.Context, spec FirewallSpec) error {
	if spec.Name == r.refuse {
		return errors.New("refused on purpose")
	}
	return r.Recorder.EnsureFirewall(ctx, spec)
}

// TestARefusedRuleSetDoesNotShrinkTheAttachmentList: when the runtime refuses
// one of a machine's sets, nothing is applied at all. Attaching the remainder
// would silently drop a deny-carrying set from the machine's interfaces —
// opening traffic the API describes as closed, which is the one direction an
// emulator must never err in.
func TestARefusedRuleSetDoesNotShrinkTheAttachmentList(t *testing.T) {
	b := newGroupSyncBench()
	b.group("open", "")
	b.group("closed", "")
	vm := b.machine("m", "10.42.0.5", "open", "closed")

	s := b.sync()
	s.Binding.Driver = refusingFirewaller{Recorder: b.rec, refuse: FirewallName("bench", "closed")}
	s.ApplyMachine(context.Background(), vm)

	for _, e := range b.rec.Events() {
		if e.Kind == "ApplyFirewall" {
			t.Fatalf("the machine was attached to a shrunken list after a set was refused: %v", e.Args)
		}
	}
}

// TestASetIsWrittenEvenWithoutAMachine: the rule set lives on the runtime for
// every wearer at once, and the resource may be the very reason its
// translation changed — a NIC created on a stopped server changes the foreign
// blocks its group must reject. So the write half runs without a machine; the
// attach half, which needs one, waits for it.
func TestASetIsWrittenEvenWithoutAMachine(t *testing.T) {
	b := newGroupSyncBench()
	b.group("g", "")
	stopped := b.machine("m", "", "g")

	b.sync().ApplyMachine(context.Background(), stopped)

	seq := b.rec.Sequence()
	if len(seq) != 1 || seq[0] != "EnsureFirewall" {
		t.Fatalf("want the set written and nothing attached, got %v", seq)
	}
}
