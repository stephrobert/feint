package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stackPaths gives the paths the stack checks read, rooted in the repository.
//
// The sources carry an absolute Root and Script — a test may hand either a copy
// — and the repository-relative CIRef, because that is the string a workflow can
// actually name.
func stackPaths(t *testing.T) (workflow, root string, sources []exampleSource) {
	t.Helper()
	repo := repoRoot(t)
	for _, source := range exampleSources() {
		sources = append(sources, exampleSource{
			Family: source.Family,
			Root:   filepath.Join(repo, source.Root),
			Script: filepath.Join(repo, source.Script),
			CIRef:  source.CIRef,
		})
	}
	return filepath.Join(repo, conformanceWorkflow), filepath.Join(repo, conformanceRoot), sources
}

// withRoot answers the source whose CIRef is the given repository-relative
// script, so a test can name one family among the several.
func withRoot(t *testing.T, sources []exampleSource, ciRef string) exampleSource {
	t.Helper()
	for _, source := range sources {
		if source.CIRef == ciRef {
			return source
		}
	}
	t.Fatalf("no example source runs %s: this test is measuring a list it does not understand", ciRef)
	return exampleSource{}
}

// The population both checks judge is not empty, and both halves of the
// judgement run here.
//
// stackProofProblems answers nothing when the example roots are absent, because
// `feint docs` also regenerates the README of somebody who installed the binary
// and has no such directory. That tolerance is exactly the shape of a check that
// stops measuring when its subject moves, so the subject is asserted here rather
// than assumed: this repository has stacks, CI applies some of them, and the
// list of exceptions is smaller than the list of stacks.
func TestTheStackChecksHaveASubjectToMeasure(t *testing.T) {
	workflow, _, sources := stackPaths(t)

	total := 0
	for _, source := range sources {
		dirs, err := stackDirs(source.Root)
		if err != nil {
			t.Fatalf("list the examples under %s: %v", source.Root, err)
		}
		total += len(dirs)
	}
	if total < 4 {
		t.Fatalf("only %d example director(ies) across %d roots: the listing is broken, not the "+
			"examples", total, len(sources))
	}

	applied, err := stacksAppliedInCI(sources, workflow)
	if err != nil {
		t.Fatalf("read which stacks CI applies: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("no `run_stack` line is read from any example suite: every stack would " +
			"need a declaration, which is a check measuring its own reader")
	}
	// Both families are really in the population. Adding the quickstart root and
	// having nothing under it read would be the same check with a longer list.
	for _, source := range sources {
		found := false
		for dir := range applied {
			if strings.HasPrefix(dir, source.Family+"/") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is declared as an example root and CI applies nothing under it: the "+
				"refusals below would judge a family nobody runs", source.Family)
		}
	}
	if len(stacksRunByHand) >= total {
		t.Fatalf("%d of %d examples are declared as run by hand: an exception list as long as the "+
			"population is not an exception list", len(stacksRunByHand), total)
	}
}

// A stack CI does not apply is declared, with a reason.
//
// The accepting half is the repository as it stands; the refusing half removes
// the invocation of a stack CI does apply, which is the shape of the mistake
// this exists for — somebody adds a fourth stack and forgets to wire it into
// tools/conformance/stacks.sh, and the generated table prints `no` for it as if
// that were a decision.
func TestAStackCIDoesNotApplyIsDeclaredWithAReason(t *testing.T) {
	workflow, _, sources := stackPaths(t)

	if problems := undeclaredStacks(sources, workflow); len(problems) != 0 {
		t.Fatalf("the repository does not satisfy its own rule:\n  %s", strings.Join(problems, "\n  "))
	}

	stacks := withRoot(t, sources, stacksScript)
	body, err := os.ReadFile(stacks.Script)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.Replace(string(body), "run_stack outscale", "true # run_stack outscale", 1)
	if trimmed == string(body) {
		t.Fatal("tools/conformance/stacks.sh no longer applies the outscale stack: either it was " +
			"removed and a declaration must have appeared, or this test is measuring a file it " +
			"does not understand")
	}
	copied := filepath.Join(t.TempDir(), "stacks.sh")
	if err := os.WriteFile(copied, []byte(trimmed), 0o600); err != nil {
		t.Fatal(err)
	}

	loosened := append([]exampleSource{}, sources...)
	for i := range loosened {
		if loosened[i].CIRef == stacksScript {
			loosened[i].Script = copied
		}
	}
	problems := undeclaredStacks(loosened, workflow)
	if len(problems) == 0 {
		t.Fatal("a stack no `run_stack` line applies and nothing declares passed: the table " +
			"would print `no` for it and read as a decision")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "outscale") {
		t.Errorf("the refusal does not name the stack, which is the one thing needed to fix it:\n  %s",
			strings.Join(problems, "\n  "))
	}

	// And a declaration with no reason is refused too: "not applied" and "not
	// applied because X" are different facts.
	restore := stacksRunByHand
	t.Cleanup(func() { stacksRunByHand = restore })
	stacksRunByHand = []stackException{{Root: stacks.Family, Stack: "exoscale", Reason: "   "}}
	problems = undeclaredStacks(sources, workflow)
	if len(problems) == 0 {
		t.Fatal("a declaration with an empty reason excused a stack anyway")
	}
}

// The same rule reaches the quickstart family, which is the whole reason
// exampleSources is a list (#593).
//
// A quickstart nobody applies is the README rotting, and it is the example most
// readers run. The mutation is the one somebody will really make: a quickstart
// directory added and never wired into its suite.
func TestAQuickstartCIDoesNotApplyIsRefusedToo(t *testing.T) {
	workflow, _, sources := stackPaths(t)
	quickstart := withRoot(t, sources, quickstartScript)

	body, err := os.ReadFile(quickstart.Script)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.Replace(string(body), "run_stack "+quickstartLead,
		"true # run_stack "+quickstartLead, 1)
	if trimmed == string(body) {
		t.Fatalf("%s no longer applies the %s quickstart: either it was removed and a declaration "+
			"must have appeared, or this test is measuring a file it does not understand",
			quickstartScript, quickstartLead)
	}
	copied := filepath.Join(t.TempDir(), "quickstart.sh")
	if err := os.WriteFile(copied, []byte(trimmed), 0o600); err != nil {
		t.Fatal(err)
	}
	loosened := append([]exampleSource{}, sources...)
	for i := range loosened {
		if loosened[i].CIRef == quickstartScript {
			loosened[i].Script = copied
		}
	}
	problems := undeclaredStacks(loosened, workflow)
	if len(problems) == 0 {
		t.Fatalf("the %s quickstart is applied by nothing and declared by nothing, and it passed: "+
			"the first door of this project would rot with no gate saying so", quickstartLead)
	}
	if !strings.Contains(strings.Join(problems, "\n"), quickstartRoot+"/"+quickstartLead) {
		t.Errorf("the refusal names neither the root nor the example:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// A declaration that excuses nothing is stale, in both the ways it can be.
//
// This is the half that keeps the list from becoming a place reasons go to be
// forgotten: a name that no longer exists, and a name CI has started applying.
// Without it the first entry somebody writes outlives the situation it
// describes, which is the defect CLAUDE.md names as the most expensive one
// measured on this repository.
func TestADeclarationThatExcusesNothingIsStale(t *testing.T) {
	workflow, _, sources := stackPaths(t)
	stacks := withRoot(t, sources, stacksScript)
	restore := stacksRunByHand
	t.Cleanup(func() { stacksRunByHand = restore })

	stacksRunByHand = append(append([]stackException{}, restore...),
		stackException{Root: stacks.Family, Stack: "kubernetes", Reason: "a stack that was never here"})
	problems := undeclaredStacks(sources, workflow)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "kubernetes") {
		t.Errorf("a declaration for a stack that does not exist was accepted:\n  %s",
			strings.Join(problems, "\n  "))
	}

	// The other direction, and the one that reads like evidence while being
	// false: the stack is applied on every pull request and the list still says
	// it is run by hand.
	stacksRunByHand = []stackException{{Root: stacks.Family, Stack: "scaleway", Reason: "stale: CI applies it"}}
	problems = undeclaredStacks(sources, workflow)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "scaleway") {
		t.Errorf("a declaration survived CI starting to apply the stack it excuses:\n  %s",
			strings.Join(problems, "\n  "))
	}

	// And the third way, which only exists because there are two roots: a
	// declaration naming a family nothing walks. Keyed by name alone it would
	// have silently excused the stack of the same name in the other root.
	stacksRunByHand = []stackException{{Root: "examples/nowhere", Stack: "scaleway", Reason: "a root nothing reads"}}
	problems = undeclaredStacks(sources, workflow)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "examples/nowhere") {
		t.Errorf("a declaration under a root no source names was accepted:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// A stack CI applies names the provider versions it accepts.
//
// `examples/stacks/outscale/modules/net` declared no constraint at all and was
// applied on every pull request, which the generated table of #325 exposed on
// its first day: `terraform init -upgrade` resolves an unconstrained provider
// from the whole registry on every run, so the apply proves the emulator
// answered whatever was newest that morning and nothing that can be replayed.
// The constraint does not have to be exact — the page says plainly what a floor
// is worth — it has to exist.
func TestAStackAppliedInCIPinsTheProviderThatAnswered(t *testing.T) {
	workflow, root, sources := stackPaths(t)

	pins, err := providerPinsOfRepository(workflow, root, sources)
	if err != nil {
		t.Fatalf("read the provider constraints: %v", err)
	}
	// The subject: rows that are applied at all, and rows that are not. A run
	// where every row were undriven would satisfy the check while measuring
	// nothing.
	var driven, undriven int
	for _, pin := range pins {
		if pin.Driven {
			driven++
			continue
		}
		undriven++
	}
	if driven == 0 || undriven == 0 {
		t.Fatalf("%d applied and %d unapplied provider entries: the reader is broken, not the stacks",
			driven, undriven)
	}
	if problems := unconstrainedAppliedPins(pins); len(problems) != 0 {
		t.Fatalf("a stack CI applies pins nothing:\n  %s", strings.Join(problems, "\n  "))
	}

	// The quickstart examples are in that population rather than beside it: the
	// first thing a reader applies must pin the provider that answered as much
	// as the qualification stack does.
	quickstartPins := 0
	for _, pin := range pins {
		if strings.Contains(pin.Dir, quickstartRoot+"/") && pin.Driven {
			quickstartPins++
		}
	}
	if quickstartPins == 0 {
		t.Errorf("no applied provider entry under %s: the quickstart is outside the population "+
			"this check judges, which is the hole it exists to close", quickstartRoot)
	}

	// The refusing half, on the entry that really was like this until the
	// table of #325 named it: the module's constraint removed, nothing else.
	loosened := make([]providerPin, len(pins))
	copy(loosened, pins)
	found := false
	for i := range loosened {
		if strings.HasSuffix(loosened[i].Dir, "/outscale/modules/net") {
			loosened[i].Constraint = ""
			found = true
		}
	}
	if !found {
		t.Fatalf("no row for %s/outscale/modules/net: this test is measuring a tree it does not "+
			"understand", stacksRoot)
	}
	problems := unconstrainedAppliedPins(loosened)
	if len(problems) == 0 {
		t.Fatal("a module applied on every pull request with no version constraint passed")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "modules/net") {
		t.Errorf("the refusal does not name the directory to fix:\n  %s", strings.Join(problems, "\n  "))
	}
}

// The generated page prints the declaration it is checked against, so a reader
// meets the reason rather than a bare `no`.
func TestThePageCarriesTheReasonAStackIsNotApplied(t *testing.T) {
	rendered := provedPage(t)
	for _, e := range stacksRunByHand {
		if !strings.Contains(rendered, "`"+e.Root+"/"+e.Stack+"` —") {
			t.Errorf("the page prints no reason for %s/%s:\n%s", e.Root, e.Stack, rendered)
		}
	}
	// And the reason is the declared one rather than a second wording of it.
	if len(stacksRunByHand) > 0 {
		first := strings.Fields(stacksRunByHand[0].Reason)[0]
		if !strings.Contains(rendered, first) {
			t.Errorf("the page's reason is not the declared one, which is two claims to keep in step")
		}
	}
}
