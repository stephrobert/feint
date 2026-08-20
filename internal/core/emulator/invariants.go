package emulator

import (
	"sort"
	"strings"
)

// A replay (`feint replay`, #73) reissues a recorded request at this emulator
// and compares the two answers. Most of that comparison needs no declaration:
// the status is exact, the set of fields is exact minus what DeclinedFields()
// excuses, and the types are exact. Two aspects cannot be decided that way, and
// both of them have already cost this repository a defect:
//
//   - **a value.** Identifiers, timestamps and addresses differ by construction
//     between two runs, so comparing every value would paint a run red and the
//     tool would be ignored within the week. Comparing none of them would let
//     an emulator that accepts a name and answers another pass — the "unread
//     request field" family, which is the one causal defect this project
//     measures.
//   - **an order.** Most lists carry no order in their contract, and asserting
//     one would be inventing a rule the cloud never stated. Some do:
//     Server.public_ips is a list Terraform stores as a list, and a read that
//     reorders it is a permanent plan diff (#320).
//
// Neither is derivable from the SDK or from the contract artefacts: 2,489
// Outscale properties declare 10 patterns and no ordering at all. So it is a
// declaration, held to the same discipline as Declined() one level up — the
// pack says what it knows, in a field, with a reason, and a declaration that
// names nothing served fails a test rather than resting unread.

// InvariantKind names what a replay may check at a field, beyond the presence
// and the type it checks everywhere.
type InvariantKind string

const (
	// InvariantValue: the value at this path is the same on two runs of the
	// same request, so a replay compares it. Reserved for what the request
	// itself carried or what the API fixes — never for a value the cloud mints.
	InvariantValue InvariantKind = "value"
	// InvariantOrder: the list at this path is ordered by the API's contract,
	// so a replay compares the positions of its elements and not only the set.
	InvariantOrder InvariantKind = "order"
)

// Invariant is one thing a pack declares a replay may compare beyond presence
// and type.
type Invariant struct {
	// Operation is the upstream operation, spelled as the route declares it
	// ("instance/v1/API.CreateServer"). A replay joins on this string, and an
	// invariant naming an operation no route serves fails
	// TestEveryReplayInvariantNamesAServedOperation (internal/cli, where every
	// pack is mounted) rather than sitting unread.
	Operation string
	// Path is the field's dotted path in the response, with "[]" for a list
	// element, exactly as internal/transcript walks a body. For
	// [InvariantOrder] it addresses the values whose sequence is the contract:
	// "server.public_ips[].id" is the ordered sequence of identifiers, where
	// "server.public_ips" alone would say only that a list is there.
	//
	// A "*" segment matches exactly one segment, the same rule
	// [FieldDecline.Path] states, for dictionaries whose keys are data.
	Path string
	Kind InvariantKind
	// Reason is one line, present tense, saying what makes this comparable
	// where the neighbouring fields are not. It faces the guard
	// [UnexplainedInvariants] applies, which is Declined()'s own.
	Reason string
}

// Invariable is implemented by a pack that declares replay invariants.
// Optional, in the manner of [FieldDecliner] and machine.Capable: a pack with
// nothing to declare implements nothing, and [InvariantsOf] answers nil for it,
// so absence reads as "compare presence and type only" rather than as an error.
type Invariable interface {
	ReplayInvariants() []Invariant
}

// InvariantsOf returns what a pack declares comparable, or nil for a pack that
// declares nothing.
func InvariantsOf(p Pack) []Invariant {
	if inv, ok := p.(Invariable); ok {
		return inv.ReplayInvariants()
	}
	return nil
}

// Matches reports whether this invariant covers the field at path within
// operation. The segment rule is [FieldDecline.Matches]'s, shared rather than
// written twice: two spellings of "does this path match" would answer
// differently the day one of them learned a case, and the gate that excuses a
// field and the replay that compares one would then disagree about which field
// they were talking about.
func (i Invariant) Matches(operation, path string) bool {
	return i.Operation == operation && pathMatches(i.Path, path)
}

// pathMatches compares a declared path against an observed one, segment by
// segment, with "*" standing for exactly one segment — never zero, never
// several, or a declaration written for "things.*.limits.local" would also
// cover "things.limits.local" and every deeper field a future recording
// learns, widening a narrow decision silently.
//
// TestAWildcardSegmentMatchesExactlyOneSegment fails without this.
func pathMatches(want, got string) bool {
	wantParts := strings.Split(want, ".")
	gotParts := strings.Split(got, ".")
	if len(wantParts) != len(gotParts) {
		return false
	}
	for i := range wantParts {
		if wantParts[i] != "*" && wantParts[i] != gotParts[i] {
			return false
		}
	}
	return true
}

// UnexplainedInvariants reports invariants whose reason carries no decision,
// under the guard [UnexplainedDeclines] applies one level up: an empty reason,
// a known placeholder, a clause too short to hold an argument. The identifiers
// come back as "operation path" so the caller can name the offender.
//
// A declaration also has to name a kind this package knows: an unknown kind is
// a comparison nobody implements, which would read as "checked" and check
// nothing — the exact shape of defect CLAUDE.md's "un commentaire n'est pas un
// contrôle" is about.
//
// TestAnInvariantWithoutAUsableReasonIsRefused fails without this.
func UnexplainedInvariants(invariants []Invariant) []string {
	var out []string
	for _, i := range invariants {
		if carriesNoDecision(i.Reason) || (i.Kind != InvariantValue && i.Kind != InvariantOrder) {
			out = append(out, i.Operation+" "+i.Path)
		}
	}
	sort.Strings(out)
	return out
}

// DuplicateInvariants reports a field declared more than once for the same
// kind, for the reason [DuplicateFieldDeclines] exists: two entries for one
// field mean two reasons for one decision, and whichever consumer walks the
// slice prints a count that disagrees with the set.
func DuplicateInvariants(invariants []Invariant) []string {
	seen := map[string]int{}
	for _, i := range invariants {
		seen[i.Operation+" "+i.Path+" "+string(i.Kind)]++
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
