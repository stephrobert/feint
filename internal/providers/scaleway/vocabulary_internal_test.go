package scaleway

import "testing"

// Every closed list this pack answers 400 against is vouched for.
//
// The property is not "the vocabulary is long enough", it is a JOIN: a value
// the pack refuses when it is unknown is exactly a value the sanitiser must not
// replace. Get that wrong in the direction of forgetting one and the corpus is
// unreplayable in a way that reads like an emulator defect -- the recorded
// create answers 400, and every read after it addresses an object that was
// never made.
//
// MEASURED on 2026-08-24 (#427): gatewayTypes was missing, one recorded
// CreateGateway was refused, and `feint corpus --check` reported 143 findings
// from that single cause -- 126 of them "GetGateway omits <field>" for a
// gateway the replay had never created. The vocabulary's own comment said, in
// so many words, that no commercial type was ever validated. createGateway
// validates one. That is the failure this repository names "un commentaire
// n'est pas un contrôle", and #354 had already found it four times in the
// Outscale pack.
//
// Written over the maps rather than over a list of names, so a zone opening or
// an offer arriving cannot make this test pass while the vocabulary drifts.
//
// The other half of the join is already held elsewhere and stays there:
// TestAnUnknownGatewayTypeIsRefused (gateways_test.go) proves the pack really
// does answer 400 on a type outside the list, so this test cannot be satisfied
// by deleting the validation instead of extending the vocabulary.
func TestTheVocabularyVouchesForEveryListThePackValidatesAgainst(t *testing.T) {
	p := &Pack{}
	vouched := map[string]bool{}
	for _, v := range p.PublicVocabulary() {
		vouched[v] = true
	}

	for _, list := range []struct {
		name   string
		values map[string]bool
	}{
		{"knownZones (servers.go)", knownZones},
		{"knownRegions (vpc.go)", knownRegions},
		{"gatewayTypes (gateways.go)", gatewayTypes},
	} {
		for value := range list.values {
			if !vouched[value] {
				t.Errorf("%s holds %q and PublicVocabulary does not vouch for it: "+
					"a sanitised transcript will replace it, and the request the pack "+
					"validates against that list will be refused on replay",
					list.name, value)
			}
		}
	}
}
