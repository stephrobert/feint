package outscale

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
// emulator on 2026-11-12, provider outscale/outscale 1.8.0, 48 resources
// created and the operations answered. A row claiming a type is served that
// nothing has applied would be the "compatible" this preflight exists to avoid
// printing.
//
// The refused rows: the same apply, with three resources added. The emulator
// answered 404 `OperationNotEmulated` naming CreateVirtualGateway and
// CreateClientGateway, after creating 48 resources and leaving 50 entries in
// the state. The third, outscale_vpn_connection, was never attempted: its
// dependencies had failed, so an operator who fixed the first two would pay a
// second apply to discover it. Those three rows are the measurement that
// justifies a preflight at all.
//
// # WHAT A MISSING ROW MEANS
//
// `unknown`, never `compatible`. This table does not enumerate the provider's
// 57 resource types and must not pretend to: a type absent from it is a type
// nothing here has measured.
func (p *Pack) StackOperations() map[string]string {
	return map[string]string{
		// Served, each one applied and destroyed against the emulator.
		"outscale_net":                      "osc/Client.CreateNet",
		"outscale_net_attributes":           "osc/Client.UpdateNet",
		"outscale_subnet":                   "osc/Client.CreateSubnet",
		"outscale_internet_service":         "osc/Client.CreateInternetService",
		"outscale_internet_service_link":    "osc/Client.LinkInternetService",
		"outscale_route_table":              "osc/Client.CreateRouteTable",
		"outscale_route_table_link":         "osc/Client.LinkRouteTable",
		"outscale_main_route_table_link":    "osc/Client.LinkRouteTable",
		"outscale_route":                    "osc/Client.CreateRoute",
		"outscale_nat_service":              "osc/Client.CreateNatService",
		"outscale_public_ip":                "osc/Client.CreatePublicIp",
		"outscale_public_ip_link":           "osc/Client.LinkPublicIp",
		"outscale_security_group":           "osc/Client.CreateSecurityGroup",
		"outscale_security_group_rule":      "osc/Client.CreateSecurityGroupRule",
		"outscale_keypair":                  "osc/Client.CreateKeypair",
		"outscale_vm":                       "osc/Client.CreateVms",
		"outscale_volume":                   "osc/Client.CreateVolume",
		"outscale_volume_link":              "osc/Client.LinkVolume",
		"outscale_nic":                      "osc/Client.CreateNic",
		"outscale_nic_link":                 "osc/Client.LinkNic",
		"outscale_nic_private_ip":           "osc/Client.LinkPrivateIps",
		"outscale_image":                    "osc/Client.CreateImage",
		"outscale_snapshot":                 "osc/Client.CreateSnapshot",
		"outscale_dhcp_option":              "osc/Client.CreateDhcpOptions",
		"outscale_net_peering":              "osc/Client.CreateNetPeering",
		"outscale_net_peering_acceptation":  "osc/Client.AcceptNetPeering",
		"outscale_load_balancer":            "osc/Client.CreateLoadBalancer",
		"outscale_load_balancer_attributes": "osc/Client.UpdateLoadBalancer",
		"outscale_load_balancer_vms":        "osc/Client.RegisterVmsInLoadBalancer",
		"outscale_flexible_gpu":             "osc/Client.CreateFlexibleGpu",

		// Refused, each one measured as a 404 OperationNotEmulated or declined
		// beside the ones that were. The reason is NOT here: Declined() has it.
		"outscale_virtual_gateway":      "osc/Client.CreateVirtualGateway",
		"outscale_client_gateway":       "osc/Client.CreateClientGateway",
		"outscale_vpn_connection":       "osc/Client.CreateVpnConnection",
		"outscale_vpn_connection_route": "osc/Client.CreateVpnConnectionRoute",

		// Data sources, read-only, and kept here because a plan reads them
		// before it creates anything: a stack whose first data source is not
		// served fails before any of this matters.
		"outscale_subregions": "osc/Client.ReadSubregions",
		"outscale_images":     "osc/Client.ReadImages",
	}
}
