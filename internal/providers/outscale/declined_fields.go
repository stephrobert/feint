package outscale

import "github.com/stephrobert/feint/internal/core/emulator"

// DeclinedFields implements emulator.FieldDecliner: fields a real account's
// recorded answers carry and this pack knowingly does not serve. Spelled with
// the mounted operation name, which is how the live field gate (#88) joins;
// the offline shapes gate reads only the declines it can resolve to its own
// catalogue keys, so an entry here never reads as stale over there.
func (p *Pack) DeclinedFields() []emulator.FieldDecline {
	return []emulator.FieldDecline{
		// A public address links to a machine here (LinkPublicIp --VmId), and
		// the link is published on the machine's interfaces (linkPublicIPView).
		// Linking to a bare interface is not modelled, so a standalone Nic's
		// private IPs never carry the link the recorded account's did. The
		// stale rule retires this entry by failing the gate the day the pack
		// serves the field.
		{
			Operation: "osc/Client.ReadNics",
			Path:      "Nics[].PrivateIps[].LinkPublicIp",
			Reason:    "an address links to a machine here, never to a bare interface, so a standalone Nic has no link to publish",
		},
		// Iops on an image's root device, and this is the one decline of the
		// pair that #389 did not dissolve. SnapshotId went the moment the
		// catalogue named a snapshot the pack really holds; Iops stayed,
		// because the premise under it was wrong in the other direction.
		//
		// The measurement, and it reverses what this file used to assume. Of
		// the 399 image device mappings the real account answered in
		// corpus/outscale/oapi-cli-lifecycle.jsonl, 396 carry NO Iops key at
		// all. The 3 that do are exactly the 3 whose VolumeType is the
		// provisioned-IOPS one. Iops is a property of that volume type, not of
		// every image — and shapes/outscale.json carries it only because that
		// catalogue is the UNION of every field ever observed, which is what
		// an earlier reading took for a per-element requirement and defaulted
		// to 100.
		//
		// What makes this decline honest where the 2026-08-21 pair was not: a
		// decline is written against an *operation*, and ReadImages answers two
		// kinds of object. It is true here for BOTH — the catalogue's images
		// are standard volumes, and createImage refuses an Iops rather than
		// storing one, so no image this operation can answer carries the field.
		// #389 is the record of what happens otherwise: score.sh fails the
		// terraform, opentofu, oapi-cli and fields legs at once with "field
		// declines whose field the emulator now serves".
		//
		// TestNoImageThisPackServesCarriesADeclinedField fails without the
		// property this decline depends on.
		{
			Operation: "osc/Client.ReadImages",
			Path:      "Images[].BlockDeviceMappings[].Bsu.Iops",
			Reason:    "Iops is a property of a provisioned-IOPS volume type, which this emulator does not model: 396 of the 399 device mappings the recorded account answered carry no Iops key, and the 3 that do are the 3 on that volume type",
		},
	}
}
