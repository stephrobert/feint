package machine

import "context"

// A public address a cloud hands out is routed to the machine that holds it, and
// on Scaleway's routed-IP mode the machine carries it: the server sees its own
// public address on its interface. Emulators stop at the record, floci going as
// far as reporting 127.0.0.1 for every instance.
//
// This file states what a driver must offer. How Incus offers it, and the two
// approaches that were measured before one was chosen, are in incus_address.go.

// AddressSpec routes an emulated public address to a machine.
type AddressSpec struct {
	// Machine is the machine that will carry the address.
	Machine string
	// Address is the emulated public address, without a mask. It must exist
	// nowhere else, which is why the packs take theirs from the ranges RFC 5737
	// reserves for documentation: routing a real public address would capture
	// the host's own traffic towards it.
	Address string
	// Network names the network to route it through. Empty lets the driver
	// pick, which it does badly on purpose: it cannot tell the runtime's own
	// default bridge from a network the emulator created, and a public address
	// belongs on the one the server actually lives on. Packs that know should
	// say.
	Network string
}

// router is the optional half of a driver that can give a machine a public
// address. Separate from the driver so a runtime without the capability is a
// compile-time fact rather than a silent no-op.
type router interface {
	// RouteAddress makes the address reach the machine, and the machine carry
	// it. Calling it twice with the same pair is harmless.
	RouteAddress(ctx context.Context, spec AddressSpec) error
	// UnrouteAddress takes it back, and succeeds when nothing was routed.
	UnrouteAddress(ctx context.Context, machine, address string) error
}
