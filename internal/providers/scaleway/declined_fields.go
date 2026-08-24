package scaleway

import "github.com/stephrobert/feint/internal/core/emulator"

// DeclinedFields implements emulator.FieldDecliner: the fields the recorded
// shapes prove this cloud returns and this pack knowingly does not serve. The
// decision level below Declined() — an operation served, one field of it
// refused — consumed by `feint shapes --check`, which excuses these with their
// reason instead of failing, and fails the other way when one stops excusing a
// real gap, so an entry here cannot outlive the omission it argues for.
func (p *Pack) DeclinedFields() []emulator.FieldDecline {
	return []emulator.FieldDecline{
		// catalog.go serialises per_volume_constraint as an empty object on
		// purpose, and documents why beside the field: the real cloud states
		// l_ssd bounds on the DEV1 and GP1 types because they carry local SSD,
		// while the emulated types attach no local volume — the same measured
		// trap that keeps volumes_constraint.min_size at 0, where a bound the
		// CLI sums against made `scw instance server create` refuse. The "*"
		// stands for the commercial type names, which are the catalogue's
		// inventory rather than part of this decision.
		{
			Operation: "GET /instance/v1/zones/fr-par-1/products/servers",
			Path:      "servers.*.per_volume_constraint.l_ssd",
			Reason:    "a bound for local volumes this catalogue never attaches, which would enter the client's size arithmetic with nothing behind it",
		},
		// The same decision, spelled for the gates that join on the mounted
		// operation name — the live field gate (#88) and the corpus replay
		// (#353). Without it the replay met no refusal and graded the nine
		// bounds as nine divergences, which is what corpus/accepted.json
		// carried until #355.
		//
		// Only this half of the catalogue's decision is spelled twice, and the
		// other half is deliberately not: the 118 commercial types this
		// catalogue does not serve are *keys of a dictionary*, and the path
		// that would decline them ("servers.*") also names the 18 it does
		// serve — measured, the omission gate published it as a stale decline
		// and tools/conformance/score.sh fails on those. An inventory entry is
		// not a field, and transcript.DataKeyed is where that is settled for
		// both gates at once.
		//
		// TestTheCatalogueBoundIsDeclinedWhereTheReplayJoins fails without this.
		{
			Operation: "instance/v1/API.ListServersTypes",
			Path:      "servers.*.per_volume_constraint.l_ssd",
			Reason:    "a bound for local volumes this catalogue never attaches, which would enter the client's size arithmetic with nothing behind it",
		},
		// The gateway's software version, on the two doors that answer a
		// gateway. Declined rather than served because there is no truthful
		// value: `version` is "version of the running gateway software"
		// (vpcgw/v2/vpcgw_sdk.go), and nothing runs here — the same argument
		// that already declines vpcgw/v2/API.UpgradeGateway, which is the
		// operation that would change it. A number would let a client branch
		// on a build this emulator does not carry.
		//
		// It goes the day a gateway is backed by a real runtime, and the gate
		// makes that compulsory: an entry that excuses nothing fails.
		//
		// TestTheGatewaySoftwareVersionIsDeclinedRatherThanInvented fails
		// without this.
		{
			Operation: "vpcgw/v2/API.GetGateway",
			Path:      "version",
			Reason:    "the version of the gateway software, which this emulator does not run: a build number here would let a client branch on a release that is not installed anywhere",
		},
		{
			Operation: "vpcgw/v2/API.CreateGateway",
			Path:      "version",
			Reason:    "the version of the gateway software, which this emulator does not run: a build number here would let a client branch on a release that is not installed anywhere",
		},
		// The element of the bastion allow-list, and only the element: the
		// array itself is served, empty. The three operations that write it
		// are already declined at operation level in pack.go, in these words —
		// "the bastion allow-list filters connections to a bastion that
		// accepts none here, so a recorded range would claim a filter nothing
		// enforces". A response carrying 0.0.0.0/0 while Set, Add and Delete
		// all refuse would state a filter no client can change and nothing
		// applies, which is the half-truth measurement-integrity's rule 5
		// exists for: the decision has to hold at both levels or it holds at
		// neither.
		{
			Operation: "vpcgw/v2/API.GetGateway",
			Path:      "bastion_allowed_ips[]",
			Reason:    "a range allowed through a bastion that accepts no connection here, while the three operations that would change the list are themselves declined: publishing one would claim a filter nothing enforces and no client can edit",
		},
		{
			Operation: "vpcgw/v2/API.CreateGateway",
			Path:      "bastion_allowed_ips[]",
			Reason:    "a range allowed through a bastion that accepts no connection here, while the three operations that would change the list are themselves declined: publishing one would claim a filter nothing enforces and no client can edit",
		},
		{
			Operation: "ipam/v1/API.ListIPs",
			Path:      "ips[].source.zonal",
			Reason:    "zonal is the source of a flexible address, and this IPAM registers private-network addresses only, whose source is the network trio; writing zonal on them would mislabel every address served",
		},
	}
}
