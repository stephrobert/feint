package outscale

import "sort"

// PublicVocabulary implements emulator.Vocabulary: the values a sanitised
// transcript (#351) may keep verbatim because they are Outscale's own closed
// lists rather than anything of a tenant's.
//
// Exactly the list this pack validates a request against. [knownSubregion] is
// what answers 400 for an unknown subregion — the #269 invariant, so that a
// create and the catalogue cannot disagree about which zones exist — and a
// region name is what [NewInRegion] refuses when it is not published. Those two
// are therefore precisely the values a sanitiser must not replace.
//
// **The union of every region, not the one in force**, and that is the
// difference from [subregionsOf], which serves one region on purpose. The two
// answer different questions: the catalogue says what this deployment offers,
// and this says what Outscale publishes and therefore what a recording of *any*
// Outscale endpoint may keep. The sanitiser runs once, against a pack built at
// its default region, and a recording made against another one would otherwise
// lose its subregion — which is exactly the defect this file fixes.
//
// Nothing else is vouched for. A VM type, an image name, a state and a
// description are values this emulator does not validate a request against, so
// replacing them costs the replay nothing and keeps the default where it
// belongs: deny.
//
// TestASanitisedOutscaleTranscriptKeepsWhatThePackValidates (internal/cli,
// where every pack is mounted) fails without this: the SubregionName of every
// CreateSubnet and CreateVolume becomes a synthetic string, [knownSubregion]
// refuses it, and the corpus replays 400 from the first create onwards — around
// a hundred findings, none of them a defect of the emulator. Measured on
// 2026-08-21 recording a real cloudgouv-eu-west-1 account:
// `{"IpRange":"198.18.12.0/24","NetId":"vpc-0000032a","SubregionName":"redacted-5"}`.
func (p *Pack) PublicVocabulary() []string {
	out := make([]string, 0, 4*len(regionCatalogue)+2)
	for region := range regionCatalogue {
		out = append(out, region)
		subs, _ := subregionsOf(region)
		for _, sub := range subs {
			out = append(out, stringOf(sub["SubregionName"]))
		}
	}
	// The direction of a security-group rule, from the same constants
	// [ruleTarget] refuses anything else against. Same argument as the
	// subregion: measured on the 2026-08-21 recording, "Inbound" became
	// "redacted-1127" and both CreateSecurityGroupRule calls answered
	// 400 "Flow must be Inbound or Outbound" where the cloud answered 200.
	out = append(out, flowInbound, flowOutbound)
	// The two kinds of load balancer and the four protocols a listener may
	// take, from the same values createLoadBalancer and listenerViews refuse
	// anything else against. On the same recording, "internal" became
	// "redacted-1208" and "TCP" became "redacted-1206", and CreateLoadBalancer
	// answered 400 where the cloud answered 200.
	out = append(out, lbInternetFacing, lbInternal)
	out = append(out, listenerProtocols...)
	sort.Strings(out)
	return out
}
