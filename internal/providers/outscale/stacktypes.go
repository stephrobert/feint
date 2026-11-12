package outscale

import "github.com/stephrobert/feint/internal/stack"

// What a Terraform type of this provider asks the API to do.
//
// # WHY THIS TABLE IS OPERATION NAMES AND NOTHING ELSE
//
// The reason a refused type is refused is not written here. It is read from
// this pack's own Declined() list, by the operation name below. #298 measured
// what a second copy costs: a decline reason rewritten in a pack took four days
// to reach the committed artefact, while two gates agreed with each other and
// both disagreed with the code. One source, two readers.
//
// The same joins make the table checkable rather than believable:
// TestEveryStackOperationIsOneThatPackDecided fails on a name this pack neither
// serves nor declines, which is what a typo and an obsolete row both look like.
//
// # WHY THE PREFIX OF A TYPE NAME IS NOT USED
//
// Because it decides nothing. Measured 2026-11-12: a resource's provider comes
// from `provider =` or from the type's prefix looked up in the enclosing
// module's required_providers, defaulting to hashicorp/<name> — so
// `scaleway_instance_server` with no required_providers resolves to
// `hashicorp/scaleway`. And on this side of the join, a type's name does not
// give its product either: `outscale_nic_private_ip` is three operations away
// from anything called "nic" upstream. Every row here is written out.
//
// # HOW THE ROWS WERE OBTAINED, WHICH IS THE ONLY THING THAT MAKES THEM TRUE
//
// The served rows: `terraform apply` of examples/stacks/outscale against an
// emulator on 2026-11-12, 48 resources created and the operations answered. A
// row claiming a type is served that nothing has applied would be the
// "compatible" this preflight exists to avoid printing.
//
// The refused rows: the same apply, with three resources added. The emulator
// answered 404 `OperationNotEmulated` naming CreateVirtualGateway and
// CreateClientGateway, after creating 48 resources and leaving 50 entries in
// the state. The third, outscale_vpn_connection, was never attempted: its
// dependencies had failed, so an operator who fixed the first two would pay a
// second apply to discover it. Those three rows are the measurement that
// justifies a preflight at all.
//
// # TWO PROVIDER VERSIONS, AND THAT IS THE POINT
//
// The apply above was run twice on 2026-11-12: once on outscale/outscale 1.8.0,
// the copy that was on the machine, and once on 1.8.1, which is what the
// registry's `~> 1.7` resolves to today and what #873 pins the stack to. Both
// answered identically — 48 created, the same two operations refused by name,
// the same 50 entries in the state, and outscale_vpn_connection never attempted
// either time.
//
// The second run exists because the first measured a version nobody runs. A
// table grounded on a build that only ever existed on one laptop is a table
// nobody can re-derive, and this repository's rule is that a behaviour without
// its version is a memory rather than a fact. Naming both is what says these
// rows are not a property of one patch release.
//
// # WHAT A MISSING ROW MEANS
//
// `unknown`, never `compatible`. This table does not enumerate the provider's
// 57 resource types and must not pretend to: a type absent from it is a type
// nothing here has measured.
func (p *Pack) StackOperations() map[stack.TypeKey]string {
	return map[stack.TypeKey]string{
		// Served, each one applied and destroyed against the emulator.
		stack.Resource("outscale_net"):                      "osc/Client.CreateNet",
		stack.Resource("outscale_net_attributes"):           "osc/Client.UpdateNet",
		stack.Resource("outscale_subnet"):                   "osc/Client.CreateSubnet",
		stack.Resource("outscale_internet_service"):         "osc/Client.CreateInternetService",
		stack.Resource("outscale_internet_service_link"):    "osc/Client.LinkInternetService",
		stack.Resource("outscale_route_table"):              "osc/Client.CreateRouteTable",
		stack.Resource("outscale_route_table_link"):         "osc/Client.LinkRouteTable",
		stack.Resource("outscale_main_route_table_link"):    "osc/Client.LinkRouteTable",
		stack.Resource("outscale_route"):                    "osc/Client.CreateRoute",
		stack.Resource("outscale_nat_service"):              "osc/Client.CreateNatService",
		stack.Resource("outscale_public_ip"):                "osc/Client.CreatePublicIp",
		stack.Resource("outscale_public_ip_link"):           "osc/Client.LinkPublicIp",
		stack.Resource("outscale_security_group"):           "osc/Client.CreateSecurityGroup",
		stack.Resource("outscale_security_group_rule"):      "osc/Client.CreateSecurityGroupRule",
		stack.Resource("outscale_keypair"):                  "osc/Client.CreateKeypair",
		stack.Resource("outscale_vm"):                       "osc/Client.CreateVms",
		stack.Resource("outscale_volume"):                   "osc/Client.CreateVolume",
		stack.Resource("outscale_volume_link"):              "osc/Client.LinkVolume",
		stack.Resource("outscale_nic"):                      "osc/Client.CreateNic",
		stack.Resource("outscale_nic_link"):                 "osc/Client.LinkNic",
		stack.Resource("outscale_nic_private_ip"):           "osc/Client.LinkPrivateIps",
		stack.Resource("outscale_image"):                    "osc/Client.CreateImage",
		stack.Resource("outscale_snapshot"):                 "osc/Client.CreateSnapshot",
		stack.Resource("outscale_dhcp_option"):              "osc/Client.CreateDhcpOptions",
		stack.Resource("outscale_net_peering"):              "osc/Client.CreateNetPeering",
		stack.Resource("outscale_net_peering_acceptation"):  "osc/Client.AcceptNetPeering",
		stack.Resource("outscale_load_balancer"):            "osc/Client.CreateLoadBalancer",
		stack.Resource("outscale_load_balancer_attributes"): "osc/Client.UpdateLoadBalancer",
		stack.Resource("outscale_load_balancer_vms"):        "osc/Client.RegisterVmsInLoadBalancer",
		stack.Resource("outscale_flexible_gpu"):             "osc/Client.CreateFlexibleGpu",

		// Refused, each one measured as a 404 OperationNotEmulated or declined
		// beside the ones that were. The reason is NOT here: Declined() has it.
		stack.Resource("outscale_virtual_gateway"):      "osc/Client.CreateVirtualGateway",
		stack.Resource("outscale_client_gateway"):       "osc/Client.CreateClientGateway",
		stack.Resource("outscale_vpn_connection"):       "osc/Client.CreateVpnConnection",
		stack.Resource("outscale_vpn_connection_route"): "osc/Client.CreateVpnConnectionRoute",

		// Data sources, read-only, and kept here because a plan reads them
		// before it creates anything: a stack whose first data source is not
		// served fails before any of this matters.
		stack.Data("outscale_subregions"): "osc/Client.ReadSubregions",
		stack.Data("outscale_images"):     "osc/Client.ReadImages",
	}
}
