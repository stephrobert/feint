package outscale

import (
	"slices"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// Declined implements emulator.Pack.
//
// Only what cannot be answered honestly is here. Everything else the scan
// reports as unknown is backlog, and the coverage report is the list of it.
//
// The names are exact because they are checked: the drift report calls out an
// entry matching nothing upstream as an orphan, which is how the four this list
// used to hold were found to be wrong.
func (p *Pack) Declined() []emulator.Decline {
	return slices.Concat(
		// Outscale's own inventory and billing.
		//
		// Measured, and worth stating because it is what makes this refusal
		// revisitable: ReadQuotas and ReadAccounts appear in no conformance
		// script and no Terraform fixture in this repository — the official
		// clients read them from data sources, never on the path of an apply —
		// so refusing them breaks no plan anybody here runs. An emulator has one implicit
		// account, no consumption and no price list, and inventing figures
		// would be worse than answering nothing.
		emulator.Because("the emulator has one implicit account with no consumption and no price list, so anything here would be a figure it invented and somebody acted on",
			"osc/Client.ReadCO2EmissionAccount",
			"osc/Client.CreateProductType",
			"osc/Client.DeleteProductType",
			"osc/Client.ReadProductTypes",
			"osc/Client.CreateAccount",
			"osc/Client.ReadAccounts",
			"osc/Client.UpdateAccount",
			"osc/Client.ReadConsumptionAccount",
			"osc/Client.ReadUnitPrice",
			"osc/Client.ReadQuotas"),

		// The audit trail of calls actually made against Outscale's platform.
		emulator.Because("the trail records calls made against Outscale's platform, and nothing here made any of them",
			"osc/Client.ReadApiLogs"),

		// The price and product catalogues. Not to be confused with ReadVmTypes,
		// which is the shape of the machines on offer and is on the critical
		// path of a create: that one stays to be served, small and fixed, the
		// way the Scaleway pack serves its own.
		emulator.Because("no client this project drives reads a price on its way to creating anything, which is the whole of it: where a catalogue is on a client's path the emulator does serve a fictional one, and docs/limits.md says so",
			"osc/Client.ReadCatalog",
			"osc/Client.ReadCatalogs",
			"osc/Client.ReadPublicCatalog",
			"osc/Client.ReadFlexibleGpuCatalog"),

		// Export tasks write an image or a snapshot into Object Storage, which
		// is not emulated: the reasons are in docs/limits.md and none of them
		// are about Outscale.
		emulator.Because("Export tasks write an image or a snapshot into Object Storage, which is not emulated: the reasons are in docs/limits.md and none of them are about Outscale",
			"osc/Client.CreateImageExportTask",
			"osc/Client.CreateSnapshotExportTask",
			"osc/Client.DeleteExportTask",
			"osc/Client.ReadImageExportTasks",
			"osc/Client.ReadSnapshotExportTasks"),

		// Identity and access management.
		//
		//
		// The emulator authenticates nothing: every credential is accepted on
		// purpose, SECURITY.md states it, and docs/roadmap.md lists verifying
		// signatures under Not planned. Serving user, group and policy
		// management on top of that would describe an access control that
		// nothing applies — a client could read back a policy denying an action
		// and then watch the emulator perform it. That gap is worse than a 404,
		// because a 404 is visible.
		emulator.Because("the emulator accepts every credential on purpose, so serving user and policy management would describe an access control that nothing here applies",
			"osc/Client.AddUserToUserGroup",
			"osc/Client.ReadPolicies",
			"osc/Client.ReadLinkedPolicies",
			"osc/Client.EnableOutscaleLogin",
			"osc/Client.DisableOutscaleLogin",
			"osc/Client.CreateCa",
			"osc/Client.ReadCas",
			"osc/Client.UpdateCa",
			"osc/Client.DeleteCa",
			"osc/Client.CreateAccessKey",
			"osc/Client.CreateApiAccessRule",
			"osc/Client.CreatePolicy",
			"osc/Client.CreatePolicyVersion",
			"osc/Client.CreateUser",
			"osc/Client.CreateUserGroup",
			"osc/Client.DeleteAccessKey",
			"osc/Client.DeleteApiAccessRule",
			"osc/Client.DeletePolicy",
			"osc/Client.DeletePolicyVersion",
			"osc/Client.DeleteUser",
			"osc/Client.DeleteUserGroup",
			"osc/Client.DeleteUserGroupPolicy",
			"osc/Client.DeleteUserPolicy",
			"osc/Client.DisableOutscaleLoginForUsers",
			"osc/Client.DisableOutscaleLoginPerUsers",
			"osc/Client.EnableOutscaleLoginForUsers",
			"osc/Client.EnableOutscaleLoginPerUsers",
			"osc/Client.LinkManagedPolicyToUserGroup",
			"osc/Client.LinkPolicy",
			"osc/Client.PutUserGroupPolicy",
			"osc/Client.PutUserPolicy",
			"osc/Client.ReadAccessKeys",
			"osc/Client.ReadApiAccessPolicy",
			"osc/Client.ReadApiAccessRules",
			"osc/Client.ReadEntitiesLinkedToPolicy",
			"osc/Client.ReadManagedPoliciesLinkedToUserGroup",
			"osc/Client.ReadPolicy",
			"osc/Client.ReadPolicyVersion",
			"osc/Client.ReadPolicyVersions",
			"osc/Client.ReadUserGroup",
			"osc/Client.ReadUserGroupPolicies",
			"osc/Client.ReadUserGroupPolicy",
			"osc/Client.ReadUserGroups",
			"osc/Client.ReadUserGroupsPerUser",
			"osc/Client.ReadUserPolicies",
			"osc/Client.ReadUserPolicy",
			"osc/Client.ReadUsers",
			"osc/Client.RemoveUserFromUserGroup",
			"osc/Client.SetDefaultPolicyVersion",
			"osc/Client.UnlinkManagedPolicyFromUserGroup",
			"osc/Client.UnlinkPolicy",
			"osc/Client.UpdateAccessKey",
			"osc/Client.UpdateApiAccessPolicy",
			"osc/Client.UpdateApiAccessRule",
			"osc/Client.UpdateUser",
			"osc/Client.UpdateUserGroup"),

		// Carrier connectivity: DirectLink and its interfaces and locations, VPN
		// connections and their routes, virtual and client gateways, and the
		// route propagation that depends on them.
		//
		// Net peerings and Net access points are deliberately NOT here. An audit
		// found them swept in, and they belong on the work list: both ends of a
		// peering are Nets this emulator creates, and machine.PeerNetworks
		// already exists to back one.
		//
		// Every one of them terminates somewhere this machine is not — a cross
		// connect in a carrier facility, a tunnel to a remote site, a
		// facility where a cross connect is patched. The runtime behind --vm creates bridges and
		// containers on one host and can back none of it. Answering "available"
		// on a link that does not exist is the exact answer this project exists
		// to avoid giving.
		emulator.Because("each of these belongs to a link terminating on a network this machine is not on — a cross connect, a tunnel to a remote site — and the facility list and route propagation exist only to serve one",
			"osc/Client.CreateClientGateway",
			"osc/Client.ReadLocations",
			"osc/Client.CreateDirectLink",
			"osc/Client.CreateDirectLinkInterface",
			"osc/Client.UpdateRoutePropagation",
			"osc/Client.CreateVirtualGateway",
			"osc/Client.CreateVpnConnection",
			"osc/Client.CreateVpnConnectionRoute",
			"osc/Client.DeleteClientGateway",
			"osc/Client.DeleteDirectLink",
			"osc/Client.DeleteDirectLinkInterface",
			"osc/Client.DeleteVirtualGateway",
			"osc/Client.DeleteVpnConnection",
			"osc/Client.DeleteVpnConnectionRoute",
			"osc/Client.LinkVirtualGateway",
			"osc/Client.ReadClientGateways",
			"osc/Client.ReadDirectLinkInterfaces",
			"osc/Client.ReadDirectLinks",
			"osc/Client.ReadVirtualGateways",
			"osc/Client.ReadVpnConnections",
			"osc/Client.UnlinkVirtualGateway",
			"osc/Client.UpdateDirectLinkInterface",
			"osc/Client.UpdateVpnConnection"),

		// Load balancing, and the server certificates that exist to terminate
		// TLS on it.
		//
		// A load balancer is a data plane: it accepts real connections, spreads
		// them over backends and health-checks what answers. The emulator has no
		// such plane, and a control plane alone would hand every client a DNS
		// name that resolves nowhere and a backend health nobody measured —
		// ReadVmsHealth answering "healthy" about traffic that cannot flow is
		// the exact invented answer this project refuses. Measured against a
		// real account (X-2 sweep, 2026-08-08): a fresh account answers these
		// with empty lists, so declining them breaks no plan that did not
		// already depend on a balancer existing.
		// ReadLoadBalancers is NOT here: its true answer is an empty list, and
		// declining a read whose honest answer is "none" costs a client the
		// ability to ask while buying no honesty — measured, it broke a
		// `terraform destroy`. See loadbalancers.go.
		emulator.Because("a load balancer is a data plane accepting real connections, and the emulator has none: creating one would hand out a DNS name resolving nowhere and a backend health nobody measured",
			"osc/Client.CreateLoadBalancer",
			"osc/Client.CreateLoadBalancerListeners",
			"osc/Client.CreateLoadBalancerPolicy",
			"osc/Client.CreateLoadBalancerTags",
			"osc/Client.CreateListenerRule",
			"osc/Client.DeleteLoadBalancer",
			"osc/Client.DeleteLoadBalancerListeners",
			"osc/Client.DeleteLoadBalancerPolicy",
			"osc/Client.DeleteLoadBalancerTags",
			"osc/Client.DeleteListenerRule",
			"osc/Client.DeregisterVmsInLoadBalancer",
			"osc/Client.LinkLoadBalancerBackendMachines",
			"osc/Client.ReadListenerRules",
			"osc/Client.ReadLoadBalancerTags",
			"osc/Client.ReadVmsHealth",
			"osc/Client.RegisterVmsInLoadBalancer",
			"osc/Client.UnlinkLoadBalancerBackendMachines",
			"osc/Client.UpdateListenerRule",
			"osc/Client.UpdateLoadBalancer",
			"osc/Client.CreateServerCertificate",
			"osc/Client.DeleteServerCertificate",
			"osc/Client.ReadServerCertificates",
			"osc/Client.UpdateServerCertificate"),

		// Histories and by-products of a lifecycle the emulator does not keep.
		//
		// Each answers a question about something that never happened here: a
		// boot console nothing captured (the runtime does not keep one; when it
		// learns to, this comes off the list), a stop history nothing recorded —
		// serving [] after a real StopVms would be a false answer, not an empty
		// one — and cold-migration tasks for updates this pack applies
		// immediately, so a task list would be empty by construction and prove
		// nothing.
		emulator.Because("each answers a question about something that never happened here: a console nothing captured, a stop history nothing kept, migration tasks for updates applied immediately",
			"osc/Client.ReadConsoleOutput",
			"osc/Client.ReadVmsStopHistory",
			"osc/Client.ReadVolumeUpdateTasks"),

		// Cross-account permissions on a snapshot. One implicit account, so
		// there is nobody to grant to: same reasoning as the IAM family above.
		emulator.Because("UpdateSnapshot manages cross-account permissions, and the emulator has one implicit account: there is nobody to grant to",
			"osc/Client.UpdateSnapshot"),

		// Hardware, and Outscale's own orchestration on top of it: flexible GPUs,
		// dedicated groups, VM templates and VM groups.
		//
		// GPUs and dedicated hosts are hardware the station does not have, and
		// pretending otherwise would let a client plan a placement the emulator
		// cannot honour. VM templates and groups are Outscale's own
		// orchestration above Vms — out of scope for the same reason as
		// baremetal, per docs/roadmap-outscale-iaas.md, not merely unbuilt: a
		// deferral in Declined() is the "not triaged yet" this file refuses.
		emulator.Because("GPUs and dedicated hosts are hardware this station does not have, and the templates and groups above them are proprietary orchestration, out of scope here for the same reason as baremetal",
			"osc/Client.CreateDedicatedGroup",
			"osc/Client.CreateFlexibleGpu",
			"osc/Client.CreateVmGroup",
			"osc/Client.CreateVmTemplate",
			"osc/Client.DeleteDedicatedGroup",
			"osc/Client.DeleteFlexibleGpu",
			"osc/Client.DeleteVmGroup",
			"osc/Client.DeleteVmTemplate",
			"osc/Client.LinkFlexibleGpu",
			"osc/Client.ReadDedicatedGroups",
			"osc/Client.ReadFlexibleGpus",
			"osc/Client.ReadVmGroups",
			"osc/Client.ReadVmTemplates",
			"osc/Client.ScaleDownVmGroup",
			"osc/Client.ScaleUpVmGroup",
			"osc/Client.UnlinkFlexibleGpu",
			"osc/Client.UpdateDedicatedGroup",
			"osc/Client.UpdateFlexibleGpu",
			"osc/Client.UpdateVmGroup",
			"osc/Client.UpdateVmTemplate"),

		// Outscale Kubernetes Service, a managed control plane on its own host
		// and its own API version. Standing up a Kubernetes control plane is a
		// different product from emulating machines and networks, and pretending
		// to would produce a kubeconfig pointing at nothing. One decision,
		// twenty-seven operations — listed one by one so that whatever upstream
		// adds under it is still seen.
		emulator.Because("a managed control plane on its own host and API version, which no route here mounts: answering any of it would describe a service the emulator does not run",
			"oks/Client.CreateCluster",
			"oks/Client.CreateProject",
			"oks/Client.DeleteCluster",
			"oks/Client.DeleteProject",
			"oks/Client.GetCPSubregions",
			"oks/Client.GetCluster",
			"oks/Client.GetClusterTemplate",
			"oks/Client.GetControlPlanePlans",
			"oks/Client.GetKubeconfig",
			"oks/Client.GetKubeconfigWithPubkeyNACL",
			"oks/Client.GetKubernetesVersions",
			"oks/Client.GetNetPeeringAcceptanceTemplate",
			"oks/Client.GetNetPeeringRequestTemplate",
			"oks/Client.GetNodepoolTemplate",
			"oks/Client.GetProject",
			"oks/Client.GetProjectNets",
			"oks/Client.GetProjectPublicIps",
			"oks/Client.GetProjectQuotas",
			"oks/Client.GetProjectSnapshots",
			"oks/Client.GetProjectTemplate",
			"oks/Client.GetQuotas",
			"oks/Client.ListAllClusters",
			"oks/Client.ListClustersByProjectID",
			"oks/Client.ListProjects",
			"oks/Client.UpdateCluster",
			"oks/Client.UpdateProject",
			"oks/Client.UpgradeCluster"),
	)
}
