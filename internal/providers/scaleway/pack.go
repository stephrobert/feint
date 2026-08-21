// Package scaleway emulates the Scaleway API.
//
// Scope of this pack: the Instance product (servers), served on the real paths
// (/instance/v1/zones/{zone}/servers). Authentication is accepted and ignored:
// the X-Auth-Token header is never validated, exactly like the other local cloud
// emulators, because the point is to run without an account.
//
// Everything the pack serves is declared in Routes with its upstream operation
// name, and everything it deliberately does not serve is declared in Declined.
// The drift report subtracts both from the SDK surface, so anything new upstream
// shows up as unknown and fails CI instead of rotting silently.
package scaleway

import (
	"net/http"
	"slices"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/serialise"
)

// Name is the provider key used by the store and the coverage report.
const Name = "scaleway"

// Pack implements emulator.Pack for Scaleway.
type Pack struct {
	env *emulator.Env
}

// lockAddresses serialises address allocation, which is read-modify-write over
// the store: an allocator is rebuilt from what exists, hands out an address,
// and the result is persisted. Two requests interleaving there receive the
// same address, and Terraform creates ten resources at a time by default.
//
// One domain for every block rather than one per network: allocation is a
// handful of map operations, the contention is imperceptible, and a finer key
// buys nothing. The lock itself lives in core/serialise, the same mechanism
// every pack and the machine binding use, so the next pack does not have to
// rediscover that it needs one.
func (p *Pack) lockAddresses() func() { return serialise.Lock(Name + "/addresses") }

// lockDefaults serialises the lazy provisioning of per-project inventory (the
// default security group). The store has no compare-and-set, so without it
// two concurrent first reads of a zone each create one.
func (p *Pack) lockDefaults() func() { return serialise.Lock(Name + "/defaults") }

// New returns a Scaleway pack backed by env.
func New(env *emulator.Env) *Pack { return &Pack{env: env} }

// Name implements emulator.Pack.
func (p *Pack) Name() string { return Name }

// Routes implements emulator.Pack.
func (p *Pack) Routes() []emulator.Route {
	const zones = "/instance/v1/zones/{zone}"
	// The VPC product is regional, not zonal, and it is a different API root.
	const regions = "/vpc/v2/regions/{region}"
	const ipamRegions = "/ipam/v1/regions/{region}"
	// Block Storage is zonal like instance, and a separate API root: a client
	// reaching a volume through the Terraform provider's fallback arrives here,
	// not under /instance/v1.
	const blockZones = "/block/v1/zones/{zone}"
	// The alpha the CLI is still pinned to, measured rather than assumed: see the
	// routes below.
	const blockAlphaZones = "/block/v1alpha1/zones/{zone}"
	// The Load Balancer's zoned API and the Public Gateway's v2, both zonal.
	// Note the URL says vpc-gw where the SDK package says vpcgw.
	const lbZones = "/lb/v1/zones/{zone}"
	const gwZones = "/vpc-gw/v2/zones/{zone}"
	return []emulator.Route{
		{Method: "GET", Path: zones + "/servers", Operation: "instance/v1/API.ListServers", Handler: p.listServers},
		{Method: "POST", Path: zones + "/servers", Operation: "instance/v1/API.CreateServer", Handler: p.createServer},
		{Method: "GET", Path: zones + "/servers/{id}", Operation: "instance/v1/API.GetServer", Handler: p.getServer},
		{Method: "PATCH", Path: zones + "/servers/{id}", Operation: "instance/v1/API.UpdateServer", Handler: p.updateServer},
		{Method: "DELETE", Path: zones + "/servers/{id}", Operation: "instance/v1/API.DeleteServer", Handler: p.deleteServer},
		{Method: "POST", Path: zones + "/servers/{id}/action", Operation: "instance/v1/API.ServerAction", Handler: p.serverAction},

		// Flexible IPs: the CLI allocates one before it creates a server, so the
		// server path is not usable end to end without them.
		{Method: "GET", Path: zones + "/ips", Operation: "instance/v1/API.ListIPs", Handler: p.listIPs},
		{Method: "POST", Path: zones + "/ips", Operation: "instance/v1/API.CreateIP", Handler: p.createIP},
		{Method: "GET", Path: zones + "/ips/{id}", Operation: "instance/v1/API.GetIP", Handler: p.getIP},
		{Method: "PATCH", Path: zones + "/ips/{id}", Operation: "instance/v1/API.UpdateIP", Handler: p.updateIP},
		{Method: "DELETE", Path: zones + "/ips/{id}", Operation: "instance/v1/API.DeleteIP", Handler: p.deleteIP},

		// Security groups and their rules. The whole product is served, and the
		// rules are enforced when a machine runtime is configured: docs/limits.md
		// records the measurement. Without --vm there is no packet to filter, so
		// the control plane answers and nothing is applied.
		{Method: "GET", Path: zones + "/security_groups", Operation: "instance/v1/API.ListSecurityGroups", Handler: p.listSecurityGroups},
		{Method: "POST", Path: zones + "/security_groups", Operation: "instance/v1/API.CreateSecurityGroup", Handler: p.createSecurityGroup},
		{Method: "GET", Path: zones + "/security_groups/{id}", Operation: "instance/v1/API.GetSecurityGroup", Handler: p.getSecurityGroup},
		{Method: "PATCH", Path: zones + "/security_groups/{id}", Operation: "instance/v1/API.UpdateSecurityGroup", Handler: p.updateSecurityGroup},
		{Method: "DELETE", Path: zones + "/security_groups/{id}", Operation: "instance/v1/API.DeleteSecurityGroup", Handler: p.deleteSecurityGroup},
		{Method: "GET", Path: zones + "/security_groups/{id}/rules", Operation: "instance/v1/API.ListSecurityGroupRules", Handler: p.listSecurityGroupRules},
		{Method: "POST", Path: zones + "/security_groups/{id}/rules", Operation: "instance/v1/API.CreateSecurityGroupRule", Handler: p.createSecurityGroupRule},
		{Method: "PUT", Path: zones + "/security_groups/{id}/rules", Operation: "instance/v1/API.SetSecurityGroupRules", Handler: p.setSecurityGroupRules},
		{Method: "GET", Path: zones + "/security_groups/{id}/rules/{ruleID}", Operation: "instance/v1/API.GetSecurityGroupRule", Handler: p.getSecurityGroupRule},
		{Method: "PATCH", Path: zones + "/security_groups/{id}/rules/{ruleID}", Operation: "instance/v1/API.UpdateSecurityGroupRule", Handler: p.updateSecurityGroupRule},
		{Method: "DELETE", Path: zones + "/security_groups/{id}/rules/{ruleID}", Operation: "instance/v1/API.DeleteSecurityGroupRule", Handler: p.deleteSecurityGroupRule},

		// Placement groups: a record served, an effect this emulator cannot
		// have. Everything a driven client does with one is create, read back
		// and store — measured on the Terraform provider at 2.43.0 (the
		// surveyed terraform-talos pin) and 2.81.0 — and the one field that
		// could turn the record into a promise, policy_respected, answers the
		// single-host truth instead of the policy's wish (#285,
		// placementgroups.go). docs/limits.md carries the limit: nothing is
		// scheduled, nothing lands apart.
		{Method: "GET", Path: zones + "/placement_groups", Operation: "instance/v1/API.ListPlacementGroups", Handler: p.listPlacementGroups},
		{Method: "POST", Path: zones + "/placement_groups", Operation: "instance/v1/API.CreatePlacementGroup", Handler: p.createPlacementGroup},
		{Method: "GET", Path: zones + "/placement_groups/{id}", Operation: "instance/v1/API.GetPlacementGroup", Handler: p.getPlacementGroup},
		{Method: "PUT", Path: zones + "/placement_groups/{id}", Operation: "instance/v1/API.SetPlacementGroup", Handler: p.setPlacementGroupFull},
		{Method: "PATCH", Path: zones + "/placement_groups/{id}", Operation: "instance/v1/API.UpdatePlacementGroup", Handler: p.updatePlacementGroup},
		{Method: "DELETE", Path: zones + "/placement_groups/{id}", Operation: "instance/v1/API.DeletePlacementGroup", Handler: p.deletePlacementGroup},
		{Method: "GET", Path: zones + "/placement_groups/{id}/servers", Operation: "instance/v1/API.GetPlacementGroupServers", Handler: p.getPlacementGroupServers},
		{Method: "PUT", Path: zones + "/placement_groups/{id}/servers", Operation: "instance/v1/API.SetPlacementGroupServers", Handler: p.setPlacementGroupServers},
		{Method: "PATCH", Path: zones + "/placement_groups/{id}/servers", Operation: "instance/v1/API.UpdatePlacementGroupServers", Handler: p.updatePlacementGroupServers},

		// Private NICs: where the addressing plan becomes a real interface. The
		// address comes from the Private Network's own block, and the machine
		// driver puts the machine on the backing bridge carrying it.
		{Method: "GET", Path: zones + "/servers/{id}/private_nics", Operation: "instance/v1/API.ListPrivateNICs", Handler: p.listPrivateNICs},
		{Method: "POST", Path: zones + "/servers/{id}/private_nics", Operation: "instance/v1/API.CreatePrivateNIC", Handler: p.createPrivateNIC},
		{Method: "GET", Path: zones + "/servers/{id}/private_nics/{nicID}", Operation: "instance/v1/API.GetPrivateNIC", Handler: p.getPrivateNIC},
		{Method: "DELETE", Path: zones + "/servers/{id}/private_nics/{nicID}", Operation: "instance/v1/API.DeletePrivateNIC", Handler: p.deletePrivateNIC},

		// The same interfaces, read through instance/v2alpha1, where they are a
		// top-level resource rather than a sub-resource of the server. Terraform
		// provider 2.81.0 creates through v1 and reads through this, so a client
		// that mixes both halves is the shape to survive, not an edge case.
		// privatenics_v2alpha1.go carries the measurement and the four
		// operations deliberately left out.
		{Method: "GET", Path: privateNICsV2Path, Operation: "instance/v2alpha1/API.ListPrivateNetworkInterfaces", Handler: p.listPrivateNetworkInterfaces},
		{Method: "POST", Path: privateNICsV2Path, Operation: "instance/v2alpha1/API.CreatePrivateNetworkInterface", Handler: p.createPrivateNetworkInterface},
		{Method: "GET", Path: privateNICsV2Path + "/{id}", Operation: "instance/v2alpha1/API.GetPrivateNetworkInterface", Handler: p.getPrivateNetworkInterface},
		{Method: "PATCH", Path: privateNICsV2Path + "/{id}", Operation: "instance/v2alpha1/API.UpdatePrivateNetworkInterface", Handler: p.updatePrivateNetworkInterface,
			// The only field it changes is `tags`, and no suite here changes the
			// tags of an interface after creating it — recorded through `feint
			// proxy` across a full apply of the conformance fixture and both
			// realistic stacks. `scw instance private-nic update` reaches
			// instance/v1.UpdatePrivateNIC, which this pack does not mount, so
			// the CLI cannot drive this one either.
			//
			// Mounted rather than declined all the same, and the choice is the
			// afternoon of 17 August 2026 in one line: the Terraform resource
			// exposes tags, so the first configuration that edits one lands
			// here, and a 501 would fail an apply for a field the provider
			// believes it can change. That is the failure this whole file exists
			// to have prevented, and declining it would have scheduled the same
			// one.
			Undriven: "no client this project drives edits the tags of an interface after creating it; mounted because the Terraform resource exposes them and a 501 would break the first apply that changes one"},
		{Method: "DELETE", Path: privateNICsV2Path + "/{id}", Operation: "instance/v2alpha1/API.DeletePrivateNetworkInterface", Handler: p.deletePrivateNetworkInterface},

		// Placement groups through the alpha door. Same forcing client as the
		// interfaces above: provider 2.81.0 moved the resource's CRUD onto
		// v2alpha1 (placement_group.go at that tag) while policy_mode and
		// policy_respected stay readable only through v1, so one apply mixes
		// both halves on one resource. The list is the name lookup of the
		// provider's data source (DataSourcePlacementGroupRead calls
		// ListPlacementGroups when no ID is given).
		{Method: "GET", Path: placementGroupsV2Path, Operation: "instance/v2alpha1/API.ListPlacementGroups", Handler: p.listPlacementGroupsV2},
		{Method: "POST", Path: placementGroupsV2Path, Operation: "instance/v2alpha1/API.CreatePlacementGroup", Handler: p.createPlacementGroupV2},
		{Method: "GET", Path: placementGroupsV2Path + "/{id}", Operation: "instance/v2alpha1/API.GetPlacementGroup", Handler: p.getPlacementGroupV2},
		{Method: "PATCH", Path: placementGroupsV2Path + "/{id}", Operation: "instance/v2alpha1/API.UpdatePlacementGroup", Handler: p.updatePlacementGroupV2,
			// The conformance fixture applies once and destroys: nothing in it
			// renames a placement group between two applies, and the CLI edits
			// through v1. Mounted rather than declined for the NIC precedent's
			// reason — the Terraform resource PATCHes name, policy type and
			// tags through this door, and a 501 would fail the first apply
			// that changes one.
			Undriven: "no client this project drives edits a placement group between two applies, and the CLI edits through v1; mounted because the Terraform resource PATCHes name, policy type and tags through this door, and a 501 would fail the first apply that changes one"},
		{Method: "DELETE", Path: placementGroupsV2Path + "/{id}", Operation: "instance/v2alpha1/API.DeletePlacementGroup", Handler: p.deletePlacementGroupV2},

		// IPAM. Not a convenience: instance/v1.PrivateNIC carries no address, only
		// ipam_ip_ids, so this is the only way a client learns the address of a
		// NIC. Serving the NIC without it is serving half a product.
		//
		// The write half is SW-4: `terraform apply` with a scaleway_ipam_ip
		// failed on the first call before it, because the provider walks BookIP,
		// GetIP, UpdateIP, MoveIP, DetachIP, ReleaseIP (measured in its
		// services/ipam/ip.go), and the address it books is then carried into
		// CreatePrivateNIC as ipam_ip_ids.
		{Method: "GET", Path: ipamRegions + "/ips", Operation: "ipam/v1/API.ListIPs", Handler: p.listIPAMIPs},
		{Method: "GET", Path: ipamRegions + "/ips/{ipID}", Operation: "ipam/v1/API.GetIP", Handler: p.getIPAMIP},
		{Method: "POST", Path: ipamRegions + "/ips", Operation: "ipam/v1/API.BookIP", Handler: p.bookIP},
		{Method: "DELETE", Path: ipamRegions + "/ips/{ipID}", Operation: "ipam/v1/API.ReleaseIP", Handler: p.releaseIPAMIP},
		{Method: "POST", Path: ipamRegions + "/ip-sets/release", Operation: "ipam/v1/API.ReleaseIPSet", Handler: p.releaseIPAMIPSet},
		{Method: "PATCH", Path: ipamRegions + "/ips/{ipID}", Operation: "ipam/v1/API.UpdateIP", Handler: p.updateIPAMIP},
		// The three no client asks for, measured rather than assumed (#174):
		// `scw ipam ip` offers create, delete, get, list and update and nothing
		// else, and the Terraform provider's scaleway_ipam_ip walks BookIP,
		// GetIP and ReleaseIP on create-update-destroy — the attach and the
		// detach happen inside CreatePrivateNIC, which carries ipam_ip_ids and
		// never calls these. They stay mounted because the SDK exposes them and
		// a client written against it would find them served.
		{Method: "POST", Path: ipamRegions + "/ips/{ipID}/attach", Operation: "ipam/v1/API.AttachIP", Handler: p.attachIPAMIP,
			Undriven: "no official client calls it: `scw ipam ip` has no attach subcommand, and the Terraform provider attaches an address by passing ipam_ip_ids to CreatePrivateNIC"},
		{Method: "POST", Path: ipamRegions + "/ips/{ipID}/detach", Operation: "ipam/v1/API.DetachIP", Handler: p.detachIPAMIP,
			Undriven: "no official client calls it: the CLI has no detach subcommand, and the provider detaches by deleting the NIC that carries the address"},
		{Method: "POST", Path: ipamRegions + "/ips/{ipID}/move", Operation: "ipam/v1/API.MoveIP", Handler: p.moveIPAMIP,
			Undriven: "no official client calls it: moving a booked address between resources is an SDK call with no CLI subcommand and no Terraform attribute that would produce it"},

		// User data: how a boot script reaches a server. The value is a raw body
		// both ways, never a JSON envelope, and "cloud-init" is the key the
		// machine driver feeds to the machine it starts.
		{Method: "GET", Path: zones + "/servers/{id}/user_data", Operation: "instance/v1/API.ListServerUserData", Handler: p.listServerUserData},
		{Method: "GET", Path: zones + "/servers/{id}/user_data/{key}", Operation: "instance/v1/API.GetServerUserData", Handler: p.getServerUserData},
		{Method: "PATCH", Path: zones + "/servers/{id}/user_data/{key}", Operation: "instance/v1/API.SetServerUserData", Handler: p.setServerUserData},
		{Method: "DELETE", Path: zones + "/servers/{id}/user_data/{key}", Operation: "instance/v1/API.DeleteServerUserData", Handler: p.deleteServerUserData},

		// Volumes. A server always owns its root volume, and the Terraform
		// provider reads it back by ID right after the create: without these
		// routes the apply fails on "failed to read instance volume".
		{Method: "GET", Path: zones + "/volumes", Operation: "instance/v1/API.ListVolumes", Handler: p.listVolumes},
		{Method: "POST", Path: zones + "/volumes", Operation: "instance/v1/API.CreateVolume", Handler: p.createVolume},
		{Method: "GET", Path: zones + "/volumes/{id}", Operation: "instance/v1/API.GetVolume", Handler: p.getVolume},
		{Method: "PATCH", Path: zones + "/volumes/{id}", Operation: "instance/v1/API.UpdateVolume", Handler: p.updateVolume},
		{Method: "DELETE", Path: zones + "/volumes/{id}", Operation: "instance/v1/API.DeleteVolume", Handler: p.deleteVolume},
		// The pair `scw instance server terminate` walks before terminating:
		// every emulated server owns a root volume, so the CLI always detaches
		// first, and a 501 here failed the command outright.
		{Method: "POST", Path: zones + "/servers/{id}/attach-volume", Operation: "instance/v1/API.AttachServerVolume", Handler: p.attachServerVolume},
		{Method: "POST", Path: zones + "/servers/{id}/detach-volume", Operation: "instance/v1/API.DetachServerVolume", Handler: p.detachServerVolume},

		// Block Storage (SBS). Not a second volume product beside the first: it
		// is where the Terraform provider lands whenever a volume is not an
		// instance one, through GetUnknownVolume's fallback. With these routes
		// unmounted, an apply carrying root_volume.volume_type = "sbs_volume"
		// died on "waiting for Volume failed: http error 404 Not Found" (#8).
		{Method: "GET", Path: blockZones + "/volumes", Operation: "block/v1/API.ListVolumes", Handler: p.listBlockVolumes},
		{Method: "POST", Path: blockZones + "/volumes", Operation: "block/v1/API.CreateVolume", Handler: p.createBlockVolume},
		{Method: "GET", Path: blockZones + "/volumes/{id}", Operation: "block/v1/API.GetVolume", Handler: p.getBlockVolume},
		{Method: "PATCH", Path: blockZones + "/volumes/{id}", Operation: "block/v1/API.UpdateVolume", Handler: p.updateBlockVolume},
		{Method: "DELETE", Path: blockZones + "/volumes/{id}", Operation: "block/v1/API.DeleteVolume", Handler: p.deleteBlockVolume},
		{Method: "GET", Path: blockZones + "/snapshots", Operation: "block/v1/API.ListSnapshots", Handler: p.listBlockSnapshots},
		{Method: "POST", Path: blockZones + "/snapshots", Operation: "block/v1/API.CreateSnapshot", Handler: p.createBlockSnapshot},
		{Method: "GET", Path: blockZones + "/snapshots/{id}", Operation: "block/v1/API.GetSnapshot", Handler: p.getBlockSnapshot},
		{Method: "PATCH", Path: blockZones + "/snapshots/{id}", Operation: "block/v1/API.UpdateSnapshot", Handler: p.updateBlockSnapshot},
		{Method: "DELETE", Path: blockZones + "/snapshots/{id}", Operation: "block/v1/API.DeleteSnapshot", Handler: p.deleteBlockSnapshot},
		// The catalogue of this product, for the reason the instance one exists:
		// a client reads the stock before it creates, and gives up on a 404.
		{Method: "GET", Path: blockZones + "/volume-types", Operation: "block/v1/API.ListVolumeTypes", Handler: p.listBlockVolumeTypes,
			// The one block/v1 route with no client, and the reason is the split
			// this product lives with: `scw block volume-type list` reads the
			// alpha spelling, which is driven, and the Terraform provider never
			// reads the type catalogue at all — it sends the iops the
			// configuration names. Mounted anyway, because a client that does
			// read it must not meet a 404 before it creates anything, which is
			// the trap the Scaleway catalogue exists for.
			Undriven: "no official client reads it in v1: `scw block volume-type list` is pinned to block/v1alpha1, and the Terraform provider sends the iops the configuration declares instead of reading the catalogue"},

		// The same product under the spelling the CLI uses.
		//
		// This was declined at first, on the reading that v1 supersedes the alpha
		// and "every client the conformance suite drives calls v1". The suite said
		// otherwise on the first run: `scw block volume list` answered
		// "feint does not serve /block/v1alpha1/zones/fr-par-1/volumes". The CLI —
		// 2.56.3, the version this repository pins in CI — is on the alpha for
		// every block command, while the Terraform provider is on v1. Two official
		// clients of one cloud, each pinned to a different spelling, and the north
		// star does not let us pick a favourite.
		//
		// The handlers are shared rather than copied, because the alpha's shapes
		// are a strict subset: v1's Volume adds kms_key_id and srn, its Snapshot
		// adds public, and nothing exists in the alpha that v1 lacks. A client
		// decoding the alpha ignores the extra fields; a second implementation
		// would be a second thing to keep in step, which is what the original
		// refusal was right to fear and wrong about how to avoid.
		{Method: "GET", Path: blockAlphaZones + "/volumes", Operation: "block/v1alpha1/API.ListVolumes", Handler: p.listBlockVolumes},
		{Method: "POST", Path: blockAlphaZones + "/volumes", Operation: "block/v1alpha1/API.CreateVolume", Handler: p.createBlockVolume},
		{Method: "GET", Path: blockAlphaZones + "/volumes/{id}", Operation: "block/v1alpha1/API.GetVolume", Handler: p.getBlockVolume},
		{Method: "PATCH", Path: blockAlphaZones + "/volumes/{id}", Operation: "block/v1alpha1/API.UpdateVolume", Handler: p.updateBlockVolume},
		{Method: "DELETE", Path: blockAlphaZones + "/volumes/{id}", Operation: "block/v1alpha1/API.DeleteVolume", Handler: p.deleteBlockVolume},
		{Method: "GET", Path: blockAlphaZones + "/snapshots", Operation: "block/v1alpha1/API.ListSnapshots", Handler: p.listBlockSnapshots},
		{Method: "POST", Path: blockAlphaZones + "/snapshots", Operation: "block/v1alpha1/API.CreateSnapshot", Handler: p.createBlockSnapshot},
		{Method: "GET", Path: blockAlphaZones + "/snapshots/{id}", Operation: "block/v1alpha1/API.GetSnapshot", Handler: p.getBlockSnapshot},
		{Method: "PATCH", Path: blockAlphaZones + "/snapshots/{id}", Operation: "block/v1alpha1/API.UpdateSnapshot", Handler: p.updateBlockSnapshot},
		{Method: "DELETE", Path: blockAlphaZones + "/snapshots/{id}", Operation: "block/v1alpha1/API.DeleteSnapshot", Handler: p.deleteBlockSnapshot},
		{Method: "GET", Path: blockAlphaZones + "/volume-types", Operation: "block/v1alpha1/API.ListVolumeTypes", Handler: p.listBlockVolumeTypes},

		// VPCs and Private Networks. This is what turns a declared block into a
		// real bridge: the subnet is validated, checked against its siblings for
		// overlap, and handed to the machine driver.
		{Method: "GET", Path: regions + "/vpcs", Operation: "vpc/v2/API.ListVPCs", Handler: p.listVPCs},
		{Method: "POST", Path: regions + "/vpcs", Operation: "vpc/v2/API.CreateVPC", Handler: p.createVPC},
		{Method: "GET", Path: regions + "/vpcs/{vpc_id}", Operation: "vpc/v2/API.GetVPC", Handler: p.getVPC},
		{Method: "PATCH", Path: regions + "/vpcs/{vpc_id}", Operation: "vpc/v2/API.UpdateVPC", Handler: p.updateVPC},
		{Method: "DELETE", Path: regions + "/vpcs/{vpc_id}", Operation: "vpc/v2/API.DeleteVPC", Handler: p.deleteVPC},
		{Method: "GET", Path: regions + "/private-networks", Operation: "vpc/v2/API.ListPrivateNetworks", Handler: p.listPrivateNetworks},
		{Method: "POST", Path: regions + "/private-networks", Operation: "vpc/v2/API.CreatePrivateNetwork", Handler: p.createPrivateNetwork},
		{Method: "GET", Path: regions + "/private-networks/{pnID}", Operation: "vpc/v2/API.GetPrivateNetwork", Handler: p.getPrivateNetwork},
		{Method: "PATCH", Path: regions + "/private-networks/{pnID}", Operation: "vpc/v2/API.UpdatePrivateNetwork", Handler: p.updatePrivateNetwork},
		{Method: "DELETE", Path: regions + "/private-networks/{pnID}", Operation: "vpc/v2/API.DeletePrivateNetwork", Handler: p.deletePrivateNetwork},

		// The rest of the VPC surface, SW-4. The subnets flat (the provider
		// matches a booked address's subnet_id against them), the enable family
		// (EnableRouting is real behaviour here: the isolation the machine
		// driver enforces reconciles when it flips), and the routes the client
		// manages — records docs/limits.md owns. Two operations of this family
		// stay declined below because the portal's API document does not
		// describe them yet and every route mounted here is checked against it.
		{Method: "GET", Path: regions + "/subnets", Operation: "vpc/v2/API.ListSubnets", Handler: p.listSubnets,
			// Served because a client written against the SDK would find it, and
			// undriven because neither official client asks the flat list:
			// `scw vpc` has no subnet subcommand at all, and the Terraform
			// provider reads a network's subnets from GetPrivateNetwork, which
			// publishes them inline — that is the door the ipam fixture proves
			// (terraform.sh reads the booked address's subnet_id back through it).
			Undriven: "no official client asks for the flat list: `scw vpc` has no subnet subcommand, and the Terraform provider reads the subnets a private network publishes inline through GetPrivateNetwork"},
		{Method: "POST", Path: regions + "/vpcs/{vpc_id}/enable-routing", Operation: "vpc/v2/API.EnableRouting", Handler: p.enableRouting},
		{Method: "POST", Path: regions + "/private-networks/{pnID}/enable-dhcp", Operation: "vpc/v2/API.EnableDHCP", Handler: p.enableDHCP},
		// The Network ACL of a VPC, one rule set per address family. Served
		// since #343 and declined before it, on a reason written from the SDK's
		// shape rather than from a measurement: `scw vpc rule get` calls the
		// first of these and took a 501, the official Terraform provider 2.81.0
		// ships `scaleway_vpc_acl`, and a real third-party module declares that
		// resource. acl.go carries the recording, the ranking and what is and is
		// not enforced.
		{Method: "GET", Path: regions + "/vpcs/{vpc_id}/acl-rules", Operation: "vpc/v2/API.GetACL", Handler: p.getACL},
		{Method: "PUT", Path: regions + "/vpcs/{vpc_id}/acl-rules", Operation: "vpc/v2/API.SetACL", Handler: p.setACL},
		{Method: "POST", Path: regions + "/routes", Operation: "vpc/v2/API.CreateRoute", Handler: p.createRoute},
		{Method: "GET", Path: regions + "/routes/{routeID}", Operation: "vpc/v2/API.GetRoute", Handler: p.getRoute},
		{Method: "PATCH", Path: regions + "/routes/{routeID}", Operation: "vpc/v2/API.UpdateRoute", Handler: p.updateRoute},
		{Method: "DELETE", Path: regions + "/routes/{routeID}", Operation: "vpc/v2/API.DeleteRoute", Handler: p.deleteRoute},

		// Snapshots and images the client makes. The golden-image sequence —
		// snapshot a volume, cut an image from it, boot a server from it — is a
		// control-plane path answerable at every step, and Outscale has answered
		// it since 0.6.0 while this pack declined it (SW-2).
		{Method: "GET", Path: zones + "/snapshots", Operation: "instance/v1/API.ListSnapshots", Handler: p.listSnapshots},
		{Method: "POST", Path: zones + "/snapshots", Operation: "instance/v1/API.CreateSnapshot", Handler: p.createSnapshot},
		{Method: "GET", Path: zones + "/snapshots/{id}", Operation: "instance/v1/API.GetSnapshot", Handler: p.getSnapshot},
		{Method: "PATCH", Path: zones + "/snapshots/{id}", Operation: "instance/v1/API.UpdateSnapshot", Handler: p.updateSnapshot},
		{Method: "DELETE", Path: zones + "/snapshots/{id}", Operation: "instance/v1/API.DeleteSnapshot", Handler: p.deleteSnapshot},
		{Method: "POST", Path: zones + "/images", Operation: "instance/v1/API.CreateImage", Handler: p.createImage},
		{Method: "PATCH", Path: zones + "/images/{id}", Operation: "instance/v1/API.UpdateImage", Handler: p.updateImage},
		{Method: "DELETE", Path: zones + "/images/{id}", Operation: "instance/v1/API.DeleteImage", Handler: p.deleteImage},

		// The catalogue: not inventory the emulator owns, but the CLI reads it
		// before it creates anything and gives up on a 404. ListImages answers
		// the client's images beside it, which is why it sits with the reads
		// rather than with the block above.
		{Method: "GET", Path: zones + "/products/servers", Operation: "instance/v1/API.ListServersTypes", Handler: p.listServerTypes},
		{Method: "GET", Path: zones + "/images", Operation: "instance/v1/API.ListImages", Handler: p.listImages},
		{Method: "GET", Path: zones + "/images/{id}", Operation: "instance/v1/API.GetImage", Handler: p.getImage},
		{Method: "GET", Path: "/marketplace/v2/local-images", Operation: "marketplace/v2/API.ListLocalImages", Handler: p.listLocalImages},

		// Load Balancers (lb/v1, ZonedAPI), SW-5 scoped by the #262 survey:
		// what kubic, terraform-talos and Scaleway's own LB module call, plus
		// the lists `scw lb` reads. The measurement and the shapes live in
		// loadbalancer.go; the rest of the family is declined below by name.
		{Method: "GET", Path: lbZones + "/lbs", Operation: "lb/v1/ZonedAPI.ListLBs", Handler: p.listLBs},
		{Method: "POST", Path: lbZones + "/lbs", Operation: "lb/v1/ZonedAPI.CreateLB", Handler: p.createLB},
		{Method: "GET", Path: lbZones + "/lbs/{lbID}", Operation: "lb/v1/ZonedAPI.GetLB", Handler: p.getLB},
		{Method: "PUT", Path: lbZones + "/lbs/{lbID}", Operation: "lb/v1/ZonedAPI.UpdateLB", Handler: p.updateLB},
		{Method: "DELETE", Path: lbZones + "/lbs/{lbID}", Operation: "lb/v1/ZonedAPI.DeleteLB", Handler: p.deleteLB},

		// The balancer's flexible IPs. This is the exact route the #74
		// OpenTofu recording died on with a plain-text 404, and the whole of
		// kubic's measured demand: one `scaleway_lb_ip` for its ingress.
		{Method: "GET", Path: lbZones + "/ips", Operation: "lb/v1/ZonedAPI.ListIPs", Handler: p.listLBIPs},
		{Method: "POST", Path: lbZones + "/ips", Operation: "lb/v1/ZonedAPI.CreateIP", Handler: p.createLBIP},
		{Method: "GET", Path: lbZones + "/ips/{ipID}", Operation: "lb/v1/ZonedAPI.GetIP", Handler: p.getLBIP},
		{Method: "PATCH", Path: lbZones + "/ips/{ipID}", Operation: "lb/v1/ZonedAPI.UpdateIP", Handler: p.updateLBIP},
		{Method: "DELETE", Path: lbZones + "/ips/{ipID}", Operation: "lb/v1/ZonedAPI.ReleaseIP", Handler: p.releaseLBIP},

		{Method: "GET", Path: lbZones + "/lbs/{lbID}/backends", Operation: "lb/v1/ZonedAPI.ListBackends", Handler: p.listLBBackends},
		{Method: "POST", Path: lbZones + "/lbs/{lbID}/backends", Operation: "lb/v1/ZonedAPI.CreateBackend", Handler: p.createLBBackend},
		{Method: "GET", Path: lbZones + "/backends/{backendID}", Operation: "lb/v1/ZonedAPI.GetBackend", Handler: p.getLBBackend},
		{Method: "PUT", Path: lbZones + "/backends/{backendID}", Operation: "lb/v1/ZonedAPI.UpdateBackend", Handler: p.updateLBBackend},
		{Method: "DELETE", Path: lbZones + "/backends/{backendID}", Operation: "lb/v1/ZonedAPI.DeleteBackend", Handler: p.deleteLBBackend},
		{Method: "PUT", Path: lbZones + "/backends/{backendID}/servers", Operation: "lb/v1/ZonedAPI.SetBackendServers", Handler: p.setLBBackendServers},
		{Method: "PUT", Path: lbZones + "/backends/{backendID}/healthcheck", Operation: "lb/v1/ZonedAPI.UpdateHealthCheck", Handler: p.updateLBHealthCheck},

		{Method: "GET", Path: lbZones + "/lbs/{lbID}/frontends", Operation: "lb/v1/ZonedAPI.ListFrontends", Handler: p.listLBFrontends},
		{Method: "POST", Path: lbZones + "/lbs/{lbID}/frontends", Operation: "lb/v1/ZonedAPI.CreateFrontend", Handler: p.createLBFrontend},
		{Method: "GET", Path: lbZones + "/frontends/{frontendID}", Operation: "lb/v1/ZonedAPI.GetFrontend", Handler: p.getLBFrontend},
		{Method: "PUT", Path: lbZones + "/frontends/{frontendID}", Operation: "lb/v1/ZonedAPI.UpdateFrontend", Handler: p.updateLBFrontend},
		{Method: "DELETE", Path: lbZones + "/frontends/{frontendID}", Operation: "lb/v1/ZonedAPI.DeleteFrontend", Handler: p.deleteLBFrontend},

		// The ACLs a frontend carries inline in Terraform: the provider
		// reconciles them one by one (CreateACL/ListACLs/UpdateACL/DeleteACL,
		// measured in its services/lb/frontend.go), never through SetACLs,
		// which stays declined.
		{Method: "GET", Path: lbZones + "/frontends/{frontendID}/acls", Operation: "lb/v1/ZonedAPI.ListACLs", Handler: p.listLBACLs},
		{Method: "POST", Path: lbZones + "/frontends/{frontendID}/acls", Operation: "lb/v1/ZonedAPI.CreateACL", Handler: p.createLBACL},
		{Method: "GET", Path: lbZones + "/acls/{aclID}", Operation: "lb/v1/ZonedAPI.GetACL", Handler: p.getLBACL},
		{Method: "PUT", Path: lbZones + "/acls/{aclID}", Operation: "lb/v1/ZonedAPI.UpdateACL", Handler: p.updateLBACL},
		{Method: "DELETE", Path: lbZones + "/acls/{aclID}", Operation: "lb/v1/ZonedAPI.DeleteACL", Handler: p.deleteLBACL},

		// Routes, for the official LB module's `scaleway_lb_route`.
		{Method: "GET", Path: lbZones + "/routes", Operation: "lb/v1/ZonedAPI.ListRoutes", Handler: p.listLBRoutes},
		{Method: "POST", Path: lbZones + "/routes", Operation: "lb/v1/ZonedAPI.CreateRoute", Handler: p.createLBRoute},
		{Method: "GET", Path: lbZones + "/routes/{routeID}", Operation: "lb/v1/ZonedAPI.GetRoute", Handler: p.getLBRoute},
		{Method: "PUT", Path: lbZones + "/routes/{routeID}", Operation: "lb/v1/ZonedAPI.UpdateRoute", Handler: p.updateLBRoute},
		{Method: "DELETE", Path: lbZones + "/routes/{routeID}", Operation: "lb/v1/ZonedAPI.DeleteRoute", Handler: p.deleteLBRoute},

		// The Private Network attachment, which is where the balancer meets
		// the addressing plan: an attach without ipam_ids books an address
		// from the network's own pool, with ipam_ids it holds the booked one,
		// and the provider reads both back through /ipam/v1 ListIPs filtered
		// by resource_type=lb_server.
		{Method: "GET", Path: lbZones + "/lbs/{lbID}/private-networks", Operation: "lb/v1/ZonedAPI.ListLBPrivateNetworks", Handler: p.listLBPrivateNetworks},
		{Method: "POST", Path: lbZones + "/lbs/{lbID}/attach-private-network", Operation: "lb/v1/ZonedAPI.AttachPrivateNetwork", Handler: p.attachLBPrivateNetwork},
		{Method: "POST", Path: lbZones + "/lbs/{lbID}/detach-private-network", Operation: "lb/v1/ZonedAPI.DetachPrivateNetwork", Handler: p.detachLBPrivateNetwork},
		// The same two operations at the spelling SDK generations up to
		// v1.0.0-beta.29 emit, with the network in the path. Not a guess: the
		// surveyed terraform-talos stack pins provider ~>2.43.0, and its
		// apply died here on a 501 while its vendored SDK's code reads the
		// current spelling — the wire overturned the code reading, the exact
		// LBU lesson (Link… vs Register…) replayed on Scaleway.
		{Method: "POST", Path: lbZones + "/lbs/{lbID}/private-networks/{pnID}/attach", Operation: "lb/v1/ZonedAPI.AttachPrivateNetwork", Handler: p.attachLBPrivateNetworkLegacy,
			Legacy: "terraform-provider-scaleway v2.43.0 (SDK v1.0.0-beta.29) sends the network in the path; measured with feint proxy --record under the surveyed terraform-talos stack"},
		{Method: "POST", Path: lbZones + "/lbs/{lbID}/private-networks/{pnID}/detach", Operation: "lb/v1/ZonedAPI.DetachPrivateNetwork", Handler: p.detachLBPrivateNetworkLegacy,
			Legacy: "the detach of the same SDK generation as the legacy attach above"},

		// Public Gateways (vpc-gw/v2), SW-6 scoped the same way: what
		// terraform-talos and Scaleway's own VPC module walk —
		// gateway IP → gateway → gateway network with its IPAM config — plus
		// the lists `scw vpc-gw` reads (the CLI drives v2 since at least
		// 2.56.3, measured with -D). gateways.go carries the shapes and the
		// reason only v2 is served.
		{Method: "GET", Path: gwZones + "/gateways", Operation: "vpcgw/v2/API.ListGateways", Handler: p.listGateways},
		{Method: "POST", Path: gwZones + "/gateways", Operation: "vpcgw/v2/API.CreateGateway", Handler: p.createGateway},
		{Method: "GET", Path: gwZones + "/gateways/{gatewayID}", Operation: "vpcgw/v2/API.GetGateway", Handler: p.getGateway},
		{Method: "PATCH", Path: gwZones + "/gateways/{gatewayID}", Operation: "vpcgw/v2/API.UpdateGateway", Handler: p.updateGateway},
		{Method: "DELETE", Path: gwZones + "/gateways/{gatewayID}", Operation: "vpcgw/v2/API.DeleteGateway", Handler: p.deleteGateway},

		{Method: "GET", Path: gwZones + "/gateway-networks", Operation: "vpcgw/v2/API.ListGatewayNetworks", Handler: p.listGatewayNetworks},
		{Method: "POST", Path: gwZones + "/gateway-networks", Operation: "vpcgw/v2/API.CreateGatewayNetwork", Handler: p.createGatewayNetwork},
		{Method: "GET", Path: gwZones + "/gateway-networks/{gatewayNetworkID}", Operation: "vpcgw/v2/API.GetGatewayNetwork", Handler: p.getGatewayNetwork},
		{Method: "PATCH", Path: gwZones + "/gateway-networks/{gatewayNetworkID}", Operation: "vpcgw/v2/API.UpdateGatewayNetwork", Handler: p.updateGatewayNetwork},
		{Method: "DELETE", Path: gwZones + "/gateway-networks/{gatewayNetworkID}", Operation: "vpcgw/v2/API.DeleteGatewayNetwork", Handler: p.deleteGatewayNetwork},

		{Method: "GET", Path: gwZones + "/ips", Operation: "vpcgw/v2/API.ListIPs", Handler: p.listGatewayIPs},
		{Method: "POST", Path: gwZones + "/ips", Operation: "vpcgw/v2/API.CreateIP", Handler: p.createGatewayIP},
		{Method: "GET", Path: gwZones + "/ips/{ipID}", Operation: "vpcgw/v2/API.GetIP", Handler: p.getGatewayIP},
		{Method: "PATCH", Path: gwZones + "/ips/{ipID}", Operation: "vpcgw/v2/API.UpdateIP", Handler: p.updateGatewayIP},
		{Method: "DELETE", Path: gwZones + "/ips/{ipID}", Operation: "vpcgw/v2/API.DeleteIP", Handler: p.deleteGatewayIP},

		// IAM SSH keys. Scaleway attaches keys to the project, not to the server,
		// so every key of the project is injected into every machine it boots.
		// Without this product a server can run but nobody can log into it.
		{Method: "GET", Path: "/iam/v1alpha1/ssh-keys", Operation: "iam/v1alpha1/API.ListSSHKeys", Handler: p.listSSHKeys},
		{Method: "POST", Path: "/iam/v1alpha1/ssh-keys", Operation: "iam/v1alpha1/API.CreateSSHKey", Handler: p.createSSHKey},
		{Method: "GET", Path: "/iam/v1alpha1/ssh-keys/{id}", Operation: "iam/v1alpha1/API.GetSSHKey", Handler: p.getSSHKey},
		{Method: "PATCH", Path: "/iam/v1alpha1/ssh-keys/{id}", Operation: "iam/v1alpha1/API.UpdateSSHKey", Handler: p.updateSSHKey},
		{Method: "DELETE", Path: "/iam/v1alpha1/ssh-keys/{id}", Operation: "iam/v1alpha1/API.DeleteSSHKey", Handler: p.deleteSSHKey},
	}
}

// productPrefixes is Scaleway's URL space, which is wider than the part this
// pack serves.
//
// It decides one thing: which requests get a Scaleway error envelope instead of
// net/http's `404 page not found` in text/plain. The SDK reads the content type
// first and drops a body that is not JSON, so a client meeting an unserved
// product used to get "404 Not Found" and nothing else — reported by @vde-dis
// on #74, measured on `/lb/v1/zones/fr-par-1/ips` and `/vpc-gw/v2/zones/...`.
//
// It listed only the five served products before, and its own comment claimed
// TestEveryRouteFallsUnderADeclaredPrefix stopped "a whole product answering
// net/http's plain text". That test cannot: it walks the routes this pack
// mounts, and a product with no routes at all is invisible to it by
// construction. A guard that can only see the served half was standing for the
// unserved half.
//
// The list is extracted from the SDK's own generated request paths rather than
// from its directory names, which differ — the SDK says `vpcgw`, the URL says
// `/vpc-gw/`. Snapshot of the SDK on 2026-08-11, refreshed with:
//
//	grep -rhoE 'Path: +"/[a-z0-9-]+/v[0-9a-z]+/' .upstream/scaleway-sdk-go/api |
//	  sed -E 's/.*"//' | sort -u
//
// Nothing regenerates it, and that is a smaller risk than it looks: a product
// Scaleway adds later and this list misses answers exactly as it does today,
// in plain text. Falling behind costs the improvement, never a regression.
var productPrefixes = []string{
	// Served here.
	"/instance/v1/",
	// Partly: the private network interfaces of provider 2.81.0 and nothing
	// else. It sits in this half because what this list decides is the error
	// envelope, and every v2alpha1 path now has a served neighbour.
	"/instance/v2alpha1/",
	"/vpc/v2/",
	"/ipam/v1/",
	"/iam/v1alpha1/",
	"/marketplace/v2/",
	// Since #282: the balancer's zoned API and the gateway's v2, the two
	// products the #74 report measured answering in plain text.
	"/lb/v1/",
	"/vpc-gw/v2/",

	// Published by Scaleway and not served here. They are declared so that a
	// client reaching one gets a Scaleway error envelope rather than net/http's
	// plain text, which is all this list decides.
	"/account/v3/",
	"/annotations/v1/",
	"/apple-silicon/v1alpha1/",
	"/audit-trail/v1alpha1/",
	"/autoscaling/v1alpha1/",
	"/autoscaling/v1alpha2/",
	"/baremetal/v1/",
	"/baremetal/v3/",
	"/billing/v2/",
	"/billing/v2beta1/",
	"/block/v1/",
	"/block/v1alpha1/",
	"/cockpit/v1/",
	"/containers/v1/",
	"/containers/v1beta1/",
	"/datalab/v1beta1/",
	"/datawarehouse/v1beta1/",
	"/dedibox/v1/",
	"/document-db/v1beta1/",
	"/domain/v2beta1/",
	"/edge-services/v1beta1/",
	"/environmental-footprint/v1alpha1/",
	"/file/v1alpha1/",
	"/flexible-ip/v1alpha1/",
	"/functions/v1beta1/",
	"/inference/v1/",
	"/interlink/v1beta1/",
	"/iot/v1/",
	"/ipam/v1alpha1/",
	"/k8s/v1/",
	"/kafka/v1alpha1/",
	"/key-manager/v1alpha1/",
	"/mailbox/v1alpha1/",
	"/messageq/v1alpha1/",
	"/mnq/v1beta1/",
	"/mongodb/v1/",
	"/mongodb/v1alpha1/",
	"/partner/v1/",
	"/product-catalog/v2alpha1/",
	"/qaas/v1alpha1/",
	"/rdb/v1/",
	"/redis/v1/",
	"/registry/v1/",
	"/s2s-vpn/v1alpha1/",
	"/search/v1alpha1/",
	"/searchdb/v1alpha1/",
	"/secret-manager/v1beta1/",
	"/serverless-jobs/v1alpha1/",
	"/serverless-jobs/v1alpha2/",
	"/serverless-sqldb/v1alpha1/",
	"/test/v1/",
	"/transactional-email/v1alpha1/",
	"/vpc-gw/v1/",
	"/webhosting/v1/",
}

// Prefixes implements emulator.Unrouted.
func (p *Pack) Prefixes() []string { return productPrefixes }

// NotFound implements emulator.Unrouted.
func (p *Pack) NotFound(w http.ResponseWriter, r *http.Request) {
	writeNotEmulated(w, r.URL.Path)
}

// Declined implements emulator.Pack.
//
// These operations exist upstream and will not be emulated as they are. Every
// group says why, because "out of scope" and "not triaged yet" are different
// answers and only the first belongs here. What stays unknown in the coverage
// report is work this project intends to do, which is what makes that report
// a list somebody can act on rather than a number nobody reads.
func (p *Pack) Declined() []emulator.Decline {
	return slices.Concat(
		// SW-1's second half: the instance, vpc and ipam columns the gate showed
		// as untriaged. Every block below says what the emulator would have to
		// have in order to answer, which is what makes a refusal revisitable —
		// "not triaged yet" and "out of scope" are different answers.

		// Snapshots and images used to be declined here, on the argument that a
		// snapshot copies bytes an emulated volume does not have, and that the
		// image catalogue is a fixed table nothing can grow. Both sentences were
		// true and neither was a reason: what a client does with them is a
		// control-plane sequence, and Outscale served exactly that from 0.6.0
		// while this pack refused it. Served since SW-2, with the bytes named as
		// the limit they are — an image cut here boots nothing, and says so,
		// rather than substituting a distribution nobody asked for (#115).
		//
		// One member of the family stays declined and keeps its own entry below:
		// ExportSnapshot writes bytes into Object Storage, which is the part
		// this emulator really cannot answer.

		// Placement groups were declined here until #285, with the reason
		// "any policy would be reported satisfied whatever it asked". The
		// sentence about the single host stays true, and it stopped being a
		// reason once measured against what a client does with the group:
		// the Terraform provider stores policy_respected and never gates on
		// it, exactly the security-group profile this pack already serves.
		// The risk the old reason named is now carried by the field itself —
		// placementPolicyRespected answers the single-host truth, so a spread
		// policy with two running members reads false rather than satisfied.

		emulator.Because("it mounts Scaleway's File Storage product, and there is no filesystem service behind this emulator for a machine to mount",
			"instance/v1/API.AttachServerFileSystem",
			"instance/v1/API.DetachServerFileSystem"),

		// Block Storage, the two halves SW-3 does not serve.
		//
		// The transfers write and read a snapshot's bytes through Object Storage,
		// which is the same measured reason instance/v1/API.ExportSnapshot carries:
		// the Terraform provider builds the S3 endpoint from the region in code,
		// so pointing it here would take DNS interception and a certificate it
		// accepts. An emulated snapshot has no bytes to send there in any case.
		emulator.Because("it moves a snapshot's bytes through Object Storage, which is not emulated because the Terraform provider builds the S3 endpoint in code: supporting it needs DNS interception and a certificate, measured in docs/limits.md",
			"block/v1/API.ExportSnapshotToObjectStorage",
			"block/v1/API.ImportSnapshotFromObjectStorage"),

		// The alpha's own transfers, declined for the reason its v1 twins are: they
		// move bytes through Object Storage. ImportSnapshotFromS3 is the older
		// spelling of the same call and goes with them.
		//
		// The rest of block/v1alpha1 is served, not declined, and the first
		// version of this entry declined the lot with a reason the conformance
		// suite falsified on its first run — "every client calls v1", while `scw`
		// 2.56.3 calls the alpha for every block command. The routes above carry
		// what replaced it.
		emulator.Because("it moves a snapshot's bytes through Object Storage, which is not emulated because the Terraform provider builds the S3 endpoint in code: supporting it needs DNS interception and a certificate, measured in docs/limits.md",
			"block/v1alpha1/API.ExportSnapshotToObjectStorage",
			"block/v1alpha1/API.ImportSnapshotFromObjectStorage",
			"block/v1alpha1/API.ImportSnapshotFromS3"),

		// Written by hand in instance_utils.go and marked deprecated there, which
		// is why the scan sees them at all: it reads every non-test file rather
		// than the generated ones only.
		emulator.Because("the SDK's hand-written helpers, deprecated upstream in favour of AttachServerVolume and DetachServerVolume, which this pack serves and which the CLI calls",
			"instance/v1/API.AttachVolume",
			"instance/v1/API.DetachVolume"),

		emulator.Because("the server already publishes allowed_actions, derived from its state, so a second listing would be a second place to keep in step with the first",
			"instance/v1/API.ListServerActions"),

		emulator.Because("its request carries tags and nothing else, and the pack stores no tag on a private NIC, so it would answer success over a field nothing reads back",
			"instance/v1/API.UpdatePrivateNIC"),

		// The one member of the IPAM family still declined. The lifecycle
		// itself — BookIP, ReleaseIP, ReleaseIPSet, UpdateIP, AttachIP,
		// DetachIP, MoveIP — is served since SW-4: the old reason here said a
		// booked address would be one "no runtime configures", which stopped
		// being true the day a booked address could be carried into
		// CreatePrivateNIC and become the NIC's own.
		emulator.Because("it hands an instance flexible IP over to IPAM's pool, and the public addresses of this emulator live and die with the instance product: IPAM here holds private-network addresses only",
			"instance/v1/API.ReleaseIPToIpam"),

		// The ingress rules, and what the measurement did to the reason they
		// share with the two ACL operations that used to sit here.
		//
		// The reason was "no runtime mode enforces a rule at the VPC edge yet,
		// and a filter recorded but never applied is indistinguishable from
		// protection". Half of it is still true and half of it was never
		// measured. What #343 measured, on 2026-08-21, is which of these seven
		// a real client calls:
		//
		//   - `vpc/v2/API.GetACL` and `vpc/v2/API.SetACL` are called. `scw vpc
		//     rule get/set` addresses `/vpcs/{id}/acl-rules` and took a 501
		//     here, recorded and ranked by `feint coverage --observed`; the
		//     official Terraform provider ships `scaleway_vpc_acl`; and
		//     tf-scaleway-modules/terraform-scaleway-network @ 99f390bb
		//     declares that resource in its own `complete` example. They are
		//     served now, as records, with the non-enforcement stated where a
		//     reader meets it (acl.go, docs/limits.md) rather than as a 501.
		//   - **the five below show zero observed calls.** `scw` has no
		//     ingress-rule subcommand, and no surveyed stack names
		//     `scaleway_vpc_ingress_rule`. Nothing is asking, so nothing is
		//     served, and this line is where that is written down.
		//
		// The distinction matters: an ACL is the whole filter of a VPC and a
		// client reads it back on every plan, while an ingress rule is an
		// object with its own lifecycle that nothing here has ever been asked
		// to create.
		emulator.Because("no runtime mode enforces a rule at the VPC edge, and no recorded client calls this one: `scw` has no ingress-rule subcommand and no surveyed stack names scaleway_vpc_ingress_rule, so it is a refusal nobody has met",
			"vpc/v2/API.CreateIngressRule",
			"vpc/v2/API.DeleteIngressRule",
			"vpc/v2/API.GetIngressRule",
			"vpc/v2/API.ListIngressRules",
			"vpc/v2/API.UpdateIngressRule"),

		// Peering is the exact inverse of the property this project measures.
		//
		// And unlike the ingress rules, the demand here is measured rather than
		// assumed absent: `scw vpc vpc-connector list` and `... create` both
		// reached `/vpc/v2/regions/fr-par/vpc-connectors` and took a 501 on
		// 2026-08-21 (#343). The refusal stands anyway, and the difference is
		// worth stating — this one is declined despite the demand, because
		// answering it would report done the one thing the bridge mode cannot
		// do. It is a capability that has to arrive under OVN, not a CRUD that
		// nobody has asked for.
		emulator.Because("it peers two VPCs, and isolation between two VPCs is the one property the bridge mode cannot deliver: joining them would report done what was never apart, and a recorded client calling it does not change that",
			"vpc/v2/API.CreateVPCConnector",
			"vpc/v2/API.DeleteVPCConnector",
			"vpc/v2/API.GetVPCConnector",
			"vpc/v2/API.ListVPCConnectors",
			"vpc/v2/API.UpdateVPCConnector"),

		// EnableDHCP, ListSubnets and the routes were declined here until SW-4
		// and are served now; their old reasons ("no DHCP server to enable",
		// "the subnets are on the network itself") described real facts but
		// blocked real clients, which is the wrong trade.

		emulator.Because("its request names a VPC connector and nothing else — it compares the subnets across a peering — and the connectors are declined below until OVN mode has measured peering",
			"vpc/v2/API.ListSubnetOverlaps"),

		// Two operations the Go SDK carries and the portal's own API document
		// does not (verified against a fresh download of vpc/v2/schema.yml,
		// 2026-08-13: 35 operationIds, neither among them). Every route mounted
		// here is checked against that document — TestEveryRouteMatchesItsContract
		// in internal/core/emulator and the probe both read
		// contracts/scaleway.json — so serving them would mean mounting
		// operations no contract can check. The drift scan keeps them visible;
		// they are served the day the document catches up.
		emulator.Because("the portal's API document does not describe it yet, and every route mounted here is checked against that document, so it cannot be served until the document catches up with the SDK",
			"vpc/v2/API.EnableCustomRoutesPropagation",
			"vpc/v2/RoutesWithNexthopAPI.ListRoutesWithNexthop"),

		emulator.Because("the CLI resolves its default image through ListLocalImages, which is served, so a per-id lookup would be a second door onto the same fixed table",
			"marketplace/v2/API.GetLocalImage"),

		// The provider's fleet: what types are available, what a quota allows,
		// what a migration would be permitted. A local emulator has none of it
		// and cannot invent headroom without telling a client something false
		// about capacity. The dashboard used to be listed here and is not, for a
		// reason the next block gives.
		emulator.Because("capacity and quotas are the provider's fleet, and a local emulator that answered would be inventing headroom a client could plan against",
			"instance/v1/API.GetServerTypesAvailability",
			"instance/v1/API.GetServerCompatibleTypes",
			"instance/v1/API.CheckBlockMigrationOrganizationQuotas"),

		// GetDashboard is separated from the group above, because "no inventory"
		// was false about it. The thirteen counters it returns — servers,
		// volumes, images, snapshots, IPs, security groups, placement groups —
		// are the tenant's own resources, and the store holds several of them.
		// An audit checked the SDK struct and was right. The reason it is
		// declined is the other half: it also counts products this pack does not
		// serve, so every total would be short by the unemulated remainder with
		// nothing in the answer saying which.
		// ListVolumesTypes is not in the block above, and an audit was right to
		// say so: it returns a catalogue of volume types with their constraints,
		// which is the same nature as ListServersTypes — served. It is declined
		// for the reason that actually applies to it.
		emulator.Because("the emulator serves one volume type, b_ssd, because that is what its catalogue attaches, so a type list would describe capabilities nothing here can create",
			"instance/v1/API.ListVolumesTypes"),

		emulator.Because("its thirteen counters span resources this pack does not serve, so every total would be short by the unemulated remainder with nothing saying which",
			"instance/v1/API.GetDashboard"),

		// The rule set Scaleway seeds a new security group with is a value, and
		// the SDK carries shapes. Serving an invented list would tell a client
		// which ports are open on a runtime that filters nothing. Trade it for
		// real values the day someone measures them against the real API.
		emulator.Because("the seeded rule set is a value the SDK does not carry, so an invented list would state which ports a real client believes are open, and docs/limits.md records that these rules do filter packets",
			"instance/v1/API.ListDefaultSecurityGroupRules"),

		// Migrating a legacy local volume, or a snapshot of one, to Scaleway
		// Block Storage. Every volume served here is already of the current
		// kind, so a plan would list nothing and applying it would confirm a
		// move that never happened.
		emulator.Because("there is no legacy storage behind this emulator to migrate from, so a plan would describe a move between two things that are the same store",
			"instance/v1/API.PlanBlockMigration",
			"instance/v1/API.ApplyBlockMigration"),

		// Writes a snapshot into an Object Storage bucket, and Object Storage
		// is not emulated: the Terraform provider builds the S3 endpoint from
		// the region in code, so pointing it here would take DNS interception
		// and a certificate it accepts. The measurement is in docs/limits.md.
		emulator.Because("it writes into Object Storage, which is not emulated because the Terraform provider builds the S3 endpoint in code: supporting it needs DNS interception and a certificate, measured in docs/limits.md",
			"instance/v1/API.ExportSnapshot"),

		// The metadata service answers on the link-local address
		// 169.254.42.42, from inside the machine, to a caller that carries no
		// credentials. It is not the same surface as the rest of instance/v1
		// and serving it would mean an HTTP listener inside every emulated
		// machine. User data reaches the guest through the runtime instead,
		// which is what cloud-init reads.
		emulator.Because("the metadata service answers on the link-local address 169.254.42.42, from inside the machine, to a caller that carries no credentials",
			"instance/v1/MetadataAPI.GetMetadata",
			"instance/v1/MetadataAPI.GetUserData",
			"instance/v1/MetadataAPI.ListUserData",
			"instance/v1/MetadataAPI.SetUserData",
			"instance/v1/MetadataAPI.DeleteUserData"),

		// IAM, everything except the SSH keys the pack serves.
		//
		// Users, groups, applications, policies, API keys, JWTs, SAML, SCIM,
		// WebAuthn, MFA, the security settings and the audit log. The emulator
		// accepts every credential on purpose — SECURITY.md states it and
		// docs/roadmap.md lists verifying signatures under Not planned — so an
		// access control served here would describe rules nothing enforces: a
		// client could read back a policy denying an action and watch the
		// emulator perform it.
		//
		// SSH keys are the exception and stay served, because they are not access
		// control: they are the public key a machine boots with, and cloud-init
		// installs it for real when --vm is on.
		emulator.Because("the emulator accepts every credential on purpose, so serving users, policies and keys would describe an access control that nothing here enforces",
			"iam/v1alpha1/API.AddGroupMember",
			"iam/v1alpha1/API.AddGroupMembers",
			"iam/v1alpha1/API.AddSamlCertificate",
			"iam/v1alpha1/API.ClonePolicy",
			"iam/v1alpha1/API.CreateAPIKey",
			"iam/v1alpha1/API.CreateApplication",
			"iam/v1alpha1/API.CreateGroup",
			"iam/v1alpha1/API.CreateJWT",
			"iam/v1alpha1/API.CreatePolicy",
			"iam/v1alpha1/API.CreateScimToken",
			"iam/v1alpha1/API.CreateUser",
			"iam/v1alpha1/API.CreateUserMFAOTP",
			"iam/v1alpha1/API.DeleteAPIKey",
			"iam/v1alpha1/API.DeleteApplication",
			"iam/v1alpha1/API.DeleteGroup",
			"iam/v1alpha1/API.DeleteJWT",
			"iam/v1alpha1/API.DeletePolicy",
			"iam/v1alpha1/API.DeleteSaml",
			"iam/v1alpha1/API.DeleteSamlCertificate",
			"iam/v1alpha1/API.DeleteScim",
			"iam/v1alpha1/API.DeleteScimToken",
			"iam/v1alpha1/API.DeleteUser",
			"iam/v1alpha1/API.DeleteUserMFAOTP",
			"iam/v1alpha1/API.DeleteWebAuthnAuthenticator",
			"iam/v1alpha1/API.EnableOrganizationSaml",
			"iam/v1alpha1/API.EnableOrganizationScim",
			"iam/v1alpha1/API.FinishUserWebAuthnRegistration",
			"iam/v1alpha1/API.GetAPIKey",
			"iam/v1alpha1/API.GetApplication",
			"iam/v1alpha1/API.GetGroup",
			"iam/v1alpha1/API.GetJWT",
			"iam/v1alpha1/API.GetLog",
			"iam/v1alpha1/API.GetOrganization",
			"iam/v1alpha1/API.GetOrganizationSaml",
			"iam/v1alpha1/API.GetOrganizationScim",
			"iam/v1alpha1/API.GetOrganizationSecuritySettings",
			"iam/v1alpha1/API.GetPolicy",
			"iam/v1alpha1/API.GetQuotum",
			"iam/v1alpha1/API.GetSamlCertificate",
			"iam/v1alpha1/API.GetUser",
			"iam/v1alpha1/API.GetUserConnections",
			"iam/v1alpha1/API.InitiateUserConnection",
			"iam/v1alpha1/API.JoinUserConnection",
			"iam/v1alpha1/API.ListAPIKeys",
			"iam/v1alpha1/API.ListApplications",
			"iam/v1alpha1/API.ListGracePeriods",
			"iam/v1alpha1/API.ListGroups",
			"iam/v1alpha1/API.ListJWTs",
			"iam/v1alpha1/API.ListLogs",
			"iam/v1alpha1/API.ListPermissionSets",
			"iam/v1alpha1/API.ListPolicies",
			"iam/v1alpha1/API.ListQuota",
			"iam/v1alpha1/API.ListRules",
			"iam/v1alpha1/API.ListSamlCertificates",
			"iam/v1alpha1/API.ListScimTokens",
			"iam/v1alpha1/API.ListUserWebAuthnAuthenticators",
			"iam/v1alpha1/API.ListUsers",
			"iam/v1alpha1/API.LockUser",
			"iam/v1alpha1/API.ParseSamlMetadata",
			"iam/v1alpha1/API.RemoveGroupMember",
			"iam/v1alpha1/API.RemoveUserConnection",
			"iam/v1alpha1/API.SetGroupMembers",
			"iam/v1alpha1/API.SetOrganizationAlias",
			"iam/v1alpha1/API.SetRules",
			"iam/v1alpha1/API.StartUserWebAuthnRegistration",
			"iam/v1alpha1/API.UnlockUser",
			"iam/v1alpha1/API.UpdateAPIKey",
			"iam/v1alpha1/API.UpdateApplication",
			"iam/v1alpha1/API.UpdateGroup",
			"iam/v1alpha1/API.UpdateOrganizationLoginMethods",
			"iam/v1alpha1/API.UpdateOrganizationSecuritySettings",
			"iam/v1alpha1/API.UpdatePolicy",
			"iam/v1alpha1/API.UpdateSaml",
			"iam/v1alpha1/API.UpdateUser",
			"iam/v1alpha1/API.UpdateUserPassword",
			"iam/v1alpha1/API.UpdateUserUsername",
			"iam/v1alpha1/API.UpdateWebAuthnAuthenticator",
			"iam/v1alpha1/API.ValidateUserMFAOTP"),

		// The marketplace beyond the local images the emulator serves.
		//
		// Categories, versions and the global image index are a catalogue of
		// every image Scaleway publishes across every zone. The emulator has a
		// small fixed table instead, and answering the index from it would either
		// list images no local image maps to, or claim the catalogue is six
		// entries long.
		//
		// GetLocalImage used to be described here as deliberately kept off the
		// list; it is now declined, with its own reason, in the SW-1 block above.
		// What the CLI walks before a create is ListLocalImages, which is served
		// — that is the call the trap is about, and an audit found this comment
		// contradicting the block it introduces.
		emulator.Because("the global image index spans every image Scaleway publishes in every zone, and the emulator answers from a small fixed table that would either list images it cannot boot or claim the catalogue is six entries long",
			"marketplace/v2/API.GetCategory",
			"marketplace/v2/API.GetImage",
			"marketplace/v2/API.GetVersion",
			"marketplace/v2/API.ListCategories",
			"marketplace/v2/API.ListImages",
			"marketplace/v2/API.ListVersions"),

		// ---- lb/v1, the part the measured clients do not call (#282) ----------

		emulator.Because("nothing here probes a backend or forwards a packet, so stats would report a health nothing measured — a backend published UP that nothing checked is the lie this emulator exists to refuse (#315 measured the same for the Outscale LBU)",
			"lb/v1/ZonedAPI.GetLBStats",
			"lb/v1/ZonedAPI.ListBackendStats"),

		emulator.Because("the Terraform provider reconciles a backend's pool through SetBackendServers alone (measured in its services/lb/backend.go, v2.43.0 and v2.81.0), and no surveyed stack edits a pool incrementally",
			"lb/v1/ZonedAPI.AddBackendServers",
			"lb/v1/ZonedAPI.RemoveBackendServers"),

		emulator.Because("the Terraform provider reconciles a frontend's ACLs one by one — CreateACL, ListACLs, UpdateACL, DeleteACL, measured in its services/lb/frontend.go — and never calls the bulk set",
			"lb/v1/ZonedAPI.SetACLs"),

		emulator.Because("migrating changes the commercial offer of the balancer, and an emulated balancer has no capacity to move: answering would confirm a resize nothing performed",
			"lb/v1/ZonedAPI.MigrateLB"),

		emulator.Because("the offer table is the provider's inventory and no measured client reads it before creating — the Terraform provider sends the type the configuration names — so a served list would be an invented catalogue (the ListVolumesTypes argument)",
			"lb/v1/ZonedAPI.ListLBTypes"),

		emulator.Because("nothing here terminates TLS: a certificate served by this emulator would be an ID over key material that signs nothing, and the Let's Encrypt half issues against domains this emulator does not hold",
			"lb/v1/ZonedAPI.CreateCertificate",
			"lb/v1/ZonedAPI.ListCertificates",
			"lb/v1/ZonedAPI.GetCertificate",
			"lb/v1/ZonedAPI.UpdateCertificate",
			"lb/v1/ZonedAPI.DeleteCertificate"),

		emulator.Because("subscribers are an alerting channel, and an emulator whose balancer never degrades has no event to deliver: a subscription recorded here would be a promise nothing can keep",
			"lb/v1/ZonedAPI.CreateSubscriber",
			"lb/v1/ZonedAPI.GetSubscriber",
			"lb/v1/ZonedAPI.ListSubscriber",
			"lb/v1/ZonedAPI.UpdateSubscriber",
			"lb/v1/ZonedAPI.DeleteSubscriber",
			"lb/v1/ZonedAPI.SubscribeToLB",
			"lb/v1/ZonedAPI.UnsubscribeFromLB"),

		// The regional lb/v1 API, deprecated upstream in favour of the zoned
		// one: the SDK's own doc comments say so, the portal documents the
		// zoned spelling only (load-balancer/zoned/v1), and every measured
		// client — the Terraform provider since 2.x, `scw lb` — calls
		// ZonedAPI. Declined as one family; the first client measured on a
		// regional path reopens the entry.
		emulator.Because("the regional lb/v1 API is deprecated upstream in favour of the zoned one, which is served: the portal publishes only the zoned document, and every measured client calls ZonedAPI",
			"lb/v1/API.AddBackendServers",
			"lb/v1/API.AttachPrivateNetwork",
			"lb/v1/API.CreateACL",
			"lb/v1/API.CreateBackend",
			"lb/v1/API.CreateCertificate",
			"lb/v1/API.CreateFrontend",
			"lb/v1/API.CreateIP",
			"lb/v1/API.CreateLB",
			"lb/v1/API.CreateRoute",
			"lb/v1/API.CreateSubscriber",
			"lb/v1/API.DeleteACL",
			"lb/v1/API.DeleteBackend",
			"lb/v1/API.DeleteCertificate",
			"lb/v1/API.DeleteFrontend",
			"lb/v1/API.DeleteLB",
			"lb/v1/API.DeleteRoute",
			"lb/v1/API.DeleteSubscriber",
			"lb/v1/API.DetachPrivateNetwork",
			"lb/v1/API.GetACL",
			"lb/v1/API.GetBackend",
			"lb/v1/API.GetCertificate",
			"lb/v1/API.GetFrontend",
			"lb/v1/API.GetIP",
			"lb/v1/API.GetLB",
			"lb/v1/API.GetLBStats",
			"lb/v1/API.GetRoute",
			"lb/v1/API.GetSubscriber",
			"lb/v1/API.ListACLs",
			"lb/v1/API.ListBackendStats",
			"lb/v1/API.ListBackends",
			"lb/v1/API.ListCertificates",
			"lb/v1/API.ListFrontends",
			"lb/v1/API.ListIPs",
			"lb/v1/API.ListLBPrivateNetworks",
			"lb/v1/API.ListLBTypes",
			"lb/v1/API.ListLBs",
			"lb/v1/API.ListRoutes",
			"lb/v1/API.ListSubscriber",
			"lb/v1/API.MigrateLB",
			"lb/v1/API.ReleaseIP",
			"lb/v1/API.RemoveBackendServers",
			"lb/v1/API.SetBackendServers",
			"lb/v1/API.SubscribeToLB",
			"lb/v1/API.UnsubscribeFromLB",
			"lb/v1/API.UpdateACL",
			"lb/v1/API.UpdateBackend",
			"lb/v1/API.UpdateCertificate",
			"lb/v1/API.UpdateFrontend",
			"lb/v1/API.UpdateHealthCheck",
			"lb/v1/API.UpdateIP",
			"lb/v1/API.UpdateLB",
			"lb/v1/API.UpdateRoute",
			"lb/v1/API.UpdateSubscriber"),

		// ---- vpcgw, the halves the measured clients do not call (#282) --------

		emulator.Because("upgrading changes the gateway's commercial offer in place, and an emulated gateway has no capacity to move: answering would confirm a resize nothing performed (the MigrateLB argument)",
			"vpcgw/v2/API.UpgradeGateway"),

		emulator.Because("the gateway's SSH bastion accepts no connection here — nothing forwards a packet — so refreshing the keys it would present is a rotation over a door that does not open",
			"vpcgw/v2/API.RefreshSSHKeys"),

		emulator.Because("the offer table is the provider's inventory and no measured client reads it before creating — the Terraform provider sends the type the configuration names — so a served list would be an invented catalogue",
			"vpcgw/v2/API.ListGatewayTypes"),

		emulator.Because("a PAT rule recorded and never applied is indistinguishable from protection — the vpc/v2 ingress-rule argument — and no surveyed stack creates one: terraform-talos carries its pat_rule block commented out",
			"vpcgw/v2/API.ListPatRules",
			"vpcgw/v2/API.GetPatRule",
			"vpcgw/v2/API.CreatePatRule",
			"vpcgw/v2/API.UpdatePatRule",
			"vpcgw/v2/API.SetPatRules",
			"vpcgw/v2/API.DeletePatRule"),

		emulator.Because("the bastion allow-list filters connections to a bastion that accepts none here, so a recorded range would claim a filter nothing enforces; served the day the gateway carries a real bastion",
			"vpcgw/v2/API.AddBastionAllowedIPs",
			"vpcgw/v2/API.SetBastionAllowedIPs",
			"vpcgw/v2/API.DeleteBastionAllowedIPs"),

		// The whole of vpcgw/v1, declined for a reason that is not "v2
		// supersedes it": the portal publishes no v1 document any more
		// (measured 2026-08-19, /en/developers/api/public-gateway offers v2
		// alone), and every route mounted in this pack is checked against the
		// portal's document — the same constraint that keeps
		// vpc/v2.EnableCustomRoutesPropagation declined. A client pinned to
		// v1 — the Terraform provider up to 2.51, terraform-talos's ~>2.43
		// pin among them — gets a named 501 pointing here rather than a
		// plain-text 404; the recorded fix for such a stack is the provider
		// bump to ≥2.52, the release that moved vpcgw onto v2.
		emulator.Because("the portal publishes no vpc-gw v1 document any more and every mounted route is checked against that document, so v1 cannot be served; v2 is, and provider releases since 2.52 (March 2025) call it",
			"vpcgw/v1/API.CreateDHCP",
			"vpcgw/v1/API.CreateDHCPEntry",
			"vpcgw/v1/API.CreateGateway",
			"vpcgw/v1/API.CreateGatewayNetwork",
			"vpcgw/v1/API.CreateIP",
			"vpcgw/v1/API.CreatePATRule",
			"vpcgw/v1/API.DeleteDHCP",
			"vpcgw/v1/API.DeleteDHCPEntry",
			"vpcgw/v1/API.DeleteGateway",
			"vpcgw/v1/API.DeleteGatewayNetwork",
			"vpcgw/v1/API.DeleteIP",
			"vpcgw/v1/API.DeletePATRule",
			"vpcgw/v1/API.EnableIPMobility",
			"vpcgw/v1/API.GetDHCP",
			"vpcgw/v1/API.GetDHCPEntry",
			"vpcgw/v1/API.GetGateway",
			"vpcgw/v1/API.GetGatewayNetwork",
			"vpcgw/v1/API.GetIP",
			"vpcgw/v1/API.GetPATRule",
			"vpcgw/v1/API.ListDHCPEntries",
			"vpcgw/v1/API.ListDHCPs",
			"vpcgw/v1/API.ListGatewayNetworks",
			"vpcgw/v1/API.ListGateways",
			"vpcgw/v1/API.ListGatewayTypes",
			"vpcgw/v1/API.ListIPs",
			"vpcgw/v1/API.ListPATRules",
			"vpcgw/v1/API.MigrateToV2",
			"vpcgw/v1/API.RefreshSSHKeys",
			"vpcgw/v1/API.SetDHCPEntries",
			"vpcgw/v1/API.SetPATRules",
			"vpcgw/v1/API.UpdateDHCP",
			"vpcgw/v1/API.UpdateDHCPEntry",
			"vpcgw/v1/API.UpdateGateway",
			"vpcgw/v1/API.UpdateGatewayNetwork",
			"vpcgw/v1/API.UpdateIP",
			"vpcgw/v1/API.UpdatePATRule",
			"vpcgw/v1/API.UpgradeGateway"),

		// The five S3 endpoints vpc/v2 grew since the last scan. They attach a
		// private network to Object Storage, and Object Storage is refused here
		// for a reason docs/limits.md measured: the Terraform provider builds the
		// S3 endpoint from the region in code, so serving it needs DNS
		// interception and a certificate the client accepts.
		emulator.Because("they attach a private network to Object Storage, which is not emulated because the Terraform provider builds that endpoint in code, measured in docs/limits.md",
			"vpc/v2/API.AddPrivateNetworkS3Endpoint",
			"vpc/v2/API.DeletePrivateNetworkS3Endpoint",
			"vpc/v2/API.DisableS3Endpoint",
			"vpc/v2/API.EnableS3Endpoint",
			"vpc/v2/API.SetPrivateNetworksS3Endpoint"),

		// ipam/v1alpha1 is the superseded draft of ipam/v1, which is served.
		emulator.Because("ipam/v1alpha1 is the superseded draft of ipam/v1, which is served",
			"ipam/v1alpha1/API.ListIPs"),

		// instance/v2alpha1 is an alpha rewrite of the whole instance API.
		//
		// "It duplicates what v1 serves" was the argument here and it was
		// wrong, measured: of the thirteen VolumeAPI operations, v1's snapshot
		// surface is untriaged and its ListVolumesTypes and ExportSnapshot are
		// themselves declined, so eight of them duplicate nothing this pack
		// serves. The decision stood on the other two legs — no client reached
		// for it, and emulating an alpha pins down shapes Scaleway is still
		// free to change.
		//
		// **The first leg broke on 17 August 2026**, and this is what a decline
		// reason is for: Terraform provider 2.81.0 shipped that afternoon and
		// reads its private network interfaces through v2alpha1 while still
		// creating them through v1. Within four hours every apply against this
		// emulator failed on a 501. The five private-network-interface
		// operations moved out of this list and into the pack (see
		// privatenics_v2alpha1.go), and the placement-group family followed
		// for the same reason when #285 measured that the provider's CRUD for
		// that resource lives here since the same release
		// (placementgroups_v2alpha1.go). The rest stay, now standing on the
		// second leg alone, and the reason below says so rather than
		// repeating a sentence the measurement has already contradicted once.
		//
		// Listed one by one on purpose. A prefix rule would swallow whatever
		// upstream adds here, and the point of this file is that additions are
		// seen. When the surface stabilises into an instance/v2, the scan
		// reports it as new and this decision gets taken again.
		emulator.Because("instance/v2alpha1 is an alpha rewrite Scaleway is still free to change, and no client this project drives reaches for these operations — a claim that held for the whole API until provider 2.81.0 moved private network interfaces and placement groups onto it, which is why those two families are served and these are not",
			"instance/v2alpha1/VolumeAPI.CreateSnapshot",
			"instance/v2alpha1/VolumeAPI.CreateVolume",
			"instance/v2alpha1/VolumeAPI.DeleteSnapshot",
			"instance/v2alpha1/VolumeAPI.DeleteVolume",
			"instance/v2alpha1/VolumeAPI.ExportSnapshotToObjectStorage",
			"instance/v2alpha1/VolumeAPI.GetSnapshot",
			"instance/v2alpha1/VolumeAPI.GetVolume",
			"instance/v2alpha1/VolumeAPI.ImportSnapshotFromObjectStorage",
			"instance/v2alpha1/VolumeAPI.ListSnapshots",
			"instance/v2alpha1/VolumeAPI.ListVolumeTypes",
			"instance/v2alpha1/VolumeAPI.ListVolumes",
			"instance/v2alpha1/VolumeAPI.UpdateSnapshot",
			"instance/v2alpha1/VolumeAPI.UpdateVolume",
			"instance/v2alpha1/API.AddSecurityGroupRules",
			"instance/v2alpha1/API.AttachServerFileSystem",
			"instance/v2alpha1/API.AttachServerIP",
			"instance/v2alpha1/API.AttachServerPrivateNetworkInterface",
			"instance/v2alpha1/API.AttachServerVolume",
			"instance/v2alpha1/API.CheckTemplate",
			"instance/v2alpha1/API.CreateSecurityGroup",
			"instance/v2alpha1/API.CreateServer",
			"instance/v2alpha1/API.CreateServerFromTemplate",
			"instance/v2alpha1/API.CreateTemplate",
			"instance/v2alpha1/API.DeleteSecurityGroup",
			"instance/v2alpha1/API.DeleteSecurityGroupRules",
			"instance/v2alpha1/API.DeleteServer",
			"instance/v2alpha1/API.DeleteTemplate",
			"instance/v2alpha1/API.DeleteTemplateUserData",
			"instance/v2alpha1/API.DeleteUserData",
			"instance/v2alpha1/API.DetachServerFileSystem",
			"instance/v2alpha1/API.DetachServerIP",
			"instance/v2alpha1/API.DetachServerPrivateNetworkInterface",
			"instance/v2alpha1/API.DetachServerVolume",
			"instance/v2alpha1/API.GetResourceCounts",
			"instance/v2alpha1/API.GetSecurityGroup",
			"instance/v2alpha1/API.GetServer",
			"instance/v2alpha1/API.GetServerCloudInit",
			"instance/v2alpha1/API.GetTemplate",
			"instance/v2alpha1/API.GetTemplateCloudInit",
			"instance/v2alpha1/API.GetTemplateUserData",
			"instance/v2alpha1/API.GetUserData",
			"instance/v2alpha1/API.ListSecurityGroups",
			"instance/v2alpha1/API.ListServerTypes",
			"instance/v2alpha1/API.ListServers",
			"instance/v2alpha1/API.ListTemplateUserDataKeys",
			"instance/v2alpha1/API.ListTemplates",
			"instance/v2alpha1/API.ListUserDataKeys",
			"instance/v2alpha1/API.PauseServer",
			"instance/v2alpha1/API.RebootServer",
			"instance/v2alpha1/API.SetSecurityGroupRules",
			"instance/v2alpha1/API.SetServerCloudInit",
			"instance/v2alpha1/API.SetServerDefaultIP",
			"instance/v2alpha1/API.SetTemplateCloudInit",
			"instance/v2alpha1/API.SetTemplateUserData",
			"instance/v2alpha1/API.SetUserData",
			"instance/v2alpha1/API.StartServer",
			"instance/v2alpha1/API.StopAndDeleteServer",
			"instance/v2alpha1/API.StopServer",
			"instance/v2alpha1/API.UpdateSecurityGroup",
			"instance/v2alpha1/API.UpdateSecurityGroupRule",
			"instance/v2alpha1/API.UpdateServer",
			"instance/v2alpha1/API.UpdateTemplate"),
	)
}

// The zone and region a client defaults to when it is given none. They are the
// ones every conformance fixture uses, so an example copied from the README and
// an example copied from the suite address the same account.
const (
	defaultEnvZone   = "fr-par-1"
	defaultEnvRegion = "fr-par"
)

// Env implements emulator.Pack.
//
// The values are the ones tools/conformance/scaleway/fake-credentials.env
// carries, deliberately and not by coincidence: the CLI and the conformance
// suite must not be able to drift apart, and the constraints they encode are
// real. The access key has to match SCW[A-Z0-9]{17} and the secret key has to be
// a UUID — the client validates their *shape* before it signs anything, though
// the emulator checks neither.
//
// The project and the organization are different UUIDs on purpose. They are
// different things on a real account — infrastructure belongs to a project, IAM
// and billing to the organization above it — and using one value for both makes
// a suite pass over a confusion instead of catching it.
func (p *Pack) Env(endpoint string) emulator.Environment {
	return emulator.Environment{
		Vars: map[string]string{
			"SCW_API_URL":                 endpoint,
			"SCW_ACCESS_KEY":              "SCWXXXXXXXXXXXXXXXXX",
			"SCW_SECRET_KEY":              "11111111-1111-1111-1111-111111111111",
			"SCW_DEFAULT_PROJECT_ID":      "11111111-1111-1111-1111-111111111111",
			"SCW_DEFAULT_ORGANIZATION_ID": "99999999-9999-4999-8999-999999999999",
			"SCW_DEFAULT_ZONE":            defaultEnvZone,
			"SCW_DEFAULT_REGION":          defaultEnvRegion,
			"SCW_INSECURE":                "true",
		},
		Note: "Terraform needs api_url in the provider block; the CLI needs nothing else.",
	}
}
