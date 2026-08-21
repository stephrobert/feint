package outscale

import (
	"context"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Where a load balancer stops being a record (#315).
//
// The LBU family has always round-tripped its configuration and forwarded
// nothing, and docs/limits.md said so plainly. One half of that can now be
// true: a balancer's own private address — the one the API publishes as
// PrivateIp, an address of the Subnet it sits in — is handed to the runtime,
// which distributes real connections across the registered machines. Measured
// on 2026-08-20: 6/6 connections answered from inside the network at t0, t+60s
// and t+180s, spread over both backends every time.
//
// The other half stays a record, and that is a measurement too, not modesty. An
// internet-facing balancer's public address is a TEST-NET-3 address the runtime
// would have to announce outside the network, and #315 measured what happens
// then: it answers for two minutes and goes dark for ever. So the public face is
// unchanged, and the reason is in internal/core/machine/balancer.go beside the
// figures.
//
// Nothing here is conditional on a mode name. The driver declares
// Capabilities.Balancing, the OVN mode alone sets it, `Verify` clears it on a
// host with no OVN, and a driver that says nothing is read as not balancing —
// which is what makes the degraded path the default rather than the exception.

// balancer returns the runtime's balancing half, or nil when it has none.
//
// Two questions, both asked: does the driver implement the interface, and does
// it *declare* the capability. The second is not redundant — the Incus driver
// implements Balancer in every mode and can only deliver it under OVN, and
// `Verify` clears the claim on a host whose daemon has no northbound
// connection. Asking only the first would drive a bridge-backed run into a
// refusal on every register.
func (p *Pack) balancer() machine.Balancer {
	if p.env.Machines == nil || !machine.CapabilitiesOf(p.env.Machines).Balancing {
		return nil
	}
	b, _ := p.env.Machines.(machine.Balancer)
	return b
}

// syncBalancer hands one balancer's current backend set to the runtime.
//
// Called after every change to the set — the create, a register, an unlink —
// because the runtime must hold what the API says it holds, and a stale backend
// keeps receiving connections the API has already stopped listing.
//
// A failure is logged and never fails the request. That is this repository's
// rule for every runtime effect: an emulator whose control plane breaks when a
// container does is worse than one that records the truth and says what it
// could not do.
func (p *Pack) syncBalancer(ctx context.Context, name string) {
	b := p.balancer()
	if b == nil {
		return
	}
	res, found := p.env.Store.Get(Name, kindLoadBalancer, name)
	if !found {
		return
	}
	network, listen := p.balancerPlacement(res)
	if network == "" || listen == "" {
		// Metadata only: nothing was ever handed to the runtime, so there is
		// nothing to hand it and nothing to take back.
		return
	}
	spec, ok := p.balancerSpecOf(res)
	if !ok {
		// Placed, and carrying no listener any more. Returning here is what the
		// code did before #344, and it was harmless only while the listener set
		// was fixed at create: DeleteLoadBalancerListeners can now empty it, and
		// the provider empties it on the way through every single-listener port
		// change, because it deletes the departing port before creating the
		// arriving one.
		//
		// Leaving the runtime alone at that moment is the exact lie this project
		// exists to refuse — the API answers "no listeners" while the balancer
		// goes on distributing connections on the old port. RemoveBalancer is
		// specified to succeed when nothing is there, so this is safe on a
		// balancer the runtime never received.
		//
		// TestEmptyingTheListenersRemovesTheBalancerFromTheRuntime fails
		// without this.
		if err := b.RemoveBalancer(ctx, network, listen); err != nil {
			p.logger().Error("could not withdraw a load balancer that lost its listeners",
				"load_balancer", res.ID, "listen", listen, "error", err)
		}
		return
	}
	if err := b.EnsureBalancer(ctx, spec); err != nil {
		p.logger().Error("could not hand the load balancer to the runtime",
			"load_balancer", res.ID, "listen", spec.Listen, "error", err)
	}
}

// removeBalancer undoes it, on the way out.
func (p *Pack) removeBalancer(ctx context.Context, res *resource.Resource) {
	b := p.balancer()
	if b == nil {
		return
	}
	network, listen := p.balancerPlacement(res)
	if network == "" || listen == "" {
		return
	}
	if err := b.RemoveBalancer(ctx, network, listen); err != nil {
		p.logger().Error("could not remove the load balancer from the runtime",
			"load_balancer", res.ID, "listen", listen, "error", err)
	}
}

// balancerPlacement is the runtime network a balancer sits on and the address
// it answers on. Both empty when the balancer has no subnet behind it, which is
// the metadata-only case and not a defect.
func (p *Pack) balancerPlacement(res *resource.Resource) (network, listen string) {
	subnets := stringsOf(res.Attrs["Subnets"])
	if len(subnets) == 0 {
		return "", ""
	}
	subnet, found := p.env.Store.Get(Name, kindSubnet, subnets[0])
	if !found {
		return "", ""
	}
	return subnet.Runtime[runtimeNetworkKey], stringOf(res.Attrs["PrivateIp"])
}

// balancerSpecOf turns a stored balancer into what the runtime is asked for.
//
// Two translations, and both are the pack's business rather than the driver's:
//
//   - the listener protocol. An LBU listener speaks HTTP, HTTPS, TCP or SSL;
//     all four ride TCP and none of them is terminated here — nothing decrypts,
//     nothing parses a request — so the honest thing to hand a packet
//     distributor is the transport. A balancer that claimed to terminate TLS
//     because its listener said HTTPS would be exactly the invention this
//     emulator exists to refuse.
//   - the backend address. The private one, never the public: this distributes
//     to machines inside the network, and a public address here is either
//     absent or the fictional block that routes nowhere.
func (p *Pack) balancerSpecOf(res *resource.Resource) (machine.BalancerSpec, bool) {
	network, listen := p.balancerPlacement(res)
	if network == "" || listen == "" {
		return machine.BalancerSpec{}, false
	}

	spec := machine.BalancerSpec{
		Name:    res.ID,
		Network: network,
		Listen:  listen,
	}
	listeners, _ := res.Attrs["Listeners"].([]any)
	for _, raw := range listeners {
		listener, _ := raw.(map[string]any)
		// numOf, because a listener that has crossed a snapshot carries
		// json.Number where the handler stored an int — the same reason
		// stringsOf exists beside it.
		front := int(numOf(listener["LoadBalancerPort"]))
		back := int(numOf(listener["BackendPort"]))
		if front == 0 {
			continue
		}
		spec.Listeners = append(spec.Listeners, machine.BalancerListener{
			Protocol: "tcp",
			Listen:   front,
			Backend:  back,
		})
	}
	if len(spec.Listeners) == 0 {
		return machine.BalancerSpec{}, false
	}
	for _, id := range stringsOf(res.Attrs["BackendVmIds"]) {
		vm, found := p.env.Store.Get(Name, kindVM, id)
		if !found {
			continue
		}
		if address := stringOf(vm.Attrs["PrivateIp"]); address != "" {
			spec.Targets = append(spec.Targets, address)
		}
	}
	return spec, true
}
