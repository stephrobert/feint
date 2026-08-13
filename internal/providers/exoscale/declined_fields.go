package exoscale

import "github.com/stephrobert/feint/internal/core/emulator"

// DeclinedFields implements emulator.FieldDecliner: fields the recorded shapes
// prove the live API returns and this pack knowingly does not serve.
//
// Both entries are the same decision, recorded in prose by 98f8df6 and until
// now readable nowhere a gate could see: the live wire is ahead of Exoscale's
// published OpenAPI description, and this emulator enforces that description as
// closed — internal/contract fails a response carrying a field the schema does
// not declare. Serving these would trade a shape finding for a contract
// finding; the description is the API the SDKs are generated from, so the
// description wins until Exoscale publishes the field.
func (p *Pack) DeclinedFields() []emulator.FieldDecline {
	const liveAheadOfSpec = "live on the wire but absent from the published OpenAPI description, which the contract gate enforces as closed"
	return []emulator.FieldDecline{
		{
			Operation: "GET /v2/security-group",
			Path:      "security-groups[].visibility",
			Reason:    liveAheadOfSpec,
		},
		{
			Operation: "GET /v2/zone",
			Path:      "zones[].id",
			Reason:    liveAheadOfSpec,
		},
	}
}
