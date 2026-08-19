package exoscale

import "strings"

// StackHazards names what, in the text of a Terraform configuration, is
// measured to reach the real cloud no matter what this emulator serves. The
// CLI hands in the stack's comment-stripped text; the one signature here is a
// measurement, not a guess (#262, #284):
//
//   - a literal *.exo.io host, which is SOS. Both surveyed shapes composed it
//     client-side and never touched the emulator: mattias-fjellstrom/…-platform
//     drives SOS buckets through the aws provider pointed at sos-<zone>.exo.io
//     (its apply reached the real endpoint on fake credentials, 403), and
//     bitrockteam/eu-data-platform keeps its state on S3-on-SOS, so
//     `terraform init` talks to the real sos-ch-gva-2.exo.io before any
//     resource exists. Serving SOS cannot fix either — the #284 triage
//     declined it for exactly this reason — so naming the contact is the only
//     answer that serves both.
//
// The Terraform provider's own split client (egoscale v2 without an endpoint
// option) is the other Exoscale escape, and it is not detectable from a
// stack's text: any exoscale_* resource may ride it. That one is refused at
// the emulator's door by user agent instead — the barrage in pack.go — which
// is the stronger position: it sees every request that arrives, where this
// scan only sees what is written down.
//
// TestStackHazardsNameTheSOSEscape fails without the check, and a clean stack
// must return nil: a warning that fires on a healthy configuration teaches
// people to ignore the one that matters.
func (p *Pack) StackHazards(config string) []string {
	var warnings []string
	if strings.Contains(config, ".exo.io") {
		warnings = append(warnings,
			"this configuration names a real *.exo.io host (SOS, Object Storage): those requests are "+
				"composed client-side and go there, never to this emulator — measured on two surveyed "+
				"stacks, an aws provider pointed at sos-<zone>.exo.io and an S3-on-SOS state backend that "+
				"terraform init contacts before any resource (#262, #284). Keep SOS out of an emulated "+
				"run, or cut egress first: docs/limits.md, \"A run presented as local\"")
	}
	return warnings
}
