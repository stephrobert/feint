package contract

import "strings"

// Naming the operation a *recorded* call addressed is a different question from
// naming the one a *mounted route* declares, and only the contract can answer
// it.
//
// emulator.Table answers the second: it resolves a request against what the
// packs serve. That is what `feint proxy` uses, and it is why a transcript
// carries an empty operation for every call the emulator does not serve — which
// is precisely the population #74 ranks. A declined operation has no route, so
// nothing in this repository could put a name on the calls a real client made
// to it, and "declined, and a client called it anyway" was unanswerable.
//
// The contract can, because the provider's own document states the method and
// the templated path of every operation, served or not: 236 for Outscale, 374
// for Exoscale, 370 for Scaleway. This is that lookup, and it is deliberately
// the provider's own table rather than a set of rules about how each dialect
// spells an identifier.

// OperationAt names the operation a concrete request addresses, and reports
// whether the document describes one there.
//
// path is the whole request path as a recording carries it, PathPrefix
// included: "/api/v1/ReadVms", "/v2/instance/{some id}:start",
// "/instance/v1/zones/fr-par-1/servers".
//
// When several templates match, the one with the fewest wildcard segments wins
// — "/instance/{id}:start" over "/instance/{id}" for a path ending in ":start",
// and a literal segment over a wildcard everywhere else. Ties break on the
// operation name so two runs over one recording agree, which matters because
// the caller counts what this returns.
func (d *Doc) OperationAt(method, path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, d.PathPrefix)
	if !ok {
		return "", false
	}
	if rest == "" {
		rest = "/"
	}
	got := strings.Split(strings.TrimSuffix(rest, "/"), "/")

	var best string
	bestWildcards := -1
	for name, op := range d.Operations {
		if !strings.EqualFold(op.Method, method) {
			continue
		}
		want := strings.Split(strings.TrimSuffix(op.Path, "/"), "/")
		if len(want) != len(got) {
			continue
		}
		wildcards, matched := matchSegments(want, got)
		if !matched {
			continue
		}
		if bestWildcards < 0 || wildcards < bestWildcards || (wildcards == bestWildcards && name < best) {
			best, bestWildcards = name, wildcards
		}
	}
	return best, best != ""
}

// matchSegments compares a templated path with a concrete one and counts the
// segments matched by a placeholder rather than literally.
func matchSegments(want, got []string) (wildcards int, matched bool) {
	for i := range want {
		hit, wild := matchSegment(want[i], got[i])
		if !hit {
			return 0, false
		}
		if wild {
			wildcards++
		}
	}
	return wildcards, true
}

// matchSegment compares one templated segment with one concrete segment.
//
// A segment is not always either a literal or a whole placeholder: Exoscale
// writes the verb inside the identifier's own segment, "{id}:add-source", which
// is the same shape emulator.Table calls an action suffix and handles with an
// extension of its own. So the match is literal-by-literal, with a placeholder
// standing for a run of at least one character.
//
// At least one, never zero, and that is the rule worth stating: a placeholder
// that matched the empty string would let "/instance/:start" name the same
// operation as a real identifier, and the count this feeds is a ranking of what
// to implement.
func matchSegment(want, got string) (matched, wildcard bool) {
	if !strings.Contains(want, "{") {
		return want == got, false
	}
	rest := got
	for {
		open := strings.Index(want, "{")
		if open < 0 {
			return rest == want, true
		}
		literal := want[:open]
		if !strings.HasPrefix(rest, literal) {
			return false, true
		}
		rest = rest[len(literal):]
		closed := strings.Index(want[open:], "}")
		if closed < 0 {
			// A malformed template. Refused rather than guessed, so a broken
			// artefact reads as "no operation there" instead of matching
			// everything that reaches it.
			return false, true
		}
		want = want[open+closed+1:]

		next := want
		if i := strings.Index(want, "{"); i >= 0 {
			next = want[:i]
		}
		if next == "" {
			// The placeholder runs to the end of the segment.
			return rest != "", true
		}
		at := strings.Index(rest, next)
		if at < 1 {
			return false, true
		}
		rest = rest[at:]
	}
}
