package machine

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
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
// So a backend on another subnet cannot be distributed here, and docs/limits.md
// carries the measurement. What that refusal must NOT do is take the whole
// specification down with it, and it did until #483: the ordinary two-tier
// stack registers one backend on the balancer's own subnet and one on another,
// the whole-spec refusal dropped both, and the host held a balancer with no
// backend and no port while the API described two healthy ones — apply exit 0,
// zero ERROR lines, found only by reading the host. The decision, stated
// rather than implied: **the runtime distributes to the backends it can take,
// withholds the ones it cannot, and reports both lists** (BalancerDelivery).
// Partial distribution that names what it withheld beats none, because the
// host then carries a witness of the distribution that does work, and the
// withheld half is on the record — in the delivery, in the pack's WARN, and in
// the resource's Runtime — instead of looking exactly like a balancer that was
// never handed over. What withholding still must never become is the runtime's
// own mid-write failure: an out-of-block backend is held back *before* the
// write, so the write that reaches the daemon is one it accepts whole.

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
	// Targets are the addresses of the machines behind it. Only the ones
	// belonging to Network's own block are distributed, for the reason the
	// package comment measures: the runtime refuses a backend outside it,
	// peered or not. The others are withheld before the write and named in the
	// BalancerDelivery, never handed to the daemon to die mid-write.
	//
	// An empty list is valid and means exactly what it says: a balancer with no
	// backend, which is what a stack has between creating one and registering
	// its first machine.
	Targets []string
}

// BalancerDelivery is what the runtime actually holds after EnsureBalancer —
// the effect, reported next to the intent, so the caller can publish the first
// rather than the second (#483).
//
// It exists because the gap it names was measured: a spec with one backend the
// runtime could take and one it could not was refused whole, the host held a
// balancer distributing to nobody, and the API went on describing both
// backends with nothing but a WARN in between. A caller that hands a balancer
// over owns writing this down where its API's readers can see it.
type BalancerDelivery struct {
	// Distributed is the target addresses the host now distributes to, in the
	// order of the spec.
	Distributed []string
	// Undistributed maps each withheld target address to the short, measured
	// reason it was withheld. Empty when the runtime took the spec whole.
	Undistributed map[string]string
}

// Lines renders the delivery as the two strings RecordBalancerDelivery stores:
// the distributed addresses comma-joined in spec order, and the withheld ones
// with their reasons, sorted so two identical deliveries write two identical
// records — a map order leaking into a stored value is a diff nobody made.
func (d BalancerDelivery) Lines() (distributed, undistributed string) {
	addresses := make([]string, 0, len(d.Undistributed))
	for address := range d.Undistributed {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	parts := make([]string, 0, len(addresses))
	for _, address := range addresses {
		parts = append(parts, address+" ("+d.Undistributed[address]+")")
	}
	return strings.Join(d.Distributed, ","), strings.Join(parts, "; ")
}

// The Runtime keys where a pack records what the runtime holds for a load
// balancer, next to the network and machine names the same map already
// carries. They exist because #483 measured their absence: a host balancer
// distributing to nobody while the API described two healthy backends, with
// one WARN line as the only trace — invisible to every gate, found by a person
// reading the host. The record is the emulator's own account of the effect,
// readable through `/_feint/state`, so a gate can compare the three parties —
// what the API describes, what this record says was delivered, what the host
// actually holds — without asking the emulator to grade itself.
const (
	RuntimeBalancerDistributed   = "balancer-distributed"
	RuntimeBalancerUndistributed = "balancer-undistributed"
)

// RecordBalancerDelivery writes the effect a balancer hand-off produced onto
// the resource that asked for it — the state published is the one the effect
// produced, never the one the intent visited.
//
// Clone-then-Commit, never Put: the hand-off ran outside the store lock, and a
// balancer deleted while the runtime worked must stay deleted. A false return
// from Commit is exactly that case and there is nothing to record on it.
func RecordBalancerDelivery(st *store.Store, res *resource.Resource, now time.Time, distributed, undistributed string) {
	base := res.Clone()
	if res.Runtime == nil {
		res.Runtime = map[string]string{}
	}
	res.Runtime[RuntimeBalancerDistributed] = distributed
	res.Runtime[RuntimeBalancerUndistributed] = undistributed
	st.Commit(base, res, now)
}

// balancer is the optional half of a driver that can distribute packets.
//
// Optional for the same reason the firewalling half is: absence has to be a
// compile-time fact and a declared capability, never a silent no-op. A pack
// whose driver does not balance serves the load-balancer family exactly as
// before — the configuration round-trips, nothing forwards — and says so.
type balancer interface {
	// EnsureBalancer creates or replaces the balancer as a whole. It must
	// succeed when called again with the same specification, and its delivery
	// reports what the host now holds: the targets distributed, and the ones
	// withheld with their reasons. The delivery is meaningful only when the
	// error is nil.
	EnsureBalancer(ctx context.Context, spec BalancerSpec) (BalancerDelivery, error)
	// RemoveBalancer deletes it. It must succeed when nothing is there, and it
	// must refuse a balancer the emulator did not create.
	RemoveBalancer(ctx context.Context, network, listen string) error
}

// balancer is the runtime's balancing half, nil when it has none — the
// assertion the balancing pack wrote for itself, now inside the layer beside
// Reconciler.router and GroupSync.enforcer (#511).
//
// Two questions, both asked, and the second is not redundant: the Incus driver
// implements Balancer in every mode and can only deliver it under OVN, and
// Verify clears the claim on a host whose daemon has no northbound connection.
// Asking only whether the interface is implemented would drive a
// bridge-backed run into a refusal on every register.
func (b Binding) balancer() balancer {
	if b.driver == nil || !CapabilitiesOf(b.driver).Balancing {
		return nil
	}
	bal, _ := b.driver.(balancer)
	return bal
}

// Balances reports whether this runtime both implements and declares
// balancing, so a pack can leave its load-balancer family a record — which is
// what it was before #315 — instead of asking the host for something it has
// said it cannot do.
func (b Binding) Balances() bool { return b.balancer() != nil }

// EnsureBalancer hands the whole balancer to the runtime and reports what it
// actually holds. See BalancerSpec for why the spec is whole rather than
// patched, and BalancerDelivery for why the effect comes back beside it.
//
// A runtime that does not balance answers ErrBalancerNotDistributed rather
// than an empty delivery and a nil error: an empty delivery reads exactly like
// a balancer with no backend, which is a legitimate state a stack passes
// through, so the caller would record "nothing distributed" with no reason
// beside it. Three outcomes, never two.
func (b Binding) EnsureBalancer(ctx context.Context, spec BalancerSpec) (BalancerDelivery, error) {
	bal := b.balancer()
	if bal == nil {
		return BalancerDelivery{}, ErrBalancerNotDistributed
	}
	return bal.EnsureBalancer(ctx, spec)
}

// RemoveBalancer withdraws it. It succeeds when nothing is there, which is the
// normal path: a delete runs after an emptied listener set that may never have
// reached the host. A runtime that does not balance holds nothing to withdraw,
// so this is a no-op rather than an error — the asymmetry with EnsureBalancer
// is deliberate, and it is the same one the driver's Remove documents.
func (b Binding) RemoveBalancer(ctx context.Context, network, listen string) error {
	bal := b.balancer()
	if bal == nil {
		return nil
	}
	return bal.RemoveBalancer(ctx, network, listen)
}
