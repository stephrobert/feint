package machine

import (
	"context"
	"sync"
)

// One address, one machine — enforced where the address actually lands rather
// than in each pack's idea of who may hold it.
//
// The three packs disagree at the API, and they are right to: the clouds
// disagree. Scaleway moves a flexible IP to the server you name. Outscale refuses
// unless the request passes AllowRelink, which is its own documented default.
// Exoscale accepts the same Elastic IP on several instances at once — measured on
// ch-gva-2, where two instances both reported holding 185.19.28.243, and it is
// the whole point of the healthcheck an Elastic IP carries.
//
// So the control planes must differ, and #213 asked for the shared part to be the
// bookkeeping. This is it. Whatever a pack allows its client to record, the
// runtime carries an emulated address on at most one machine, because the
// alternative is not a second opinion but a coin toss: two containers answering
// ARP for one /32, the host picking arbitrarily, and the API describing both as
// holders. An emulator that publishes an address its machine may or may not carry
// has given up the only thing it was built to promise.
//
// What this deliberately does not do is elect. Exoscale's platform elects by
// healthcheck; feint has no healthcheck and does not invent one, so the address
// follows the most recent attach and docs/limits.md says so. Guessing the winner
// would be worse than naming the rule.

// placements records which machine an emulated address is routed to, keyed by
// provider so two packs numbering their addresses the same way do not collide.
//
// Package-level for the same reason serialise.Lock is: Binding is a value built
// per call, so state that must outlive one request cannot live in it.
var placements sync.Map // string -> string

func placementKey(provider, address string) string { return provider + "|" + address }

// RouteAddress gives the address to one machine and takes it back from whichever
// machine had it.
//
// The order matters and is not an implementation detail: the previous holder is
// unrouted first, so no instant exists where two machines carry the address. Both
// packs that got this right wrote the same sentence about it in their own file —
// "moving the address means taking it back first" — which is how it came to be
// written twice and missing the third time.
//
// A failure to unroute the previous holder is returned rather than swallowed. The
// caller wanted the address moved, and a move whose first half failed has left
// the address where it was.
func (b Binding) RouteAddress(ctx context.Context, router Router, spec AddressSpec) error {
	if router == nil || spec.Address == "" || spec.Machine == "" {
		return nil
	}
	key := placementKey(b.Provider, spec.Address)

	if held, ok := placements.Load(key); ok {
		if previous, _ := held.(string); previous != "" && previous != spec.Machine {
			if err := router.UnrouteAddress(ctx, previous, spec.Address); err != nil {
				return err
			}
		}
	}
	if err := router.RouteAddress(ctx, spec); err != nil {
		return err
	}
	placements.Store(key, spec.Machine)
	return nil
}

// UnrouteAddress takes the address back and forgets the placement.
//
// The machine name is honoured rather than assumed: a pack may unroute from a
// machine that no longer holds the address — a delete arriving after a move, say
// — and forgetting the placement then would leave the current holder unknown, so
// the next move would not take it back from anybody. Only a call naming the
// recorded holder clears the record.
func (b Binding) UnrouteAddress(ctx context.Context, router Router, machine, address string) error {
	if router == nil || address == "" {
		return nil
	}
	err := router.UnrouteAddress(ctx, machine, address)

	key := placementKey(b.Provider, address)
	if held, ok := placements.Load(key); ok {
		if current, _ := held.(string); current == machine || machine == "" {
			placements.Delete(key)
		}
	}
	return err
}

// ForgetPlacements drops every recorded placement for this provider. Tests use
// it; nothing else should, because an emulator that forgets where an address is
// stops taking it back.
func (b Binding) ForgetPlacements() {
	prefix := b.Provider + "|"
	placements.Range(func(key, _ any) bool {
		if name, _ := key.(string); len(name) > len(prefix) && name[:len(prefix)] == prefix {
			placements.Delete(key)
		}
		return true
	})
}
