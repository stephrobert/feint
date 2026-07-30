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
		// Outscale's own inventory and billing. An emulator has one implicit
		// account, no consumption and no price list, and inventing figures
		// would be worse than answering nothing.
		emulator.Because("the emulator has one implicit account with no consumption and no price list, so anything here would be a figure it invented and somebody acted on",
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
