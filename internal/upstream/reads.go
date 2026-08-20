package upstream

import (
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/shape"
)

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
		// The read one Terraform refreshes a private network with, and the one
		// nothing in this repository could arbitrate: a list read on an account
		// with no network answers an empty array, so it carries no field of the
		// object itself. See the templated-entry note below.
		"/vpc/v2/regions/fr-par/private-networks/" + shape.Placeholder,
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

// A read-list entry ending in [shape.Placeholder] reads *one* element of the
// collection above it, and the identifier is filled in from that collection's
// own answer earlier in the same run.
//
// It exists because the shape of a "read one" was unobservable here. Only a
// collection can be asked for blind: an item read needs an identifier, an
// identifier belongs to an account, and so it can be neither written in this
// list nor committed in a catalogue. #270 sat on that for weeks — the
// private-network read is what the Terraform provider refreshes with, and not
// one field of it was arbitrated by anything in this repository.
//
// Two properties make what comes back committable:
//
//   - the catalogue stores the anonymised path ([shape.AnonymisePath]), so an
//     entry as written above is exactly the key its recording lands under, and
//     no account identifier reaches the file;
//   - the collection is read first, so a run against an account holding none of
//     the resource resolves nothing and says so, instead of inventing an
//     identifier or reading somebody else's.

// TemplateOf returns the collection a templated entry reads one element of, and
// whether the entry is templated at all.
func TemplateOf(call string) (collection string, templated bool) {
	trimmed := strings.TrimSuffix(call, "/"+shape.Placeholder)
	if trimmed == call {
		return "", false
	}
	return trimmed, true
}

// FirstID picks one identifier out of a collection's answer.
//
// These APIs answer a collection as an object holding one array of records —
// {"private_networks": [...], "total_count": 1} — so the array is found rather
// than named: a table of "which key holds the records" per provider per
// resource would be one more thing to maintain, and every line of it a chance
// to name the wrong key.
//
// Keys are visited in sorted order, so one body always yields the same
// identifier however Go happened to hash the map that day. A recording that
// moved with map iteration is exactly the volatility internal/shape refuses.
func FirstID(body any) (string, bool) {
	obj, ok := body.(map[string]any)
	if !ok {
		return "", false
	}
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		items, ok := obj[name].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		first, ok := items[0].(map[string]any)
		if !ok {
			continue
		}
		if id, ok := first["id"].(string); ok && id != "" {
			return id, true
		}
	}
	return "", false
}
