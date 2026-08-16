package machine

import (
	"context"
	"sync"
	"testing"
)

type placementRouter struct {
	mu       sync.Mutex
	routed   []AddressSpec
	unrouted [][2]string
	failNext error
}

func (r *placementRouter) RouteAddress(_ context.Context, spec AddressSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routed = append(r.routed, spec)
	return nil
}

func (r *placementRouter) UnrouteAddress(_ context.Context, machine, address string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.failNext; err != nil {
		r.failNext = nil
		return err
	}
	r.unrouted = append(r.unrouted, [2]string{machine, address})
	return nil
}

func placementFixture(t *testing.T) (Binding, *placementRouter) {
	t.Helper()
	b := Binding{Provider: "example"}
	b.ForgetPlacements()
	t.Cleanup(b.ForgetPlacements)
	return b, &placementRouter{}
}

// Moving an address takes it back first, and the order is the property: an
// address routed to the new machine before being withdrawn from the old one
// leaves an instant with two machines answering for one /32, which is precisely
// what a client cannot debug.
func TestRouteAddressTakesItBackFromThePreviousHolderFirst(t *testing.T) {
	b, router := placementFixture(t)
	ctx := context.Background()

	if err := b.RouteAddress(ctx, router, AddressSpec{Machine: "one", Address: "192.0.2.7"}); err != nil {
		t.Fatalf("first route: %v", err)
	}
	if len(router.unrouted) != 0 {
		t.Fatalf("the first placement withdrew from somebody: %v", router.unrouted)
	}

	if err := b.RouteAddress(ctx, router, AddressSpec{Machine: "two", Address: "192.0.2.7"}); err != nil {
		t.Fatalf("second route: %v", err)
	}
	if len(router.unrouted) != 1 || router.unrouted[0] != [2]string{"one", "192.0.2.7"} {
		t.Fatalf("the previous holder was not withdrawn: %v", router.unrouted)
	}
	if len(router.routed) != 2 || router.routed[1].Machine != "two" {
		t.Fatalf("the address did not reach the new machine: %v", router.routed)
	}
}

// A move whose withdrawal failed has not moved anything, so it must not report
// success and must not record a placement that never happened — the next move
// would then take the address back from the wrong machine.
func TestRouteAddressReportsAFailedWithdrawalInsteadOfMovingAnyway(t *testing.T) {
	b, router := placementFixture(t)
	ctx := context.Background()

	if err := b.RouteAddress(ctx, router, AddressSpec{Machine: "one", Address: "192.0.2.7"}); err != nil {
		t.Fatalf("first route: %v", err)
	}
	router.failNext = context.DeadlineExceeded
	if err := b.RouteAddress(ctx, router, AddressSpec{Machine: "two", Address: "192.0.2.7"}); err == nil {
		t.Fatal("a move whose withdrawal failed reported success")
	}
	if len(router.routed) != 1 {
		t.Errorf("the address reached the second machine although the first still held it: %v", router.routed)
	}

	// And the record still names the first machine, so a later move withdraws
	// from the machine that actually has it.
	router.failNext = nil
	if err := b.RouteAddress(ctx, router, AddressSpec{Machine: "two", Address: "192.0.2.7"}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(router.unrouted) != 1 || router.unrouted[0][0] != "one" {
		t.Errorf("the retry withdrew from %v, not from the machine that held it", router.unrouted)
	}
}

// Re-routing to the same machine is the ordinary repair path — a pack replays
// routes on every read of a running machine — and must not withdraw the address
// from the machine it is about to give it back to.
func TestRouteAddressDoesNotWithdrawFromTheMachineItIsRoutingTo(t *testing.T) {
	b, router := placementFixture(t)
	ctx := context.Background()

	for range 3 {
		if err := b.RouteAddress(ctx, router, AddressSpec{Machine: "one", Address: "192.0.2.7"}); err != nil {
			t.Fatalf("route: %v", err)
		}
	}
	if len(router.unrouted) != 0 {
		t.Errorf("a replay withdrew the address it was replacing: %v", router.unrouted)
	}
}

// An unroute naming a machine that no longer holds the address must not erase the
// record of who does. A delete arriving after a move is exactly that, and
// forgetting there would leave the current holder unknown — so the next move
// would take the address back from nobody and two machines would carry it.
func TestUnrouteAddressKeepsThePlacementWhenAnotherMachineHoldsIt(t *testing.T) {
	b, router := placementFixture(t)
	ctx := context.Background()

	_ = b.RouteAddress(ctx, router, AddressSpec{Machine: "one", Address: "192.0.2.7"})
	_ = b.RouteAddress(ctx, router, AddressSpec{Machine: "two", Address: "192.0.2.7"})
	if err := b.UnrouteAddress(ctx, router, "one", "192.0.2.7"); err != nil {
		t.Fatalf("late unroute: %v", err)
	}

	before := len(router.unrouted)
	_ = b.RouteAddress(ctx, router, AddressSpec{Machine: "three", Address: "192.0.2.7"})
	if len(router.unrouted) != before+1 || router.unrouted[before][0] != "two" {
		t.Errorf("the move withdrew from %v; the holder was two", router.unrouted[before:])
	}
}

// Two providers numbering their addresses the same way do not take each other's
// routes back. RFC 5737 gives each pack its own block, but the emulated blocks
// are configuration and the key must not depend on them staying distinct.
func TestPlacementsAreKeptPerProvider(t *testing.T) {
	first, router := placementFixture(t)
	second := Binding{Provider: "other"}
	second.ForgetPlacements()
	t.Cleanup(second.ForgetPlacements)
	ctx := context.Background()

	_ = first.RouteAddress(ctx, router, AddressSpec{Machine: "one", Address: "192.0.2.7"})
	_ = second.RouteAddress(ctx, router, AddressSpec{Machine: "two", Address: "192.0.2.7"})

	if len(router.unrouted) != 0 {
		t.Errorf("one pack took another pack's address back: %v", router.unrouted)
	}
}
