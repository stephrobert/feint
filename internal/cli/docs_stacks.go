package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// A stack in this repository is applied on every pull request, or it says why
// not — and a stack that is applied pins the provider that answered.
//
// Both halves came out of the generated table of #325 within a day of it
// landing, and neither was visible before it existed:
//
//  1. `examples/stacks/outscale/modules/net` declared no `version` at all and
//     was applied on every pull request. A stack applied under whatever the
//     registry served that morning proves that the emulator answered
//     *something*, and nothing anybody can reproduce — which is precisely the
//     complaint #325 was filed for, turned inward.
//  2. `examples/stacks/exoscale` is applied by nothing. That one is a decision,
//     and a good one (the published provider splits between the emulator and a
//     paying account), but it was written only in prose: three files say it in
//     three wordings and no check reads any of them. A comment is not a
//     control, so the fourth stack somebody adds and forgets to wire into
//     tools/conformance/stacks.sh would have printed `no` in the table and read
//     as a decision.
//
// So the exception is declared, with its reason and its date, the way CLAUDE.md
// rule 3 has declined operations declared — and the declaration is checked in
// both directions: a stack CI does not apply and nobody declared is refused,
// and a declaration for a stack that does not exist, or that CI does in fact
// apply, is refused as stale. One direction alone is the check that stops
// measuring the day its subject moves.
//
// TestAStackCIDoesNotApplyIsDeclaredWithAReason,
// TestADeclarationThatExcusesNothingIsStale and
// TestAStackAppliedInCIPinsTheProviderThatAnswered fail without them.
//
// One accommodation, and it is the shape this repository has just removed five
// of, so it is worth being exact about why this one is not that. `feint docs`
// regenerates the README of somebody who installed the binary and has no
// examples/stacks directory at all, so stackProofProblems answers "nothing to
// report" there rather than failing a user's regeneration over a directory that
// is not theirs. What stops that from becoming a check that skips itself here
// is TestTheStackChecksHaveASubjectToMeasure, which asserts in this repository
// that the population is non-empty and that both halves of the judgement run.
// The runtime tolerance is for the other repository; the assertion is for this
// one.

// stackException is one example stack the conformance suite does not apply, and
// why.
type stackException struct {
	// Stack is the directory name directly under examples/stacks.
	Stack string
	// Reason says what stops CI applying it, and what is done instead. Empty is
	// refused: "not run" and "not run because X, applied by hand on D" are
	// different facts, and only the second is a decision.
	Reason string
}

// stacksRunByHand is the whole list, and it is deliberately short.
//
// An entry here costs a reader something: it is a stack whose green nobody sees
// on a pull request. The bar is that CI *cannot* apply it, not that applying it
// would be inconvenient.
var stacksRunByHand = []stackException{
	{
		Stack: "exoscale",
		Reason: "the published Exoscale provider builds two clients and only one honours " +
			"`EXOSCALE_API_ENDPOINT`, so an apply splits between the emulator and a paying " +
			"account (docs/limits.md, upstream exoscale/terraform-provider-exoscale#573). " +
			"Running it needs the patched fork, and no gate here clones a third-party " +
			"repository — that would put somebody else's availability in this pipeline. " +
			"Applied by hand on 2026-08-18: 13 resources, empty second plan, clean destroy",
	},
}

// stackDirs lists the example stacks that exist, which is the population both
// checks below judge.
//
// An empty result is an error rather than an empty answer. A walk that finds
// nothing passes every check written over it while measuring none of them.
func stackDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no stack directory under %s: the listing is broken, not the stacks", root)
	}
	sort.Strings(out)
	return out, nil
}

// stackProofProblems is what `feint docs` reports in both modes: the things a
// regeneration will not repair.
//
// It answers nothing when this repository's own directories are not there,
// because `feint docs` also regenerates a README outside it — see the note at
// the top of this file, and TestTheStackChecksHaveASubjectToMeasure, which is
// what keeps that from becoming a check nobody runs.
func stackProofProblems(workflow, root, stacks, script string) []string {
	if _, err := os.Stat(stacks); os.IsNotExist(err) {
		return nil
	}
	problems := undeclaredStacks(stacks, script, workflow)
	pins, err := providerPinsOfRepository(workflow, root, stacks, script)
	if err != nil {
		return append(problems, fmt.Sprintf(
			"cannot read the provider constraints under %s and %s: %v", root, stacks, err))
	}
	return append(problems, unconstrainedAppliedPins(pins)...)
}

// undeclaredStacks names every disagreement between the stacks that exist, the
// stacks CI applies, and the exceptions declared above.
//
// Three refusals, and the second and third are what keep the first from
// becoming decoration:
//
//   - a stack CI does not apply and nobody declared;
//   - a declaration naming a stack that does not exist;
//   - a declaration for a stack CI does apply, or with no reason given.
func undeclaredStacks(root, script, workflow string) []string {
	stacks, err := stackDirs(root)
	if err != nil {
		return []string{err.Error()}
	}
	applied, err := stacksAppliedInCI(script, workflow)
	if err != nil {
		return []string{fmt.Sprintf("cannot read which stacks CI applies: %v", err)}
	}

	declared := map[string]string{}
	var problems []string
	for _, e := range stacksRunByHand {
		if strings.TrimSpace(e.Reason) == "" {
			problems = append(problems, fmt.Sprintf(
				"%s/%s is declared as run by hand with no reason: \"not applied\" and \"not applied "+
					"because X\" are different facts, and only the second is a decision",
				root, e.Stack))
		}
		declared[e.Stack] = e.Reason
	}

	existing := map[string]bool{}
	for _, stack := range stacks {
		existing[stack] = true
		if applied[stack] {
			continue
		}
		if _, ok := declared[stack]; !ok {
			problems = append(problems, fmt.Sprintf(
				"%s/%s is applied by no `run_stack` line in %s and stacksRunByHand in "+
					"internal/cli/docs_stacks.go does not declare why: a stack nobody applies is not a "+
					"proof, and the table would print `no` for it as if that were a decision",
				root, stack, script))
		}
	}

	for _, e := range stacksRunByHand {
		if !existing[e.Stack] {
			problems = append(problems, fmt.Sprintf(
				"stacksRunByHand declares %s/%s and no such stack exists: a declaration that "+
					"excuses nothing is a reason nobody re-reads", root, e.Stack))
			continue
		}
		if applied[e.Stack] {
			problems = append(problems, fmt.Sprintf(
				"stacksRunByHand says %s/%s is run by hand and %s applies it: the reason is stale, "+
					"and it is the kind that survives for months because it reads like evidence",
				root, e.Stack, script))
		}
	}
	sort.Strings(problems)
	return problems
}

// unconstrainedAppliedPins names every provider a suite applies without saying
// which versions it accepts.
//
// The narrow claim, and it is the one #325 was filed over: an unconstrained
// provider is resolved by `terraform init -upgrade` from the whole registry on
// every run, so the run proves the emulator answered whatever was newest that
// morning and nothing that can be replayed. A constraint does not have to be
// exact — the page is explicit about what a floor is worth — it has to exist.
//
// Written over the pins the page itself reads, so a row that reads "not pinned"
// and "applied in CI" in the table cannot exist without this firing.
func unconstrainedAppliedPins(pins []providerPin) []string {
	var problems []string
	for _, pin := range pins {
		if !pin.Driven || strings.TrimSpace(pin.Constraint) != "" {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s declares %s with no version constraint and CI applies it: `terraform init "+
				"-upgrade` resolves it fresh on every run, so nothing here records which provider "+
				"answered — the complaint of #325, turned inward",
			pin.Dir, pin.Source))
	}
	sort.Strings(problems)
	return problems
}

// renderStackExceptions is the paragraph under the table, written from the same
// list the refusals above read.
//
// Both states are printed. An empty list says so rather than printing nothing,
// because a section that disappears when it has nothing to report is a section
// a reader cannot tell from one nobody generated.
func renderStackExceptions() string {
	if len(stacksRunByHand) == 0 {
		return "\nEvery stack under `examples/stacks/` is applied on every pull request.\n"
	}
	var b strings.Builder
	b.WriteString("\nThe stacks the last column says CI does not apply are declared rather than\n")
	b.WriteString("merely absent, so a `no` is a decision somebody wrote down and not a stack\n")
	b.WriteString("nobody wired up:\n\n")
	ordered := append([]stackException{}, stacksRunByHand...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Stack < ordered[j].Stack })
	for _, e := range ordered {
		b.WriteString(wrapBullet(fmt.Sprintf("`%s/%s` — %s.", stacksRoot, e.Stack, e.Reason)))
	}
	return b.String()
}

// wrapBullet renders one list item wrapped the way the prose around it is, so a
// declared reason does not land as a single 500-character line in a file every
// other paragraph of which stops at the same column.
func wrapBullet(text string) string {
	const width = 76
	var b strings.Builder
	column := 0
	prefix := "- "
	for _, word := range strings.Fields(text) {
		switch {
		case column == 0:
			b.WriteString(prefix)
			b.WriteString(word)
			column = len(prefix) + len(word)
			prefix = "  "
		case column+1+len(word) > width:
			b.WriteString("\n  ")
			b.WriteString(word)
			column = 2 + len(word)
		default:
			b.WriteString(" ")
			b.WriteString(word)
			column += 1 + len(word)
		}
	}
	b.WriteString("\n")
	return b.String()
}
