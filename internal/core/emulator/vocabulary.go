package emulator

import (
	"sort"
	"strings"
	"unicode"

	"github.com/stephrobert/feint/internal/shape"
)

// A transcript recorded against a real cloud is the inventory of somebody's
// account, and internal/corpus (#351) is what turns one into an artefact this
// repository may commit: every value it carries is replaced by a synthetic one
// of the same shape, so the file keeps the statuses, the field trees and the
// order a replay grades, and holds none of the identifiers, addresses and names
// a tenant cannot publish.
//
// That default cannot be absolute, and this is what a pack declares here. Some
// values in a request are not the tenant's data at all: they are the provider's
// own closed lists, and the API validates against them. Scaleway refuses an
// unknown zone by name (knownZones in servers.go, knownRegions in vpc.go), so a
// transcript whose "fr-par-1" had been replaced by a synthetic string would be
// answered 400 by this emulator on every call — a divergence manufactured by
// the sanitiser rather than measured on the cloud, which is the "artefact of
// the instrument" family #73 already paid for once.
//
// So the rule is: **default deny, and what survives is what a pack vouches
// for.** Not a rule about names — docs/proxy.md's own argument is that a
// redaction by name answers "does this look like a secret" and never "is this
// not one" — but an allowlist of exact values that this repository publishes in
// its own source. A tenant string that happens to equal one of them survives,
// and reveals only that coincidence.

// Vocabulary is implemented by a pack that publishes the closed lists its API
// validates a request against. Optional, in the manner of [FieldDecliner] and
// [Invariable]: a pack that declares nothing has every value of its transcripts
// replaced, which is the safe direction.
type Vocabulary interface {
	// PublicVocabulary returns the provider's own constants — zone and region
	// names and the like — that a sanitised transcript may keep verbatim.
	PublicVocabulary() []string
}

// VocabularyOf returns what a pack vouches for, or nil for a pack that vouches
// for nothing.
func VocabularyOf(p Pack) []string {
	if v, ok := p.(Vocabulary); ok {
		return v.PublicVocabulary()
	}
	return nil
}

// maxVocable bounds one entry. A closed list of an API is short words — zones,
// regions, states — and a long string is how a value that is really data would
// arrive in a list that is supposed to hold vocabulary.
const maxVocable = 64

// UnsafeVocabulary reports the entries a pack must not be allowed to keep
// verbatim, under the guard [UnexplainedInvariants] applies one level up: a
// declaration faces a check rather than resting on the reviewer who reads it.
//
// Three ways an entry is refused, and each one is a way a sanitiser would
// publish what it was built to remove:
//
//   - it is shaped like something a cloud minted (a UUID, an address, an
//     Outscale "i-<hex>"). That is the whole class the corpus exists to strip,
//     and a pack that listed one would exempt every occurrence of it;
//   - it is empty or blank, which would exempt nothing and read as a decision;
//   - it is longer than [maxVocable], or carries a control character, which is
//     not vocabulary but data — a name, a description, a key.
//
// TestAVocabularyEntryThatLooksMintedIsRefused fails without this, and
// TestThePacksVocabularyPassesItsOwnGuard (internal/cli, where every pack is
// mounted) is the half that proves the guard is actually wired to the packs.
func UnsafeVocabulary(values []string) []string {
	var out []string
	for _, v := range values {
		switch {
		case strings.TrimSpace(v) == "":
			out = append(out, "(blank)")
		case shape.IsMintedIdentifier(v):
			out = append(out, v)
		case len(v) > maxVocable:
			out = append(out, v[:maxVocable]+"…")
		case strings.ContainsFunc(v, unicode.IsControl):
			out = append(out, "(carries a control character)")
		}
	}
	sort.Strings(out)
	return out
}
