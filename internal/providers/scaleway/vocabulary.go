package scaleway

import "sort"

// PublicVocabulary implements emulator.Vocabulary: the values a sanitised
// transcript (#351) may keep verbatim because they are Scaleway's own closed
// lists rather than anything of a tenant's.
//
// Exactly the two lists this pack validates a request against, read from the
// maps themselves rather than written out again: knownZones (servers.go) and
// knownRegions (vpc.go) are what answer 400 for an unknown value, so they are
// precisely the values a sanitiser must not replace. A third list written here
// by hand would be a copy that drifts the day a zone opens — and it would drift
// silently, because the symptom is a corpus that replays 400 on every call and
// reads like an emulator defect.
//
// Nothing else is vouched for. A commercial type, an image label and a state
// name are all values this emulator does not validate a request against, so
// replacing them costs the replay nothing and keeps the default where it
// belongs: deny.
//
// TestASanitisedTranscriptStillReplays (internal/cli) fails without this: the
// zone in every recorded path becomes a synthetic string, and the emulator
// answers 400 "unknown zone" to the whole corpus.
func (p *Pack) PublicVocabulary() []string {
	out := make([]string, 0, len(knownZones)+len(knownRegions))
	for zone := range knownZones {
		out = append(out, zone)
	}
	for region := range knownRegions {
		out = append(out, region)
	}
	sort.Strings(out)
	return out
}
