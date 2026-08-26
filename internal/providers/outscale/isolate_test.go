package outscale

import (
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"

	"github.com/stephrobert/feint/internal/core/resource"
)

// What counts as foreign is the only part of isolation that belongs to a pack,
// and for Outscale it is one sentence: two Subnets of one Net reach each other,
// everything else does not (#201).
//
// This pack had no isolation wiring at all, and the gap stayed invisible while
// the fallback bridge left machines without a route to each other anyway. Once a
// machine carried only its own subnet address (#202), two Nets reached each
// other in ICMP and in TCP on the same emulator where Scaleway's did not — the
// difference being that Scaleway called this layer and Outscale never did.
//
// Both halves, because a rule that called everything foreign would isolate two
// subnets of one Net, which no client asked for and which the real cloud routes.
func TestSubnetsOfOtherNetsAreForeign(t *testing.T) {
	subnet := func(id, net string) *resource.Resource {
		return &resource.Resource{
			ID:    id,
			Kind:  kindSubnet,
			Attrs: map[string]any{"NetId": net},
		}
	}
	p := &Pack{}

	a := subnet("subnet-1", "vpc-a")
	b := subnet("subnet-2", "vpc-a")
	c := subnet("subnet-3", "vpc-b")

	if !p.reachableFrom(a, b, nil) {
		t.Error("two subnets of one Net were treated as foreign; the real cloud routes between them")
	}
	if p.reachableFrom(a, c, nil) {
		t.Error("a subnet of another Net was treated as reachable, which is the leak this closes")
	}
	// A subnet with no Net at all must not be reachable by accident: an absent
	// value is not a match, and treating two empties as equal would join every
	// malformed record to every other.
	empty := subnet("subnet-4", "")
	if p.reachableFrom(a, empty, nil) {
		t.Error("a subnet naming no Net was treated as reachable from one that does")
	}

	// An active peering is the one thing that widens the answer (#508), and it
	// widens it in both directions — activePeeringReaches records both — while
	// leaving every unpeered pair exactly as foreign as before.
	peered := netReachability{"vpc-a": {"vpc-b": true}, "vpc-b": {"vpc-a": true}}
	if !p.reachableFrom(a, c, peered) || !p.reachableFrom(c, a, peered) {
		t.Error("an active peering between the two Nets did not make their subnets reachable")
	}
	d := subnet("subnet-5", "vpc-c")
	if p.reachableFrom(a, d, peered) {
		t.Error("a peering between vpc-a and vpc-b leaked reachability towards vpc-c")
	}
}

// A Vm created with no Subnet is in the public Cloud, and Outscale gives it a
// private address there: the schema declares PrivateIp and a real one carries a
// value.
//
// The conformance suite caught this the moment #202 removed the fallback
// network outright — "the machine is running and the API publishes no
// PrivateIp". The fallback was not wrong in itself; it was wrong where the
// address it handed out was published by no API, which was the Scaleway case
// and not this one. So this pack asks for the emulator's own network by name,
// and publishes what it receives.
func TestAVmOutsideANetStillCarriesAPrivateAddress(t *testing.T) {
	p := &Pack{}

	outside := &resource.Resource{
		ID:    "i-1",
		Kind:  kindVM,
		Attrs: map[string]any{},
	}
	got := p.attachmentOf(outside)
	if len(got) != 1 || got[0].Network != machine.DefaultMachineNetwork {
		t.Errorf("a Vm outside a Net asked for %v, want the emulator's own network", got)
	}

	// And a Vm that names a Subnet must never take it: that one belongs to its
	// Net, and joining the shared network would put two Nets on one segment.
	// The case chosen carries no address yet, so the decision is reached before
	// the store is consulted — what is under test is the branch, not a lookup.
	inside := &resource.Resource{
		ID:    "i-2",
		Kind:  kindVM,
		Attrs: map[string]any{"SubnetId": "subnet-1"},
	}
	if got := p.attachmentOf(inside); len(got) != 0 {
		t.Errorf("a Vm naming a Subnet asked for %v, want nothing shared", got)
	}
}
