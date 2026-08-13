package emulator

import (
	"sort"
	"strings"
)

// A refusal is a decision, and a decision nobody can read is indistinguishable
// from an oversight.
//
// Declined() used to be a list of operation names, with the reason living in a
// comment above each group. That reason was real — the groups were written with
// care — and invisible to everything that consumes the list: the coverage report,
// `feint coverage`, the generated route reference. A reviewer looking at the
// report saw a count of refusals and had to open three provider files to learn
// why any of them was refused.
//
// So the reason travels with the operation. What was prose for a reader of the
// code becomes data the report prints, and the difference between "out of scope"
// and "not triaged yet" stops depending on somebody opening the right file.

// Decline is an upstream operation a pack knowingly does not serve, and why.
type Decline struct {
	// Operation is the upstream name, spelled exactly as Route.Operation spells
	// it: the coverage report joins on this string.
	Operation string
	// Reason is one line, in the present tense, saying what makes this operation
	// out of scope rather than merely undone. It is printed next to the
	// operation, so it has to read on its own — "see above" means nothing in a
	// report.
	Reason string
}

// Because groups the operations refused for one reason.
//
// The shape exists so the reason is written once per group rather than repeated
// per line, which is how 117 refusals stayed readable when they moved from
// comments into data:
//
//	slices.Concat(
//	    emulator.Because("the metadata service answers inside the machine, on a link-local
//	                      address, so serving it would mean a listener in every guest",
//	        "<product>/v1/MetadataAPI.GetMetadata",
//	        "<product>/v1/MetadataAPI.GetUserData"),
//	)
//
// The placeholder names are deliberate: an example in internal/core must not
// teach a provider's vocabulary to the neutral core.
//
// The longer argument stays in a comment above the call. Reason is the sentence
// the report prints; the comment is for whoever revisits the decision.
func Because(reason string, operations ...string) []Decline {
	out := make([]Decline, 0, len(operations))
	for _, op := range operations {
		out = append(out, Decline{Operation: op, Reason: reason})
	}
	return out
}

// DeclinedOperations is the operation names alone, for a caller that only needs
// the set — the drift join, for instance.
func DeclinedOperations(declined []Decline) []string {
	out := make([]string, 0, len(declined))
	for _, d := range declined {
		out = append(out, d.Operation)
	}
	return out
}

// placeholders are the strings that satisfy "not empty" and say nothing. An
// audit defeated the first version of this check with "TODO" in under a minute.
var placeholders = map[string]bool{
	"todo": true, "tbd": true, "fixme": true, "n/a": true, "na": true,
	"-": true, "--": true, "x": true, "?": true, "none": true, "unknown": true,
	"out of scope": true, "not supported": true, "see above": true,
}

// placeholderStems are the tokens that carry no argument wherever they appear.
// Half a reason made of them is not a reason.
var placeholderStems = map[string]bool{
	"todo": true, "tbd": true, "fixme": true, "xxx": true, "wip": true,
	"later": true, "someday": true, "decide": true, "decided": true,
}

// shortReasonWords is where a reason is too brief to carry a waiting word
// innocently. Measured against the fourteen real reasons in this repository: the
// shortest is ten words and none of them contains a stem.
const shortReasonWords = 12

// minReasonWords is the floor, and it is a floor rather than a judgement: four
// words cannot hold a decision, and no amount of counting proves that a longer
// string does. What this stops is the degenerate case; what it cannot stop is a
// reason that reads well and is wrong for its operation, which only a reader
// catches. Said plainly here so nobody mistakes this for a proof of quality.
const minReasonWords = 5

// UnexplainedDeclines reports refusals whose reason does not carry one.
//
// The interface change alone would have delivered nothing: a pack could satisfy
// the new signature with `Reason: ""` on every entry and the report would print a
// column of blanks. Emptiness was the first check; it was not enough. An
// adversarial audit passed the guard with "TODO", with "-", and with "x", which
// is the same defect one layer in — a control that verifies a shape and is read
// as verifying a decision.
//
// So three things fail here: an empty or whitespace reason, a known placeholder,
// and a reason too short to be a clause. The report prints these, the caller's
// test fails on them.
func UnexplainedDeclines(declined []Decline) []string {
	var out []string
	for _, d := range declined {
		if carriesNoDecision(d.Reason) {
			out = append(out, d.Operation)
		}
	}
	return out
}

// carriesNoDecision is the shared judgement on one reason string, so a decline
// of a field faces exactly the same guard as a decline of an operation. Two
// copies of this switch would be two guards one audit hardens and the other
// keeps accepting "TODO".
func carriesNoDecision(raw string) bool {
	reason := strings.ToLower(strings.TrimSpace(strings.Trim(raw, ".:;— -")))
	words := strings.Fields(reason)
	// Token level, not whole string: "TODO TODO TODO TODO TODO" passed the
	// exact-match list and the word count at once, which an audit found in
	// one try. A reason built out of placeholder tokens is a placeholder
	// however many times it repeats.
	stems := 0
	for _, w := range words {
		if placeholderStems[strings.Trim(w, ".,;:!?")] {
			stems++
		}
	}
	switch {
	// A waiting word in a short reason is a note to self; the same word in a
	// developed sentence can be honest prose ("nobody has decided what a
	// later version should answer"). So the length is what separates them,
	// and the ratio catches the shorter repetitions. Neither closes the case
	// entirely: an audit passed six stems drowned in seven other words, and
	// no string check ever will. What actually reviews these sentences is a
	// reader, which is why they are printed into docs/routes.md.
	case reason == "", placeholders[reason],
		len(words) < minReasonWords,
		stems > 0 && len(words) < shortReasonWords,
		stems*2 >= len(words):
		return true
	}
	return false
}

// DuplicateDeclines reports operations declined more than once.
//
// Two entries for one operation is not a style problem: `feint coverage` keeps
// the last reason, because it builds a map, and docs/routes.md prints the
// operation twice with both reasons, because it walks the slice — so the two
// documents disagree and the count in the heading is wrong. An audit reproduced
// exactly that.
func DuplicateDeclines(declined []Decline) []string {
	seen := map[string]int{}
	for _, d := range declined {
		seen[d.Operation]++
	}
	var out []string
	for op, n := range seen {
		if n > 1 {
			out = append(out, op)
		}
	}
	sort.Strings(out)
	return out
}
