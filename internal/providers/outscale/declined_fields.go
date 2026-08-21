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
		// Two entries lived here until 2026-08-21, declining
		// Images[].BlockDeviceMappings[].Bsu.SnapshotId and .Iops. They are gone
		// because they stopped being true, and the way they stopped is worth
		// keeping.
		//
		// A field decline says *this pack never serves this field*. That held
		// while ReadImages answered the catalogue alone. It stopped holding when
		// CreateImage began answering an image cut from a real snapshot: there
		// the SnapshotId names a snapshot ReadSnapshots can answer for, and the
		// Iops is the one the client itself declared. Both are served, so both
		// declines were fiction — and tools/conformance/score.sh said so, on the
		// terraform, opentofu, oapi-cli and fields legs at once, with "field
		// declines whose field the emulator now serves".
		//
		// The argument they carried was not wrong, only misplaced: it is about
		// the *catalogue*, not about the operation. It lives in catalog.go beside
		// the mapping that omits both keys, where an absent SnapshotId says this
		// emulator models no snapshot rather than naming a fictional one (rule
		// 4), and an absent Iops keeps a performance figure out of a client's
		// plan for a `standard` volume that has none.
		//
		// The general shape, and it is the same one corpus/accepted.json is
		// built on: an exemption whose subject is one *object* cannot be written
		// against an *operation* that answers more than one kind of object. It
		// reads as true for as long as only one kind exists.
	}
}
