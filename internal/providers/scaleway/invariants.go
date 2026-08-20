package scaleway

import "github.com/stephrobert/feint/internal/core/emulator"

// ReplayInvariants implements emulator.Invariable: what `feint replay` may
// compare on this pack's answers beyond the presence and the type it compares
// everywhere.
//
// The default is deliberately weak — an identifier, a timestamp and an address
// differ between two runs of the same request, so comparing them would paint
// every replay red — and the entries below are the places where that default is
// too weak, each one a defect this repository has already measured.
//
// **The order of public_ips is the contract (#320).** Terraform stores
// Server.public_ips as a list, so a read that returns the addresses in store
// order rather than in the order the create named them is a plan diff that
// never converges: the two-way swap was reproduced against talos. A replay that
// ignored ordering everywhere would have called that run clean, which is why
// this is a declaration and not a comment.
//
// **A name the request carried has to come back (the unread-field family).**
// UpdateServer.public_ips was declared, accepted, answered 200 and read by
// nobody for the whole of #320's lifetime, and the only signal that says so is
// a value the client named and the answer does not carry. `name` and
// `commercial_type` are the two the client always sends on a create, and they
// are the two the emulator has no licence to reinterpret.
//
// What is deliberately absent: anything the cloud mints. `id`, `creation_date`,
// `public_ip.address` and `mac_address` are outside this list on purpose, and a
// future entry naming one of them is the mistake this comment exists to
// prevent.
// publicIPsOrder is the one spelling of the ordered sequence, used by the three
// operations that answer it.
//
// One constant rather than three literals, and the falsification is the reason:
// with the path written out three times, breaking the create's declaration left
// the read's and the update's still evaluating, the run still counted an order
// check, and the test meant to prove #320's guard stayed green
// (tools/falsify/specs/replay-compares.json, run of 2026-08-20).
const publicIPsOrder = "server.public_ips[].id"

func (p *Pack) ReplayInvariants() []emulator.Invariant {
	return []emulator.Invariant{
		{
			Operation: "instance/v1/API.CreateServer",
			Path:      "server.name",
			Kind:      emulator.InvariantValue,
			Reason:    "the client names the server in the request, and an API that accepts a name and answers another is the unread-request-field defect this project measures",
		},
		{
			Operation: "instance/v1/API.CreateServer",
			Path:      "server.commercial_type",
			Kind:      emulator.InvariantValue,
			Reason:    "the client chooses the commercial type in the request, and the answer describes what was created, not what the catalogue would have preferred",
		},
		{
			Operation: "instance/v1/API.CreateServer",
			Path:      publicIPsOrder,
			Kind:      emulator.InvariantOrder,
			Reason:    "Terraform stores public_ips as a list, so an answer in store order rather than in the order the create named is a plan diff that never converges (#320)",
		},
		{
			Operation: "instance/v1/API.GetServer",
			Path:      publicIPsOrder,
			Kind:      emulator.InvariantOrder,
			Reason:    "the read has to agree with the create it follows, or the client sees the order move on its own between two plans (#320)",
		},
		{
			Operation: "instance/v1/API.UpdateServer",
			Path:      publicIPsOrder,
			Kind:      emulator.InvariantOrder,
			Reason:    "UpdateServer.public_ips is the client's own reconciliation list, and the answer states the order it just asked for (#320)",
		},
	}
}
