package emulator

import (
	"sort"
)

// Declined() answers for whole operations. One level down, the shapes gate
// (`feint shapes --check`) compares what this emulator answers with what a real
// cloud was observed to answer, field by field — and some of those fields are
// decisions, not omissions: a constraint for volumes the emulator never
// attaches, a field live on the wire but absent from the API description the
// contract gate enforces as closed. Without a way to say so, the gate stays red
// on purpose-made gaps, and a gate that is red on purpose is a gate nobody
// wires into CI. The same doctrine applies at both levels: "not served" and
// "not triaged" are different answers, and the difference is data, not a
// comment.

// FieldDecline is one field of an observed response a pack knowingly does not
// serve, and why.
type FieldDecline struct {
	// Operation is spelled exactly as the shapes catalogue keys it — the
	// upstream operation name when the recording carried one, "METHOD path"
	// otherwise. The gate joins on this string; a spelling of its own would
	// make the decline silently match nothing, which is why a decline that
	// matches nothing fails the gate instead of resting unused.
	Operation string
	// Path is the field's dotted path in the recorded shape, e.g.
	// "things[].limits.local". A "*" segment matches exactly one segment, for
	// dictionaries whose keys are data: "things.*.limits.local" declines the
	// field under every key without writing the keys down, because the keys
	// belong to the cloud's inventory, not to the decision.
	Path string
	// Reason is one line, in the present tense, saying what makes this field a
	// decision rather than an omission. It faces the same guard as an
	// operation's reason: no placeholder, no half-clause.
	Reason string
}

// FieldDecliner is implemented by a pack that declines fields of otherwise
// served operations. Optional, in the manner of machine.Capable: a pack with
// nothing to declare implements nothing, and FieldDeclinesOf answers nil for
// it, so absence of the method reads as "no field is declined" rather than as
// an error.
type FieldDecliner interface {
	DeclinedFields() []FieldDecline
}

// FieldDeclinesOf returns what a pack declines at field level, or nil for a
// pack that declares nothing.
func FieldDeclinesOf(p Pack) []FieldDecline {
	if fd, ok := p.(FieldDecliner); ok {
		return fd.DeclinedFields()
	}
	return nil
}

// Matches reports whether this decline covers the field at path within
// operation.
//
// The segment rule lives in pathMatches (invariants.go) and is shared with
// [Invariant.Matches]: "*" stands for exactly one segment — never zero, never
// several, or a decline written for "things.*.limits.local" would also excuse
// "things.limits.local" and every deeper field a future recording learns,
// widening a narrow decision silently. One implementation, because the gate
// that excuses a field and the replay that compares one must not disagree
// about which field they are naming.
//
// TestAWildcardSegmentMatchesExactlyOneSegment fails without this.
func (d FieldDecline) Matches(operation, path string) bool {
	return d.Operation == operation && pathMatches(d.Path, path)
}

// UnexplainedFieldDeclines reports field declines whose reason carries no
// decision, under the same guard as UnexplainedDeclines: an empty reason, a
// known placeholder, a clause too short to hold an argument. The identifiers
// come back as "operation path" so the caller can name the offender.
//
// TestFieldDeclineReasonsFaceTheSameGuardAsOperations fails without this.
func UnexplainedFieldDeclines(declined []FieldDecline) []string {
	var out []string
	for _, d := range declined {
		if carriesNoDecision(d.Reason) {
			out = append(out, d.Operation+" "+d.Path)
		}
	}
	return out
}

// DuplicateFieldDeclines reports fields declined more than once, for the same
// reason DuplicateDeclines exists one level up: two entries for one field mean
// two reasons for one decision, and whichever consumer walks the slice prints
// a count that disagrees with the set.
func DuplicateFieldDeclines(declined []FieldDecline) []string {
	seen := map[string]int{}
	for _, d := range declined {
		seen[d.Operation+" "+d.Path]++
	}
	var out []string
	for key, n := range seen {
		if n > 1 {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
