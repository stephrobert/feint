package exoscale

import "github.com/stephrobert/feint/internal/core/emulator"

// DeclinedFields implements emulator.FieldDecliner: fields the recorded shapes
// prove the live API returns and this pack knowingly does not serve.
//
// Empty, and that is a measurement rather than an omission. It carried two
// entries — GET /v2/security-group's visibility and GET /v2/zone's id — both
// declined for one reason written in prose by 98f8df6: the live wire is ahead of
// Exoscale's published OpenAPI description, this emulator enforces that
// description as closed, and serving either field traded a shape finding for a
// contract finding.
//
// #370 and #371 retired both by moving the thing that was actually wrong. The
// description is behind the API, so the contract now records that explicitly:
// tools/contract/exoscale-recorded-fields.yaml adds each field with the
// recording that proves the cloud answers it, and internal/cli's
// TestEveryRecordedFieldIsStillOnTheWire fails the day no recording does. The
// pack answers both, and the declines had to go with them — a decline that
// declines nothing fails `feint shapes --check` under the same staleness rule
// corpus/accepted.json states (TestAStaleFieldDeclineFailsTheGate).
//
// The nil is the honest answer while there is nothing to decline. A pack that
// starts refusing a field again declares it here, with the reason, because
// "not served" and "not triaged" are not the same answer.
func (p *Pack) DeclinedFields() []emulator.FieldDecline {
	return nil
}
