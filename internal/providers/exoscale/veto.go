package exoscale

// No Terraform for Exoscale, until upstream stops the split. The maintainer's
// decision of 2026-08-26, taken on #525's measurement, and this method is its
// enforcement at the only doorstep that fires before a process starts.
//
// The history, dated rather than erased: the published exoscale/exoscale
// provider builds two clients and honours EXOSCALE_API_ENDPOINT for one of
// them, so an apply or destroy *splits* between this emulator and the real
// cloud (docs/limits.md, upstream exoscale/terraform-provider-exoscale#573).
// A four-line fork proved the emulated surface itself holds under Terraform —
// sixteen resources, empty second plan, clean destroy, 2026-08-24 — but a
// patched client is not the official client, and #525 then measured what the
// remaining paths cost: a `feint down` without the fork's dev_overrides
// resolved the published 0.70.0 and sent five signed requests to
// api-ch-*.exoscale.com, stopped only by the pack's fake credential pair
// outranking the shell's. So the fork stopped being a recipe and Terraform
// stopped being a client of this pack, fork included, until #573 is fixed
// where every user gets the fix: upstream.
//
// Both engine names, because OpenTofu resolves the same published provider
// from the same registry namespace. The emulator-side user-agent guard
// (guardSplitClients in pack.go) stays as the last line for a hand-run
// terraform that never went through `feint up`, which is the #55 scenario;
// this veto is the first line, and the incident proved the first line is the
// one that matters — those five requests never reached the emulator.
//
// TestTheVetoNamesTheDecisionTheIssueAndTheRemainingClient fails if the veto
// goes quiet or stops naming a way on; in internal/cli,
// TestUpRefusesAVetoedEngineBeforeStartingAnything fails when `up` stops
// asking, and TestDownSkipsAVetoedEngineOutLoudAndNeverRunsIt when `down`
// does.
func (p *Pack) VetoEngine(engine string) string {
	switch engine {
	case "terraform", "opentofu":
		return "no Terraform for Exoscale until the published provider honours " +
			"EXOSCALE_API_ENDPOINT for both of the clients it builds " +
			"(exoscale/terraform-provider-exoscale#573): an apply or destroy splits between " +
			"this emulator and the real cloud, and #525 measured five signed requests leaving " +
			"for api-ch-*.exoscale.com. The exo CLI drives this pack end to end: " +
			"eval \"$(feint env exoscale)\", then exo"
	default:
		return ""
	}
}
