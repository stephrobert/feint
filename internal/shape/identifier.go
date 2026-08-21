package shape

import (
	"net"
	"regexp"
)

// prefixedID is the spelling Outscale mints: one or more lower-case labels,
// then a run of at least eight hexadecimal characters — "i-0e4a3c1f",
// "eni-attach-0e4a3c1f", "key-<32 hex>". Read off internal/providers/outscale's
// own newID rather than guessed, so the shape recognised is the shape minted.
var prefixedID = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z][a-z0-9]*)*-[0-9a-f]{8,}$`)

// IsMintedIdentifier reports whether a value is the kind of thing a cloud hands
// out and a later request refers back to.
//
// Three shapes, each read off a provider rather than imagined: a UUID (Scaleway
// and Exoscale address every resource that way), an IP address (allocated per
// run, and named by later rules and routes), and Outscale's prefixed
// hexadecimal identifier.
//
// One definition rather than two, and the reason is that two readers ask this
// exact question from opposite ends of the same chain: internal/replay asks it
// of a recorded value before rebinding it to the one this emulator answered,
// and internal/corpus asks it of a value before deciding whether a transcript
// may be committed. Two spellings would answer differently the day one of them
// learned a case, and a sanitiser that recognised one identifier less than the
// replay would publish exactly the values the replay knows are identifiers.
//
// A shape a fourth provider invents is not recognised. For the replay the
// honest consequence is a divergence rather than a silent substitution; for the
// corpus it is a leak, which is why internal/corpus does not rest on this
// function alone — its scan reads the bytes it is about to commit and refuses
// anything outside the alphabet a sanitised transcript may contain.
func IsMintedIdentifier(s string) bool {
	if IsUUID(s) {
		return true
	}
	if net.ParseIP(s) != nil {
		return true
	}
	return prefixedID.MatchString(s)
}
