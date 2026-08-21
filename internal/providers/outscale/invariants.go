package outscale

import "github.com/stephrobert/feint/internal/core/emulator"

// ReplayInvariants implements emulator.Invariable: what `feint replay` may
// compare on this pack's answers beyond the presence and the type it compares
// everywhere.
//
// This pack declared nothing at all until #354, and that absence was invisible
// for the reason CLAUDE.md names most often: `feint corpus --check` printed
// "0 divergent finding(s)" over a run in which no value and no order of an
// Outscale answer had been compared, which reads as "nothing is wrong" and
// meant "those comparisons did not happen". The gate's own counters
// (values_checked, orders_checked) were carried entirely by the Scaleway pack.
//
// The default is deliberately weak — an identifier, a timestamp and an address
// differ between two runs of the same request, so comparing them would paint
// every replay red — and the entries below are the places where that default is
// too weak. Each is a value the *client* put in the request, or an order a
// client stores as a list.
//
// What is deliberately absent, on the same rule the Scaleway pack states:
// anything the cloud mints. VmId, ImageId, PublicIp, CreationDate and the
// private addresses are outside this list on purpose, and a future entry naming
// one of them is the mistake this comment exists to prevent.
//
// # The order a replay can grade, and the one it cannot
//
// **A machine's security groups are not here, and that is a measurement rather
// than an oversight.** The cloud orders them by `SecurityGroupId` ascending —
// measured on 2026-08-21 against a real account, on the recording's own
// `UpdateVm` and confirmed against the account's two long-lived machines, which
// refute the other candidate rule (their name order is not the order answered):
//
//	machine A  sg-2222aaaa "ssh-only",  sg-3333bbbb "alerting"
//	machine B  sg-2222aaaa "ssh-only",  sg-ffffcccc "open-all"
//
// The two rows above are anonymised, and the anonymisation keeps the only thing
// they are evidence of: the ids ascend, and in neither row is the name order the
// answered one. The real identifiers and group names are a live account's
// inventory and this repository is public — docs/proxy.md states the rule:
// name a path, a type, a status and a position, never a value.
//
// This pack now sorts the same way ([effectiveSecurityGroups]), and a *replay*
// still cannot grade it: the order is derived from identifiers the cloud minted,
// and no emulator mints those. `feint replay` maps the recorded sequence into
// this emulator's namespace and compares position by position, so it asks "is
// the object that was first upstream first here" — and the answer depends on two
// unrelated id spaces sorting the same way, which is a coincidence rather than a
// property. Declaring it would buy a permanent exemption, and a permanent
// exemption is a gate that has quietly stopped covering what it names.
//
// So the guard lives where it can bite: TestAMachinesSecurityGroupsAnswerInIdentifierOrder,
// a unit test of this pack that does not depend on a second id space existing.
// #379 records the whole of it.
//
// What is declared instead is an order the cloud derives from a **value both
// sides carry**: the routes of a route table, ordered by destination. The
// recording's table answers `0.0.0.0/0` before the Net's own range, which is
// that string order, and a sanitised transcript keeps both — `0.0.0.0/0`
// verbatim, the Net's range as a block of the synthetic space. Terraform's
// `outscale_route_table` stores routes as a list, so this is #320's family with
// a subject a replay can actually hold.
const routesOrder = "RouteTables[].Routes[].DestinationIpRange"

func (p *Pack) ReplayInvariants() []emulator.Invariant {
	return []emulator.Invariant{
		{
			Operation: operation("CreateVms"),
			Path:      "Vms[].VmType",
			Kind:      emulator.InvariantValue,
			Reason:    "the client chooses the VM type in the request, and the answer describes what was created, not what the catalogue would have preferred — the unread-request-field defect this project measures",
		},
		{
			Operation: operation("CreateNet"),
			Path:      "Net.IpRange",
			Kind:      emulator.InvariantValue,
			Reason:    "the client names the Net's address range and every Subnet it later creates is validated against it, so an API that accepts one range and answers another sends the next request into a refusal it cannot explain",
		},
		{
			Operation: operation("CreateSubnet"),
			Path:      "Subnet.IpRange",
			Kind:      emulator.InvariantValue,
			Reason:    "the client names the Subnet's range, and a stack that reads back a different one plans a replacement of a subnet it just made",
		},
		{
			Operation: operation("ReadRouteTables"),
			Path:      routesOrder,
			Kind:      emulator.InvariantOrder,
			Reason:    "Terraform stores a route table's routes as a list, so a read that returns them in this emulator's own order rather than the cloud's is a plan diff that never converges (#320, one provider out)",
		},
		{
			Operation: operation("CreateRoute"),
			Path:      "RouteTable.Routes[].DestinationIpRange",
			Kind:      emulator.InvariantOrder,
			Reason:    "the create answers the whole table, and it has to agree with the read that follows it or the client sees the order move on its own between two plans",
		},
	}
}
