package exoscale

import "github.com/stephrobert/feint/internal/stack"

// What a Terraform type of this provider asks the API to do.
//
// The reason a refused type is refused is not written here: it is read from
// this pack's own Declined() list, by the operation name below. One source, two
// readers (#298).
//
// # HOW THESE ROWS WERE OBTAINED
//
// Every one is a type of examples/stacks/exoscale, which
// tools/conformance/stacks.sh applies and destroys against an emulator with a
// real Terraform and a real OpenTofu on every pull request and every night.
// Terraform has driven this pack since 2026-09-05 against the published
// provider v0.71.0, which is the floor docs/limits.md names.
//
// Two things this provider does that the other two do not, and that the rows
// show: its operation names are the document's own kebab-case verbs rather than
// an SDK method, and `register-ssh-key` is a register rather than a create,
// which is why reading the name off the type would have produced nothing.
//
// # WHAT IS NOT HERE
//
// `aws_s3_bucket`, which the example stack uses for object storage through the
// S3 provider: it is not an Exoscale type and this pack must not claim it.
// Anything else reads as unknown, which is the honest answer for a type no
// recorded run has applied here.
func (p *Pack) StackOperations() map[stack.TypeKey]string {
	return map[stack.TypeKey]string{
		stack.Resource("exoscale_compute_instance"):              "exoscale/v2.create-instance",
		stack.Resource("exoscale_instance_pool"):                 "exoscale/v2.create-instance-pool",
		stack.Resource("exoscale_anti_affinity_group"):           "exoscale/v2.create-anti-affinity-group",
		stack.Resource("exoscale_private_network"):               "exoscale/v2.create-private-network",
		stack.Resource("exoscale_elastic_ip"):                    "exoscale/v2.create-elastic-ip",
		stack.Resource("exoscale_security_group"):                "exoscale/v2.create-security-group",
		stack.Resource("exoscale_security_group_rule"):           "exoscale/v2.add-rule-to-security-group",
		stack.Resource("exoscale_block_storage_volume"):          "exoscale/v2.create-block-storage-volume",
		stack.Resource("exoscale_block_storage_volume_snapshot"): "exoscale/v2.create-block-storage-snapshot",
		stack.Resource("exoscale_nlb"):                           "exoscale/v2.create-load-balancer",
		stack.Resource("exoscale_nlb_service"):                   "exoscale/v2.add-service-to-load-balancer",
		stack.Resource("exoscale_ssh_key"):                       "exoscale/v2.register-ssh-key",

		// Data sources: a plan reads them before it creates anything, so a
		// stack whose first read is not served fails before any of the rows
		// above matter.
		stack.Data("exoscale_template"):      "exoscale/v2.list-templates",
		stack.Data("exoscale_elastic_ip"):    "exoscale/v2.get-elastic-ip",
		stack.Data("exoscale_instance_pool"): "exoscale/v2.get-instance-pool",
		// The one that made the key carry its mode: as a resource this name is
		// create-load-balancer, as a data source it is get-load-balancer. Two
		// different questions under one word.
		stack.Data("exoscale_nlb"): "exoscale/v2.get-load-balancer",
	}
}
