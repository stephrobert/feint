package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stackPaths gives the four paths the stack checks read, rooted in the
// repository.
func stackPaths(t *testing.T) (workflow, root, stacks, script string) {
	t.Helper()
	repo := repoRoot(t)
	return filepath.Join(repo, conformanceWorkflow),
		filepath.Join(repo, conformanceRoot),
		filepath.Join(repo, stacksRoot),
		filepath.Join(repo, stacksScript)
}

// The population both checks judge is not empty, and both halves of the
// judgement run here.
//
// stackProofProblems answers nothing when examples/stacks is absent, because
// `feint docs` also regenerates the README of somebody who installed the binary
// and has no such directory. That tolerance is exactly the shape of a check that
// stops measuring when its subject moves, so the subject is asserted here rather
// than assumed: this repository has stacks, CI applies some of them, and the
// list of exceptions is smaller than the list of stacks.
func TestTheStackChecksHaveASubjectToMeasure(t *testing.T) {
	workflow, _, stacks, script := stackPaths(t)

	dirs, err := stackDirs(stacks)
	if err != nil {
		t.Fatalf("list the stacks: %v", err)
	}
	if len(dirs) < 3 {
		t.Fatalf("only %d stack(s) under %s: the listing is broken, not the stacks", len(dirs), stacksRoot)
	}

	applied, err := stacksAppliedInCI(script, workflow)
	if err != nil {
		t.Fatalf("read which stacks CI applies: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("no `run_stack` line is read from tools/conformance/stacks.sh: every stack would " +
			"need a declaration, which is a check measuring its own reader")
	}
	if len(stacksRunByHand) >= len(dirs) {
		t.Fatalf("%d of %d stacks are declared as run by hand: an exception list as long as the "+
			"population is not an exception list", len(stacksRunByHand), len(dirs))
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
	workflow, _, stacks, script := stackPaths(t)

	if problems := undeclaredStacks(stacks, script, workflow); len(problems) != 0 {
		t.Fatalf("the repository does not satisfy its own rule:\n  %s", strings.Join(problems, "\n  "))
	}

	body, err := os.ReadFile(script)
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

	problems := undeclaredStacks(stacks, copied, workflow)
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
	stacksRunByHand = []stackException{{Stack: "exoscale", Reason: "   "}}
	problems = undeclaredStacks(stacks, script, workflow)
	if len(problems) == 0 {
		t.Fatal("a declaration with an empty reason excused a stack anyway")
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
	workflow, _, stacks, script := stackPaths(t)
	restore := stacksRunByHand
	t.Cleanup(func() { stacksRunByHand = restore })

	stacksRunByHand = append(append([]stackException{}, restore...),
		stackException{Stack: "kubernetes", Reason: "a stack that was never here"})
	problems := undeclaredStacks(stacks, script, workflow)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "kubernetes") {
		t.Errorf("a declaration for a stack that does not exist was accepted:\n  %s",
			strings.Join(problems, "\n  "))
	}

	// The other direction, and the one that reads like evidence while being
	// false: the stack is applied on every pull request and the list still says
	// it is run by hand.
	stacksRunByHand = []stackException{{Stack: "scaleway", Reason: "stale: CI applies it"}}
	problems = undeclaredStacks(stacks, script, workflow)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "scaleway") {
		t.Errorf("a declaration survived CI starting to apply the stack it excuses:\n  %s",
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
	workflow, root, stacks, script := stackPaths(t)

	pins, err := providerPinsOfRepository(workflow, root, stacks, script)
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
		if !strings.Contains(rendered, "`"+stacksRoot+"/"+e.Stack+"` —") {
			t.Errorf("the page prints no reason for %s/%s:\n%s", stacksRoot, e.Stack, rendered)
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
