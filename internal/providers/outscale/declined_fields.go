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
	}
}
