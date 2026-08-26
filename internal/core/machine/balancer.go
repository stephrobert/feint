package machine

import (
	"context"
	"errors"
)

// A load balancer that distributes packets, within the one bound that was
// measured (#315).
//
// The LBU and lb/v1 families are served as configuration everywhere: created,
// read back, destroyed in the right order. Nothing distributed a packet, and the
// documentation said so. This is the half that can now be more than a record,
// and the half that cannot, told apart by measurement rather than by hope.
//
// What holds. On 2026-08-20, on an OVN network of this emulator's own making,
// an `incus network load-balancer` whose listen address is an address of the
// network's own block distributed real connections across two backends and kept
// doing so: 6/6 answered at t0, at t+60s and at t+180s, spread over both
// machines every time. That is a balancer an emulated *internal* load balancer
// can be, because an internal balancer's address is exactly that — one address
// of the subnet it sits in.
//
// What does not. #315 measured the other address on 2026-08-19: a VIP outside
// the network, delegated through the uplink's routes, answered 6/6 at t0 and
// t+60s and then 0/6 for ever from t+180s. The runtime announces such an address
// with a burst of gratuitous ARPs at creation time and never again, which is the
// same defect incus_ovn.go records for network forwards. An internet-facing
// balancer's public address is that kind of address, so it stays what
// docs/limits.md describes: a TEST-NET-3 address routed nowhere on purpose.
//
// The two are not the same mechanism, and reading the second measurement as a
// verdict on the first is the mistake this comment exists to prevent: an
// in-block address needs no announcement at all. The emulator already delegates
// every emulated subnet to the uplink (delegateQueuedRoutes), so the uplink routes the
// whole block to the OVN router, and the router answers for the VIP as it does
// for any other address of its own switch.
//
// Hence Capabilities.Balancing, declared by the OVN mode alone, and hence
// EnsureBalancer refusing a listen address outside the network's own block
// rather than configuring one that would go dark in minutes.
//
// The backends live under the same bound, and that half was measured on
// 2026-08-25 for #457, because nothing had asked the runtime the question. On
// Incus 7.2 with OVN, on two networks of this emulator's own making:
//
//   - a backend outside the balancer's network is refused outright, "Target
//     address is not within the network subnet for backend \"b1-80\"";
//   - peering the two networks — both halves CREATED — does not relax it: the
//     same refusal, word for word;
//   - putting the balancer on the *backends'* network instead, which is the
//     placement that would serve the ordinary two-tier shape, is refused on the
//     other end: "Load balancer listen address \"10.181.0.5/32\" overlaps with
//     another network or NIC";
//   - and there is no external-route key on an OVN network to declare the
//     address with — "Invalid option for network ... option \"ipv4.routes\"" —
//     only NICs carry ipv4.routes.external, and a VIP has no NIC.
//
// The one placement the runtime does accept is a listen address belonging to no
// emulated block at all, delegated through the uplink. That is exactly the
// address class of the paragraph above, the one that goes dark in minutes, and
// it is not the address the API published either.
//
// So a load balancer in front of machines on another subnet is not served here,
// it is refused by name, and docs/limits.md carries the measurement. Refusing is
// what makes the refusal reach somebody: the runtime's own refusal arrives in
// the middle of the write, leaving the balancer standing with the backends it
// already had.

// ErrBalancerNotDistributed marks a balancer whose shape this runtime does not
// distribute — a stated limit, not a failure.
//
// It exists so a caller can tell the two apart without matching prose. A pack
// hands a balancer to the runtime after every change to it, and a shape the
// runtime will never take is not an incident to be reported at ERROR on every
// register: it is a limit, said once, next to what the API still describes. A
// runtime that broke is the other thing, and it stays an error.
var ErrBalancerNotDistributed = errors.New("this runtime does not distribute a balancer of this shape")

// BalancerListener is one port a balancer answers on, and the port it hands the
// connection to on each backend.
type BalancerListener struct {
	// Protocol is "tcp" or "udp". A runtime that distributes neither is not a
	// Balancer.
	Protocol string
	// Listen is the port the balancer's address answers on.
	Listen int
	// Backend is the port each machine is given the connection on.
	Backend int
}

// BalancerSpec is a named balancer on one network, described whole.
//
// Whole, and not patched member by member, for the reason EnsureFirewall states
// for a rule set: replacing is what makes a backend removed upstream actually
// stop receiving connections. A register followed by an unregister has to leave
// the runtime holding exactly what the API says it holds.
type BalancerSpec struct {
	// Name identifies the emulated balancer, for the description the runtime
	// stores and an ownership check reads back.
	Name string
	// Network is the runtime network the balancer lives on. It must be one the
	// emulator created: a balancer written onto an operator's own network would
	// survive every sweep.
	Network string
	// Listen is the address the balancer answers on. It must belong to
	// Network's own block — see the package comment: an address outside it is
	// announced once and goes dark.
	Listen string
	// Listeners are the ports, at least one.
	Listeners []BalancerListener
	// Targets are the addresses of the machines behind it. They must belong to
	// Network's own block too, for the reason the package comment measures: the
	// runtime refuses a backend outside it, peered or not.
	//
	// An empty list is valid and means exactly what it says: a balancer with no
	// backend, which is what a stack has between creating one and registering
	// its first machine.
	Targets []string
}

// Balancer is the optional half of a Driver that can distribute packets.
//
// Optional for the same reason Firewaller is: absence has to be a compile-time
// fact and a declared capability, never a silent no-op. A pack whose driver is
// not a Balancer serves the load-balancer family exactly as before — the
// configuration round-trips, nothing forwards — and says so.
type Balancer interface {
	// EnsureBalancer creates or replaces the balancer as a whole. It must
	// succeed when called again with the same specification.
	EnsureBalancer(ctx context.Context, spec BalancerSpec) error
	// RemoveBalancer deletes it. It must succeed when nothing is there, and it
	// must refuse a balancer the emulator did not create.
	RemoveBalancer(ctx context.Context, network, listen string) error
}
