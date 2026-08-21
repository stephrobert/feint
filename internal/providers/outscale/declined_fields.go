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
		// The one field of an image's device mapping that names another object.
		// #383 served the rest of the mapping — the device name, the root
		// volume's size and type, which a client reads before it sizes what it
		// creates — and stops here on purpose.
		//
		// A SnapshotId would name a snapshot ReadSnapshots cannot answer for,
		// and that is not a cosmetic difference: the same shape, a fictional
		// root VolumeId written on a machine, killed a whole conformance run
		// once when the Terraform provider resolved it. An absent key says this
		// emulator models no snapshot behind its catalogue; an invented one
		// would say something false about an object that does not exist, which
		// is rule 4.
		//
		// The stale rule retires this entry by failing the gate the day the
		// catalogue is backed by snapshots the pack really holds.
		{
			Operation: "osc/Client.ReadImages",
			Path:      "Images[].BlockDeviceMappings[].Bsu.SnapshotId",
			Reason:    "the emulated catalogue is backed by no snapshot, and naming one ReadSnapshots cannot answer for is how a client's resolve fails on an object that does not exist",
		},
		// The other half of the same decision, and it surfaced the moment the
		// mapping stopped being empty: a gate that could not descend into an
		// empty list can descend into this one.
		//
		// The catalogue's root volume is `standard`, and a standard volume has
		// no provisioned IOPS — the field belongs to io1. A number here would
		// be a performance figure entering a client's plan with nothing behind
		// it, which is the argument the Scaleway catalogue already makes for
		// per_volume_constraint.l_ssd.
		{
			Operation: "osc/Client.ReadImages",
			Path:      "Images[].BlockDeviceMappings[].Bsu.Iops",
			Reason:    "the catalogue's root volume is standard and a standard volume has no provisioned IOPS, so a number here would be a performance figure in a client's plan with nothing behind it",
		},
	}
}
