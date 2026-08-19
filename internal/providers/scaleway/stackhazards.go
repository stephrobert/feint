package scaleway

import "strings"

// StackHazards names what, in the text of a Terraform configuration, is
// measured to reach the real cloud no matter where SCW_API_URL or api_url
// point. The CLI hands in the stack's comment-stripped text; every signature
// here is a measurement, not a guess:
//
//   - scaleway_object_* resources: the provider hardcodes
//     https://s3.<region>.scw.cloud in newS3Client (no env var, no attribute —
//     the docs/limits.md table), so their calls ignore the redirect entirely.
//     Measured live on a surveyed stack (#262, ioandev/scaleway-flatcar-k3s):
//     CreateBucket went to the real endpoint and only the fake credentials'
//     403 made it harmless. Reproduced for #280 with egress cut to a dead
//     proxy: the same apply created its instance IP on the emulator and died
//     on `Put "https://<bucket>.s3.fr-par.scw.cloud/"` — one run, half local,
//     half not, indistinguishable from outside.
//   - a literal *.scw.cloud host: whatever names it (an S3 state backend as in
//     CentraleSupelec/kubic, an aws provider block) sends those requests to
//     that host by construction — an emulator on 127.0.0.1 never sees them.
//
// TestStackHazardsNameTheObjectStorageEscape fails without the first check,
// TestStackHazardsNameARealHost without the second, and a clean stack must
// return nil: a warning that fires on a healthy configuration teaches people
// to ignore the one that matters.
func (p *Pack) StackHazards(config string) []string {
	var warnings []string
	if strings.Contains(config, `"scaleway_object`) {
		warnings = append(warnings,
			"this configuration carries scaleway_object_* (Object Storage): the Terraform provider "+
				"hardcodes https://s3.<region>.scw.cloud for that product, so those calls leave for the "+
				"real cloud with whatever credentials the run holds — measured on a surveyed stack, where "+
				"only a 403 on fake credentials made it harmless (#262). Keep Object Storage out of an "+
				"emulated run, or cut egress first: docs/limits.md, \"A run presented as local\"")
	}
	if strings.Contains(config, ".scw.cloud") {
		warnings = append(warnings,
			"this configuration names a real *.scw.cloud host: requests to it go there, never to this "+
				"emulator. An S3 state backend is the measured shape. Check the mention is deliberate "+
				"before treating the run as local (docs/limits.md, \"A run presented as local\")")
	}
	return warnings
}
