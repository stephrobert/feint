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
// # The order of a machine's security groups
//
// `securityGroupsOrder` is the Outscale spelling of #320. Terraform's
// `outscale_vm` stores `security_group_ids` as a list, so a read that returns
// the groups in store order rather than in the order the answer carried is a
// plan diff that never converges — the defect that cost a pull request on the
// Scaleway side, in the family that produced it.
//
// **It is not "the order the client named", and that distinction is measured.**
// The recording of 2026-08-21 sent `UpdateVm` the two groups web-then-db and the
// cloud answered db-then-web. So what this holds the emulator to is the order
// *the cloud answered*, which is the only thing a replay can grade and the only
// thing a client's plan actually compares against.
const securityGroupsOrder = "Vm.SecurityGroups[].SecurityGroupId"

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
			Operation: operation("UpdateVm"),
			Path:      securityGroupsOrder,
			Kind:      emulator.InvariantOrder,
			Reason:    "UpdateVm.SecurityGroupIds is the client's own reconciliation list and Terraform stores the answer as a list, so an order of this emulator's own is a plan diff that never converges (#320, one provider out)",
		},
		{
			Operation: operation("ReadVms"),
			Path:      "Vms[].SecurityGroups[].SecurityGroupId",
			Kind:      emulator.InvariantOrder,
			Reason:    "the read has to agree with the update it follows, or the client sees the order move on its own between two plans (#320, one provider out)",
		},
	}
}
