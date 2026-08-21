package emulator

import "testing"

// A pack may vouch for its API's words; it may not vouch for the account's
// data.
//
// The guard is the whole of what stops a sanitised transcript from being a
// transcript with a shorter denylist: an entry here exempts *every* occurrence
// of that value, everywhere, in every recording. A pack that listed a UUID
// would publish that UUID each time it appeared, and the file would still read
// as sanitised.
//
// TestThePacksVocabularyPassesItsOwnGuard (internal/cli, where every pack is
// mounted) is the wiring half.
func TestAVocabularyEntryThatLooksMintedIsRefused(t *testing.T) {
	for _, refused := range []struct{ name, value string }{
		{"a UUID", "3f2a91c4-77b0-4d19-9c2e-51ab8e0d64f7"},
		{"an address", "51.15.0.1"},
		{"an Outscale identifier", "i-0e4a3c1f"},
		{"a blank entry", "   "},
		{"a control character", "fr-par-1\nkey: value"},
		{"something too long to be a word", "0123456789012345678901234567890123456789012345678901234567890123456789"},
	} {
		if got := UnsafeVocabulary([]string{refused.value}); len(got) == 0 {
			t.Errorf("%s was accepted as public vocabulary: every occurrence of it would be published verbatim", refused.name)
		}
	}
}

// And the words themselves pass, or the guard would be a refusal of everything
// and the packs would stop declaring.
func TestARealVocabularyIsAccepted(t *testing.T) {
	values := []string{"fr-par", "fr-par-1", "nl-ams-2", "cloudgouv-eu-west-1", "ch-gva-2"}
	if got := UnsafeVocabulary(values); len(got) != 0 {
		t.Errorf("the guard refuses %v, which are the zone and region names it exists to allow", got)
	}
}
