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
	"sync"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// Name is the provider key used by the store and the coverage report.
const Name = "scaleway"

// Pack implements emulator.Pack for Scaleway.
type Pack struct {
	env *emulator.Env
	// defaults serializes the lazy provisioning of per-project inventory (the
	// default security group). The store has no compare-and-set, so without it
	// two concurrent first reads of a zone each create one.
	defaults sync.Mutex
	// addresses serializes address allocation, which is read-modify-write over
	// the store: an allocator is rebuilt from what exists, hands out an address,
	// and the result is persisted. Two requests interleaving there receive the
	// same address, and Terraform creates ten resources at a time by default.
	//
	// One lock for every block rather than one per network: allocation is a
	// handful of map operations, the contention is imperceptible, and a lock per
	// resource would have to be created, found and eventually collected.
	addresses sync.Mutex
}

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

		// Private NICs: where the addressing plan becomes a real interface. The
		// address comes from the Private Network's own block, and the machine
		// driver puts the machine on the backing bridge carrying it.
		{Method: "GET", Path: zones + "/servers/{id}/private_nics", Operation: "instance/v1/API.ListPrivateNICs", Handler: p.listPrivateNICs},
		{Method: "POST", Path: zones + "/servers/{id}/private_nics", Operation: "instance/v1/API.CreatePrivateNIC", Handler: p.createPrivateNIC},
		{Method: "GET", Path: zones + "/servers/{id}/private_nics/{nicID}", Operation: "instance/v1/API.GetPrivateNIC", Handler: p.getPrivateNIC},
		{Method: "DELETE", Path: zones + "/servers/{id}/private_nics/{nicID}", Operation: "instance/v1/API.DeletePrivateNIC", Handler: p.deletePrivateNIC},

		// IPAM. Not a convenience: instance/v1.PrivateNIC carries no address, only
		// ipam_ip_ids, so this is the only way a client learns the address of a
		// NIC. Serving the NIC without it is serving half a product.
		{Method: "GET", Path: ipamRegions + "/ips", Operation: "ipam/v1/API.ListIPs", Handler: p.listIPAMIPs},
		{Method: "GET", Path: ipamRegions + "/ips/{ipID}", Operation: "ipam/v1/API.GetIP", Handler: p.getIPAMIP},

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
	"/vpc/v2/",
	"/ipam/v1/",
	"/iam/v1alpha1/",
	"/marketplace/v2/",

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
	"/instance/v2alpha1/",
	"/interlink/v1beta1/",
	"/iot/v1/",
	"/ipam/v1alpha1/",
	"/k8s/v1/",
	"/kafka/v1alpha1/",
	"/key-manager/v1alpha1/",
	"/lb/v1/",
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
	"/vpc-gw/v2/",
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

		emulator.Because("placement constrains which physical hosts run a machine, and every emulated machine runs on the single host that started feint, so any policy would be reported satisfied whatever it asked",
			"instance/v1/API.CreatePlacementGroup",
			"instance/v1/API.DeletePlacementGroup",
			"instance/v1/API.GetPlacementGroup",
			"instance/v1/API.GetPlacementGroupServers",
			"instance/v1/API.ListPlacementGroups",
			"instance/v1/API.SetPlacementGroup",
			"instance/v1/API.SetPlacementGroupServers",
			"instance/v1/API.UpdatePlacementGroup",
			"instance/v1/API.UpdatePlacementGroupServers"),

		emulator.Because("it mounts Scaleway's File Storage product, and there is no filesystem service behind this emulator for a machine to mount",
			"instance/v1/API.AttachServerFileSystem",
			"instance/v1/API.DetachServerFileSystem"),

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

		// The IPAM half, and the reason it is read-only here.
		emulator.Because("addresses come from the subnet plan a server or a private NIC is placed in rather than from a client reservation, so booking or moving one would hand out an address no runtime configures",
			"instance/v1/API.ReleaseIPToIpam",
			"ipam/v1/API.AttachIP",
			"ipam/v1/API.BookIP",
			"ipam/v1/API.DetachIP",
			"ipam/v1/API.MoveIP",
			"ipam/v1/API.ReleaseIP",
			"ipam/v1/API.ReleaseIPSet",
			"ipam/v1/API.UpdateIP"),

		// Routing, and the honest half of the mode difference: only OVN has a
		// router to program, and a route the bridge mode cannot apply is a
		// topology the packets do not follow.
		emulator.Because("routing between private networks belongs to the runtime, and only the OVN mode has a router to program, so a route recorded in bridge mode would describe a path the packets do not take",
			"vpc/v2/API.CreateRoute",
			"vpc/v2/API.DeleteRoute",
			"vpc/v2/API.GetRoute",
			"vpc/v2/API.UpdateRoute",
			"vpc/v2/API.EnableRouting",
			"vpc/v2/API.EnableCustomRoutesPropagation",
			"vpc/v2/RoutesWithNexthopAPI.ListRoutesWithNexthop"),

		// A rule recorded and never enforced is worse than a refusal: it is
		// indistinguishable from protection.
		emulator.Because("filtering at the VPC edge has no edge here, since nothing routes between the host and these networks, so a rule would be recorded and never enforced",
			"vpc/v2/API.CreateIngressRule",
			"vpc/v2/API.DeleteIngressRule",
			"vpc/v2/API.GetIngressRule",
			"vpc/v2/API.ListIngressRules",
			"vpc/v2/API.UpdateIngressRule",
			"vpc/v2/API.GetACL",
			"vpc/v2/API.SetACL"),

		// Peering is the exact inverse of the property this project measures.
		emulator.Because("it peers two VPCs, and isolation between two VPCs is the one property the bridge mode cannot deliver: joining them would report done what was never apart",
			"vpc/v2/API.CreateVPCConnector",
			"vpc/v2/API.DeleteVPCConnector",
			"vpc/v2/API.GetVPCConnector",
			"vpc/v2/API.ListVPCConnectors",
			"vpc/v2/API.UpdateVPCConnector"),

		emulator.Because("the runtime writes a fixed address on each interface at boot instead of leasing one, so there is no DHCP server behind these networks to enable",
			"vpc/v2/API.EnableDHCP"),

		emulator.Because("the pack publishes a private network's subnets on the network itself, which is where ListPrivateNetworks already returns them",
			"vpc/v2/API.ListSubnets"),

		emulator.Because("it compares the subnets of a fleet of VPCs against each other, and no client in tools/conformance drives it",
			"vpc/v2/API.ListSubnetOverlaps"),

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
		// serves. The decision stands on the other two legs — no client reaches
		// for it (neither the scw binary nor the pinned Terraform provider
		// carries the string), and emulating an alpha pins down shapes Scaleway
		// is still free to change.
		//
		// Listed one by one on purpose. A prefix rule would swallow whatever
		// upstream adds here, and the point of this file is that additions are
		// seen. When the surface stabilises into an instance/v2, the scan
		// reports it as new and this decision gets taken again.
		emulator.Because("instance/v2alpha1 is an alpha rewrite Scaleway is still free to change, and no client this project drives reaches for it: every instance request the conformance suite makes lands on v1",
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
			"instance/v2alpha1/API.CreatePlacementGroup",
			"instance/v2alpha1/API.CreatePrivateNetworkInterface",
			"instance/v2alpha1/API.CreateSecurityGroup",
			"instance/v2alpha1/API.CreateServer",
			"instance/v2alpha1/API.CreateServerFromTemplate",
			"instance/v2alpha1/API.CreateTemplate",
			"instance/v2alpha1/API.DeletePlacementGroup",
			"instance/v2alpha1/API.DeletePrivateNetworkInterface",
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
			"instance/v2alpha1/API.GetPlacementGroup",
			"instance/v2alpha1/API.GetPrivateNetworkInterface",
			"instance/v2alpha1/API.GetResourceCounts",
			"instance/v2alpha1/API.GetSecurityGroup",
			"instance/v2alpha1/API.GetServer",
			"instance/v2alpha1/API.GetServerCloudInit",
			"instance/v2alpha1/API.GetTemplate",
			"instance/v2alpha1/API.GetTemplateCloudInit",
			"instance/v2alpha1/API.GetTemplateUserData",
			"instance/v2alpha1/API.GetUserData",
			"instance/v2alpha1/API.ListPlacementGroups",
			"instance/v2alpha1/API.ListPrivateNetworkInterfaces",
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
			"instance/v2alpha1/API.UpdatePlacementGroup",
			"instance/v2alpha1/API.UpdatePrivateNetworkInterface",
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
