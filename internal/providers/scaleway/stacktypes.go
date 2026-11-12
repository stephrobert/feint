package scaleway

import "github.com/stephrobert/feint/internal/stack"

// What a Terraform type of this provider asks the API to do.
//
// The reason a refused type is refused is not written here: it is read from
// this pack's own Declined() list, by the operation name below. One source, two
// readers, for the reason #298 measured — a decline reason rewritten in a pack
// took four days to reach the committed artefact while two gates agreed with
// each other and both disagreed with the code.
//
// # HOW THESE ROWS WERE OBTAINED
//
// Every one of them is a type of examples/stacks/scaleway, which
// tools/conformance/stacks.sh applies and destroys against an emulator with a
// real Terraform and a real OpenTofu, on every pull request and every night.
// The operation names are this pack's own, joined by
// TestEveryStackOperationIsOneThatPackDecided: a name this pack neither serves
// nor declines fails the build, which is what a typo and an outlived row both
// look like.
//
// WHY THE TYPE'S PREFIX IS NOT USED TO FIND ITS PRODUCT
//
// Because it is wrong. `scaleway_vpc_public_gateway` and
// `scaleway_vpc_gateway_network` are product `vpcgw` upstream, not `vpc`, and
// `scaleway_block_volume` is `block` while `scaleway_instance_volume` is
// `instance`. Every row is written out.
//
// # WHAT IS NOT HERE, AND WHY IT READS AS UNKNOWN RATHER THAN REFUSED
//
// The products the drift scan does not cover — RDB, Registry, Kapsule, Domain
// among them: the Scaleway scan covers 11 of the 56 products in that SDK. This
// emulator serves none of their operations, so a stack using them will fail,
// and yet there is no row here for them: a row must name an operation this pack
// serves or declines, and an unscanned product has neither. Reporting them as
// refused would mean inventing a decision nobody took. They read as unknown,
// docs/limits.md says so, and giving them a standing of their own needs a guard
// that tells an unscanned product from a typo.
func (p *Pack) StackOperations() map[stack.TypeKey]string {
	return map[stack.TypeKey]string{
		// instance
		stack.Resource("scaleway_instance_server"):          "instance/v1/API.CreateServer",
		stack.Resource("scaleway_instance_ip"):              "instance/v1/API.CreateIP",
		stack.Resource("scaleway_instance_volume"):          "instance/v1/API.CreateVolume",
		stack.Resource("scaleway_instance_snapshot"):        "instance/v1/API.CreateSnapshot",
		stack.Resource("scaleway_instance_image"):           "instance/v1/API.CreateImage",
		stack.Resource("scaleway_instance_security_group"):  "instance/v1/API.CreateSecurityGroup",
		stack.Resource("scaleway_instance_placement_group"): "instance/v1/API.CreatePlacementGroup",
		stack.Resource("scaleway_instance_private_nic"):     "instance/v1/API.CreatePrivateNIC",
		stack.Resource("scaleway_instance_user_data"):       "instance/v1/API.SetServerUserData",

		// block storage, which is a different product from the instance volume
		// above however alike the two names look
		stack.Resource("scaleway_block_volume"):   "block/v1alpha1/API.CreateVolume",
		stack.Resource("scaleway_block_snapshot"): "block/v1alpha1/API.CreateSnapshot",

		// vpc, and the gateway that is product vpcgw
		stack.Resource("scaleway_vpc"):                   "vpc/v2/API.CreateVPC",
		stack.Resource("scaleway_vpc_private_network"):   "vpc/v2/API.CreatePrivateNetwork",
		stack.Resource("scaleway_vpc_route"):             "vpc/v2/API.CreateRoute",
		stack.Resource("scaleway_vpc_acl"):               "vpc/v2/API.SetACL",
		stack.Resource("scaleway_vpc_public_gateway"):    "vpcgw/v2/API.CreateGateway",
		stack.Resource("scaleway_vpc_public_gateway_ip"): "vpcgw/v2/API.CreateIP",
		stack.Resource("scaleway_vpc_gateway_network"):   "vpcgw/v2/API.CreateGatewayNetwork",

		// ipam
		stack.Resource("scaleway_ipam_ip"): "ipam/v1/API.BookIP",

		// load balancer
		stack.Resource("scaleway_lb"):          "lb/v1/ZonedAPI.CreateLB",
		stack.Resource("scaleway_lb_ip"):       "lb/v1/ZonedAPI.CreateIP",
		stack.Resource("scaleway_lb_backend"):  "lb/v1/ZonedAPI.CreateBackend",
		stack.Resource("scaleway_lb_frontend"): "lb/v1/ZonedAPI.CreateFrontend",
		stack.Resource("scaleway_lb_route"):    "lb/v1/ZonedAPI.CreateRoute",
		stack.Resource("scaleway_lb_acl"):      "lb/v1/ZonedAPI.CreateACL",

		// iam
		stack.Resource("scaleway_iam_ssh_key"): "iam/v1alpha1/API.CreateSSHKey",

		// account, which every third-party VPC stack reads first (#372)
		stack.Resource("scaleway_account_project"): "account/v3/ProjectAPI.CreateProject",
	}
}
