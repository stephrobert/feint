package upstream

// Reads is what to ask each provider in order to learn its shapes.
//
// A list rather than a derivation, and the reason is worth stating: what an
// emulator needs to know is what a *populated* answer looks like, and only a
// human knows which operations their account has something to show for. A list
// generated from the route table would ask for exactly what is already served
// and learn nothing new; one generated from the untriaged column would ask for
// hundreds of operations most accounts answer empty.
//
// So this is curated, and it mixes two purposes on purpose:
//
//   - operations the packs already serve, so a recording can be diffed against
//     the emulator and reveal a field it omits — the class of defect no
//     contract can see, because the providers declare almost no required field;
//   - operations nothing serves yet, so the shape is known before the handler
//     is written rather than after a client rejects it.
//
// Every entry is a read. [Client.Read] refuses anything else anyway, but a list
// that had to be filtered would be a list somebody would eventually extend
// wrongly.
var Reads = map[Provider][]string{
	Outscale: {
		// Served today: the diff side.
		"ReadVms", "ReadNets", "ReadSubnets", "ReadVolumes", "ReadKeypairs",
		"ReadSecurityGroups", "ReadPublicIps", "ReadImages", "ReadVmTypes",
		"ReadTags", "ReadRouteTables", "ReadInternetServices", "ReadNatServices",
		"ReadSnapshots", "ReadNics", "ReadDhcpOptions", "ReadSubregions",
		"ReadVmsState", "ReadNetAccessPointServices", "ReadPublicIpRanges",
		// Not served: the learning side.
		"ReadQuotas", "ReadNetPeerings", "ReadLoadBalancers", "ReadAccounts",
		"ReadNetAccessPoints", "ReadVmsHealth", "ReadListenerRules",
		"ReadServerCertificates", "ReadVolumeUpdateTasks", "ReadRegions",
	},
	Scaleway: {
		// The zone and region are in the path for this provider, so a list
		// entry is a whole route. fr-par-1 is the zone the conformance suites
		// use, which keeps a recording comparable with what they drive.
		"/instance/v1/zones/fr-par-1/servers",
		"/instance/v1/zones/fr-par-1/ips",
		"/instance/v1/zones/fr-par-1/volumes",
		"/instance/v1/zones/fr-par-1/security_groups",
		"/instance/v1/zones/fr-par-1/images",
		"/instance/v1/zones/fr-par-1/snapshots",
		"/instance/v1/zones/fr-par-1/placement_groups",
		"/instance/v1/zones/fr-par-1/products/servers",
		"/vpc/v2/regions/fr-par/vpcs",
		"/vpc/v2/regions/fr-par/private-networks",
		"/ipam/v1/regions/fr-par/ips",
		"/iam/v1alpha1/ssh-keys",
		"/marketplace/v2/local-images",
		// block/v1 is unserved and a real client walks it — see #8. Recording
		// it is how its shape becomes known before the batch is written.
		"/block/v1/zones/fr-par-1/volumes",
		"/block/v1/zones/fr-par-1/snapshots",
	},
	Exoscale: {
		// Exoscale's API is per zone and the client is normally told which by
		// the endpoint; here the endpoint carries it, so the paths are bare.
		"/v2/instance",
		"/v2/instance-type",
		"/v2/template",
		"/v2/security-group",
		"/v2/elastic-ip",
		"/v2/ssh-key",
		"/v2/anti-affinity-group",
		"/v2/private-network",
		"/v2/block-storage-volume",
		"/v2/block-storage-snapshot",
		"/v2/snapshot",
		"/v2/deploy-target",
		"/v2/instance-pool",
		"/v2/load-balancer",
		"/v2/zone",
	},
}
