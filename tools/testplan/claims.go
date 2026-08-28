package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// What an `Unproven` sentence rests on, so that a sentence which has gone false
// reddens instead of being believed.
//
// THE DEFECT THIS IS. On #567 a rule said, in its own `Unproven`, that "a change
// to the stored shape is proved across a restart by `mise run
// conformance:environment`". That suite is tools/conformance/environment/up.sh,
// and it contains **zero** occurrences of `snapshot` and of `--state`: it starts
// an emulator, asserts its ready conditions, stops it, and its fixture declares
// no infrastructure at all. The sentence was false on the day it was written and
// it was greppable on that day — which is the whole argument for this file. Two
// of the five errors #588 counts are contradicted by a rule's own prose, and
// prose is the one part of this table nothing was reading.
//
// So a sentence that names an artefact of this repository must say what makes it
// true, and that "what" is read off the artefact rather than believed.
//
// WHAT THIS DOES NOT DO, because a mechanism that oversells itself is the defect
// it exists to catch:
//
//   - It cannot judge a sentence that names no artefact. "a bridge is a
//     different verdict for isolation alone" (#574, wrong: three declared
//     capabilities turn on the mode) names nothing greppable, and no arrangement
//     of this file would have caught it. It was corrected by hand.
//   - A claim can be made vacuous by choosing a token every file contains. What
//     stops that is not this code: it is that writing the claim at all means
//     opening the artefact, which is exactly the step whose absence produced the
//     false sentence above.
//   - Containment is whole-file. A claim that leg.sh names a script does not
//     prove the script sits in the arm the sentence means. Where that mattered,
//     the token was chosen to include its context (`FEINT_FIELD_GATE=1
//     tools/conformance/score.sh`).
//   - One claim discharges a sentence, even one naming two artefacts. The
//     forcing rule is a prompt, not a proof.
type claim struct {
	// About is the fragment of the rule's `Unproven` this holds up. It must be
	// a substring of it, so that editing the sentence out from under a claim is
	// itself a failure rather than a silent orphan.
	About string
	// In is the repository-relative artefact whose text decides the claim.
	In string
	// Shows are tokens that must appear in In. Whitespace is collapsed on both
	// sides before matching, so gofmt realigning a struct literal does not turn
	// a true claim red.
	Shows []string
	// Absent are tokens that must not appear in In. This is the half that
	// catches the #567 sentence, and the half that will redden the day somebody
	// makes it true — at which point the sentence is what needs rewriting.
	Absent []string
}

// The artefacts a sentence names, extracted syntactically.
//
// Backticks are not required. They were the first shape of this and they hand
// out a dodge: drop the backticks and the sentence names nothing this can see.
// A path is a path in prose too.
var (
	artefactPath = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*\.(?:sh|go|py|json|ya?ml|toml|md)\b`)
	artefactTask = regexp.MustCompile(`mise run ([a-z0-9][a-z0-9:_-]*)`)
	artefactBare = regexp.MustCompile("`([a-z0-9][a-z0-9-]*:[a-z0-9][a-z0-9-]*)`")
)

// artefactsNamed lists the files and mise tasks a sentence names.
func artefactsNamed(text string) []string {
	var named []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			named = append(named, s)
		}
	}
	for _, m := range artefactPath.FindAllString(text, -1) {
		add(m)
	}
	for _, re := range []*regexp.Regexp{artefactTask, artefactBare} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			add(m[1])
		}
	}
	return named
}

// collapse folds every run of whitespace to one space, so a token may be
// written the way a reader would say it rather than the way gofmt aligned it.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// checkClaims reads every claim against the artefact it names and returns what
// no longer holds. An empty result is the only acceptable one; the caller is
// TestEveryUnprovenClaimHoldsAgainstTheArtefactItNames, and
// TestTheClaimCheckerFindsAClaimThatHasGoneFalse plants each kind of defect to
// prove this can find them at all.
func checkClaims(root string, rs []rule) []string {
	var problems []string
	for _, r := range rs {
		if r.Unproven == prepushIsTheWholeGate {
			if len(r.Cites) > 0 {
				problems = append(problems, fmt.Sprintf(
					"rule %q leaves Unproven empty and still cites %d artefact(s): a claim with no "+
						"sentence holds nothing up", r.Path, len(r.Cites)))
			}
			continue
		}
		if named := artefactsNamed(r.Unproven); len(named) > 0 && len(r.Cites) == 0 {
			problems = append(problems, fmt.Sprintf(
				"rule %q says something about %s and cites nothing. An Unproven sentence that names "+
					"an artefact must say what makes it true: the one that did not (#567) claimed a "+
					"suite proved a stored shape across a restart, and that suite saves no state",
				r.Path, strings.Join(named, ", ")))
		}
		for _, c := range r.Cites {
			problems = append(problems, checkClaim(root, r, c)...)
		}
	}
	return problems
}

func checkClaim(root string, r rule, c claim) []string {
	var problems []string
	if !strings.Contains(r.Unproven, c.About) {
		return []string{fmt.Sprintf(
			"rule %q cites %q as holding up %q, which is not a fragment of its Unproven any more: "+
				"the sentence moved and the claim stayed", r.Path, c.In, c.About)}
	}
	if len(c.Shows)+len(c.Absent) == 0 {
		return []string{fmt.Sprintf(
			"rule %q cites %s and asks nothing of it; a citation that reads nothing is a comment",
			r.Path, c.In)}
	}
	body, err := os.ReadFile(filepath.Join(root, c.In)) //nolint:gosec // a path from this repository's own table
	if err != nil {
		return []string{fmt.Sprintf(
			"rule %q rests on %s, which cannot be read: %v", r.Path, c.In, err)}
	}
	text := collapse(string(body))
	for _, token := range c.Shows {
		if !strings.Contains(text, collapse(token)) {
			problems = append(problems, fmt.Sprintf(
				"rule %q says %q, and %s does not contain %q — the sentence rests on something that "+
					"is no longer there", r.Path, c.About, c.In, token))
		}
	}
	for _, token := range c.Absent {
		if strings.Contains(text, collapse(token)) {
			problems = append(problems, fmt.Sprintf(
				"rule %q says %q, and %s now contains %q — the artefact grew what the sentence says "+
					"it lacks, so the sentence is what needs rewriting", r.Path, c.About, c.In, token))
		}
	}
	return problems
}
