package outscale

import (
	"context"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Two Nets do not reach each other; two Subnets of one Net do (#201).
//
// This pack had no isolation wiring at all, and the gap was invisible until the
// rule that a machine carries one address removed the fallback bridge that used
// to leave machines with no route to each other anyway. Measured on one
// emulator, same runtime, same minute:
//
//	Scaleway  a server of another VPC   unreachable   (this call, via Scaleway)
//	Outscale  a machine of another Net   REACHES       (nothing called it here)
//
// The difference was the wiring, not the mechanism: both providers back their
// subnets with networks of the same driver, and the driver applies the rule set.
// A control that lives in two packs out of three is a control the third forgets,
// which is exactly what happened.
//
// What is foreign is an Outscale question, and it is the only part that belongs
// here: two Subnets of one Net are routed to each other upstream, and everything
// else — another Net, another account — is not.
//
// TestSubnetsOfOtherNetsAreForeign fails without this.

// reachableFrom reports whether one subnet may reach another: same Net, and
// nothing else. Net peerings widen this, and they do it through PeerNetworks
// rather than here, so a peering that is only pending grants nothing.
func (p *Pack) reachableFrom(subnet, other *resource.Resource) bool {
	return stringOf(subnet.Attrs["NetId"]) == stringOf(other.Attrs["NetId"])
}

// isolateNetworks reconciles what every subnet's backing network may reach.
//
// Called after any change to the set of subnets, because a new one changes
// what its neighbours must keep out. The reconciliation is
// machine.ReconcileIsolation, shared with the two other packs — this pack is
// the one that had no isolation wiring at all until #201 — and only the
// Outscale question above stays here, as the predicate.
//
// Coalesced, because the pass is O(N) over every subnet and a terraform apply
// issues its creates concurrently: fifteen subnets each paying a full pass,
// serialised on the runtime's locks, is the straight line of #473 — a flat
// ~6 s per create, cut at the emulator's write deadline from the tenth on.
// Any pass that starts after a change covers it, so the coalescer runs at
// most the pass in flight plus one for a whole burst, and every caller still
// returns only once a pass that read its own change has completed. The pass
// re-reads the store when it runs (isolationPass), which is what makes
// covering a burst sound. Detached from the request's cancellation because
// one pass serves many requests, and the first client hanging up must not
// abort the reconciliation the others are waiting on.
// TestConcurrentSubnetCreatesShareTheirIsolationPasses fails without this.
func (p *Pack) isolateNetworks(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	p.isolation.Run(func() { p.isolationPass(ctx) })
}

// isolationPass is one full reconciliation, reading the store at the moment it
// runs. Only the Coalescer calls it.
func (p *Pack) isolationPass(ctx context.Context) {
	all := p.env.Store.List(kindSubnet, resource.Tenant{Provider: Name})
	members := make([]machine.IsolationMember, len(all))
	for i, subnet := range all {
		members[i] = machine.IsolationMember{
			ID:      subnet.ID,
			Network: subnet.Runtime[runtimeNetworkKey],
			Block:   stringOf(subnet.Attrs["IpRange"]),
		}
	}
	machine.ReconcileIsolation(ctx, p.env.Machines, p.logger(), "subnet",
		members, func(from, to int) bool { return p.reachableFrom(all[from], all[to]) })
}
