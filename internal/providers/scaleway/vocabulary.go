package scaleway

import "sort"

// PublicVocabulary implements emulator.Vocabulary: the values a sanitised
// transcript (#351) may keep verbatim because they are Scaleway's own closed
// lists rather than anything of a tenant's.
//
// Exactly the lists this pack validates a request against, read from the maps
// themselves rather than written out again: knownZones (servers.go),
// knownRegions (vpc.go) and gatewayTypes (gateways.go) are what answer 400 for
// an unknown value, so they are precisely the values a sanitiser must not
// replace. A list written here by hand would be a copy that drifts the day a
// zone opens — and it would drift silently, because the symptom is a corpus
// that replays 400 on every call and reads like an emulator defect.
//
// gatewayTypes was MISSING, and this comment used to say the opposite: "a
// commercial type … this emulator does not validate a request against, so
// replacing them costs the replay nothing". It does validate one.
// createGateway (gateways.go) answers 400 "unknown gateway type" for a value
// outside gatewayTypes, so the sanitiser minted a synthetic offer name, the
// replayed CreateGateway was refused, and every read after it addressed a
// gateway that had never been created: ONE root cause, 143 findings, measured
// on 2026-08-24 against a real fr-par recording (#427). It is the same defect
// #354 found four times in the Outscale pack, and the same shape of error the
// rest of this repository keeps paying for — a claim in a comment that nothing
// was checking.
//
// Nothing else is vouched for. An image label and a state name are values this
// emulator does not validate a request against, so replacing them costs the
// replay nothing and keeps the default where it belongs: deny.
//
// TestASanitisedTranscriptStillReplays (internal/cli) fails without the zones:
// the zone in every recorded path becomes a synthetic string and the emulator
// answers 400 "unknown zone" to the whole corpus.
// TestTheVocabularyVouchesForEveryListThePackValidatesAgainst fails without the
// gateway types.
func (p *Pack) PublicVocabulary() []string {
	out := make([]string, 0, len(knownZones)+len(knownRegions)+len(gatewayTypes))
	for zone := range knownZones {
		out = append(out, zone)
	}
	for region := range knownRegions {
		out = append(out, region)
	}
	for offer := range gatewayTypes {
		out = append(out, offer)
	}
	sort.Strings(out)
	return out
}
