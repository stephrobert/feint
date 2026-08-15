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
// Called after any change to the set of subnets, because a new one changes what
// its neighbours must keep out. Reconciled over all of them rather than patched
// for the one that moved: a patch has to be right about what changed, and this
// only has to be right about what is.
//
// Failure is logged, never fatal. The control plane must keep answering when no
// runtime is configured, which is the default and what CI uses.
func (p *Pack) isolateNetworks(ctx context.Context) {
	all := p.env.Store.List(kindSubnet, resource.Tenant{Provider: Name})

	peerer, native := p.env.Machines.(machine.Peerer)
	if native && peerer.NativeIsolation() {
		for _, subnet := range all {
			name := subnet.Runtime[runtimeNetworkKey]
			if name == "" {
				continue
			}
			peers := make([]string, 0, len(all))
			for _, other := range all {
				if other.ID == subnet.ID || other.Runtime[runtimeNetworkKey] == "" {
					continue
				}
				if p.reachableFrom(subnet, other) {
					peers = append(peers, other.Runtime[runtimeNetworkKey])
				}
			}
			if err := peerer.PeerNetworks(ctx, name, peers); err != nil {
				p.logger().Error("could not peer the subnet's network",
					"subnet", subnet.ID, "network", name, "error", err)
			}
		}
		return
	}

	isolator, ok := p.env.Machines.(machine.Isolator)
	if !ok {
		return
	}
	for _, subnet := range all {
		name := subnet.Runtime[runtimeNetworkKey]
		if name == "" {
			continue
		}
		foreign := make([]string, 0, len(all))
		for _, other := range all {
			if other.ID == subnet.ID || p.reachableFrom(subnet, other) {
				continue
			}
			if block := stringOf(other.Attrs["IpRange"]); block != "" {
				foreign = append(foreign, block)
			}
		}
		if err := isolator.IsolateNetwork(ctx, name, foreign); err != nil {
			p.logger().Error("could not isolate the subnet's network",
				"subnet", subnet.ID, "network", name, "error", err)
		}
	}
}
