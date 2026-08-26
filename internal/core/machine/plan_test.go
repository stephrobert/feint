package machine

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

// The witnesses of #510: the post-boot order — addresses, then memberships,
// the firewall last — and the shared guards around it are asserted here once,
// against the contract recorder, instead of living as three comments in three
// packs.

// reconcilerBench extends the group-sync bench with a plan, so the whole
// choreography runs over one recorder.
func reconcilerBench(b *groupSyncBench, plan Plan) Reconciler {
	return Reconciler{
		Groups:      b.sync(),
		PlanOf:      func(*testResource) Plan { return plan },
		PublicBlock: netip.MustParsePrefix("203.0.113.0/24"),
	}
}

// TestTheReplayRoutesThenJoinsThenAppliesTheFirewall is the order property
// itself, the sentence three packs kept in comments: route the promised
// addresses, join the private networks, resync the firewall, in that order —
// so the expansion sees every address and every interface. Red without the
// Reconciler for any pack whose comment was the only thing holding it.
func TestTheReplayRoutesThenJoinsThenAppliesTheFirewall(t *testing.T) {
	b := newGroupSyncBench()
	b.group("g", "")
	vm := b.machine("m", "", "g")

	r := reconcilerBench(b, Plan{
		Publics:     []string{"203.0.113.9"},
		Memberships: []Attachment{{Network: "fnt-bench-net"}},
	})
	if !r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
		t.Fatalf("the boot did not start; sequence: %v", b.rec.Sequence())
	}

	position := map[string]int{}
	for i, kind := range b.rec.Sequence() {
		if _, seen := position[kind]; !seen {
			position[kind] = i
		}
	}
	for _, kind := range []string{"Start", "RouteAddress", "Attach", "EnsureFirewall", "ApplyFirewall"} {
		if _, seen := position[kind]; !seen {
			t.Fatalf("the replay never emitted %s; sequence: %v", kind, b.rec.Sequence())
		}
	}
	ordered := position["Start"] < position["RouteAddress"] &&
		position["RouteAddress"] < position["Attach"] &&
		position["Attach"] < position["EnsureFirewall"] &&
		position["EnsureFirewall"] < position["ApplyFirewall"]
	if !ordered {
		t.Fatalf("the order is not route, then attach, then firewall: %v", b.rec.Sequence())
	}
}

// TestAPoisonedStoredAddressIsNeverRoutedByTheLayer holds the authorisation
// half once for the three packs: a stored address is untrusted input — a
// restored snapshot carries it verbatim — and routing an arbitrary value would
// send the host's traffic for that address into a container. The accepting
// half is asserted too, because a guard that refuses everything passes every
// attack test and breaks the product.
func TestAPoisonedStoredAddressIsNeverRoutedByTheLayer(t *testing.T) {
	b := newGroupSyncBench()
	vm := b.machine("m", "10.0.0.5")
	vm.Runtime["machine"] = "feint-bench-m"
	r := reconcilerBench(b, Plan{})

	r.Route(context.Background(), vm, "192.168.1.10") // the operator's LAN
	r.Unroute(context.Background(), "feint-bench-m", "192.168.1.10")
	for _, e := range b.rec.Events() {
		if e.Kind == "RouteAddress" || e.Kind == "UnrouteAddress" {
			t.Fatalf("an address outside the emulated block reached the driver: %v", e)
		}
	}

	r.Route(context.Background(), vm, "203.0.113.9")
	routed := false
	for _, e := range b.rec.Events() {
		if e.Kind == "RouteAddress" && e.Resource == "203.0.113.9" {
			routed = true
		}
	}
	if !routed {
		t.Fatalf("the guard also refuses the emulated block, so no address can ever be routed; events: %v", b.rec.Sequence())
	}
}

// TestJoinRunsTheFirewallAfterTheAttach is the hot half of the order: a NIC
// attached to a running machine creates an interface the machine's rule sets
// must cover, so the resync runs after the attach — and still runs when the
// attach was refused, because the store moved even when the runtime did not.
func TestJoinRunsTheFirewallAfterTheAttach(t *testing.T) {
	b := newGroupSyncBench()
	b.group("g", "")
	vm := b.machine("m", "10.0.0.5", "g")
	r := reconcilerBench(b, Plan{})

	if err := r.Join(context.Background(), vm, Attachment{Network: "fnt-bench-net"}); err != nil {
		t.Fatalf("the attach failed on a recorder that refuses nothing: %v", err)
	}
	seq := b.rec.Sequence()
	attach, firewall := -1, -1
	for i, kind := range seq {
		if kind == "Attach" && attach == -1 {
			attach = i
		}
		if kind == "ApplyFirewall" && firewall == -1 {
			firewall = i
		}
	}
	if attach == -1 || firewall == -1 || attach > firewall {
		t.Fatalf("want the attach then the firewall, got %v", seq)
	}
}

// refusingAttacher wraps the recorder and refuses every attach, for the
// refused-attach half of the Join contract.
type refusingAttacher struct{ *Recorder }

func (r refusingAttacher) Attach(context.Context, string, Attachment) error {
	return errors.New("refused on purpose")
}

// TestARefusedAttachStillResyncsTheFirewall: the error comes back to the pack
// — one surfaces it on its NIC's state — and the firewall step runs anyway,
// exactly what the pack that surfaces the error also does today.
func TestARefusedAttachStillResyncsTheFirewall(t *testing.T) {
	b := newGroupSyncBench()
	b.group("g", "")
	vm := b.machine("m", "10.0.0.5", "g")
	r := reconcilerBench(b, Plan{})
	r.Groups.Binding.Driver = refusingAttacher{b.rec}

	if err := r.Join(context.Background(), vm, Attachment{Network: "fnt-bench-net"}); err == nil {
		t.Fatal("the refusal never reached the caller, so no pack can surface it on its resource")
	}
	applied := false
	for _, kind := range b.rec.Sequence() {
		if kind == "ApplyFirewall" {
			applied = true
		}
	}
	if !applied {
		t.Fatalf("a refused attach skipped the resync, and the machine's sets no longer match the store: %v", b.rec.Sequence())
	}
}

// TestEnsureBackingNetworkRecordsOnlyWhatTheDriverAccepted settles the write
// ordering the three packs disagreed on: the Runtime key is written after the
// driver accepted the network — the one ordering that never records a network
// the runtime refused, which the delete path would then try to remove.
func TestEnsureBackingNetworkRecordsOnlyWhatTheDriverAccepted(t *testing.T) {
	b := newGroupSyncBench()
	res := b.machine("net-1", "")
	binding := b.binding()

	err := binding.EnsureBackingNetwork(context.Background(), res, BackingNetwork{
		Key:     "network",
		CIDR:    netip.MustParsePrefix("10.61.0.0/24"),
		Gateway: true,
		NAT:     true,
		Marker:  "feint.bench-net",
	})
	if err != nil {
		t.Fatalf("the recorder refused a network: %v", err)
	}
	if res.Runtime["network"] == "" {
		t.Fatal("the accepted network was not recorded")
	}
	var spec NetworkSpec
	found := false
	for _, e := range b.rec.Events() {
		if e.Kind == "EnsureNetwork" {
			spec = e.Args.(NetworkSpec)
			found = true
		}
	}
	if !found {
		t.Fatalf("no EnsureNetwork reached the driver: %v", b.rec.Sequence())
	}
	if spec.Labels[LabelKey] != "bench" || spec.Labels["feint.bench-net"] != res.ID {
		t.Fatalf("the labels lost the provider or the marker: %v", spec.Labels)
	}
	if spec.Gateway != "10.61.0.1" {
		t.Fatalf("gateway %q, want the block's first host address", spec.Gateway)
	}

	refused := b.machine("net-2", "")
	binding.Driver = refusingNetworker{b.rec}
	if err := binding.EnsureBackingNetwork(context.Background(), refused, BackingNetwork{
		Key: "network", CIDR: netip.MustParsePrefix("10.62.0.0/24"),
	}); err == nil {
		t.Fatal("the refusal was swallowed")
	}
	if refused.Runtime["network"] != "" {
		t.Fatal("a network the driver refused was recorded anyway — the ordering two packs had")
	}
}

// refusingNetworker wraps the recorder and refuses every network.
type refusingNetworker struct{ *Recorder }

func (r refusingNetworker) EnsureNetwork(context.Context, NetworkSpec) error {
	return errors.New("refused on purpose")
}

// TestRemoveBackingNetworkForgetsTheNameOnSuccessOnly: the Runtime entry
// survives a refused removal, so the pack can refuse its delete instead of
// reporting gone something that still holds its block.
func TestRemoveBackingNetworkForgetsTheNameOnSuccessOnly(t *testing.T) {
	b := newGroupSyncBench()
	res := b.machine("net-1", "")
	res.Runtime["network"] = "fnt-bench-net"
	binding := b.binding()

	if err := binding.RemoveBackingNetwork(context.Background(), res, "network"); err != nil {
		t.Fatalf("the recorder refused a removal: %v", err)
	}
	if res.Runtime["network"] != "" {
		t.Fatal("the removed network is still recorded")
	}

	held := b.machine("net-2", "")
	held.Runtime["network"] = "fnt-bench-kept"
	binding.Driver = refusingRemover{b.rec}
	if err := binding.RemoveBackingNetwork(context.Background(), held, "network"); err == nil {
		t.Fatal("the refusal was swallowed")
	}
	if held.Runtime["network"] != "fnt-bench-kept" {
		t.Fatal("a network the runtime kept was forgotten, and nothing can remove it any more")
	}
}

// refusingRemover wraps the recorder and refuses every network removal.
type refusingRemover struct{ *Recorder }

func (r refusingRemover) RemoveNetwork(context.Context, string) error {
	return errors.New("refused on purpose")
}
