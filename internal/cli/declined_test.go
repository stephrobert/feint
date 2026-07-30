package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// Every refusal in every pack states its reason.
//
// This is the assertion the interface change exists for. Without it a pack could
// satisfy []emulator.Decline with Reason: "" on every entry, the report would
// print a column of blanks, and the change would have moved the problem instead
// of solving it: the reason would still be readable only in the source.
//
// It lives here rather than in each pack because a check copied into three packs
// is a check the fourth pack will not have.
func TestEveryDeclinedOperationSaysWhy(t *testing.T) {
	env := &emulator.Env{Store: store.New()}
	packs := []emulator.Pack{scaleway.New(env), outscale.New(env), exoscale.New(env)}

	total := 0
	for _, p := range packs {
		declined := p.Declined()
		total += len(declined)
		if unexplained := emulator.UnexplainedDeclines(declined); len(unexplained) > 0 {
			for _, op := range unexplained {
				t.Errorf("%s declines %s with no reason", p.Name(), op)
			}
		}
	}
	if total == 0 {
		t.Fatal("no pack declines anything: this test would pass on an empty repository")
	}
	t.Logf("%d refusals, each with a reason", total)
}

// One operation declined twice is not a style problem. `feint coverage` builds a
// map and keeps the last reason; docs/routes.md walks the slice and prints the
// operation twice with both reasons, so the two documents disagree and the count
// in the heading is wrong. An adversarial audit reproduced exactly that, which is
// why this is asserted rather than assumed.
func TestNoOperationIsDeclinedTwice(t *testing.T) {
	env := &emulator.Env{Store: store.New()}
	for _, p := range []emulator.Pack{scaleway.New(env), outscale.New(env), exoscale.New(env)} {
		if dup := emulator.DuplicateDeclines(p.Declined()); len(dup) > 0 {
			t.Errorf("%s declines these more than once: %v", p.Name(), dup)
		}
	}
}

// The guard's own limits, stated as a test so nobody reads it as proving more
// than it does. It refuses the degenerate cases; it cannot tell a well-written
// reason from a well-written wrong one.
func TestTheGuardRefusesWhatLooksLikeAReasonAndIsNot(t *testing.T) {
	for _, bad := range []string{"", "   ", "TODO", "-", "x", "n/a", "out of scope", "see above", "not supported yet"} {
		if got := emulator.UnexplainedDeclines([]emulator.Decline{{Operation: "a/API.One", Reason: bad}}); len(got) != 1 {
			t.Errorf("the guard accepted %q as a reason", bad)
		}
	}
	// And the accepting half: a real reason must pass, or the guard would only
	// be a way to fail the build.
	ok := "a local emulator has no inventory and cannot invent capacity without lying about it"
	if got := emulator.UnexplainedDeclines([]emulator.Decline{{Operation: "a/API.One", Reason: ok}}); len(got) != 0 {
		t.Errorf("the guard refused a real reason: %v", got)
	}
}

// stubDeclining is a pack whose refusals are broken on purpose.
type stubDeclining struct {
	emulator.Pack
	declines []emulator.Decline
}

func (s stubDeclining) Name() string                 { return "stub" }
func (s stubDeclining) Declined() []emulator.Decline { return s.declines }

// The gate must refuse a pack whose refusals are unusable, and say which.
//
// This is the test the fix shipped without: an audit deleted the block in
// coverage() that consumes this and `go test ./...` stayed green, so nothing
// proved the gate gated.
func TestTheGateRefusesUnusableRefusals(t *testing.T) {
	long := "a local emulator has no inventory and cannot invent one"

	if got := declineProblems(stubDeclining{declines: []emulator.Decline{
		{Operation: "a/API.One", Reason: "TODO"},
	}}); len(got) != 1 {
		t.Errorf("a placeholder reason did not reach the gate: %v", got)
	}
	if got := declineProblems(stubDeclining{declines: []emulator.Decline{
		{Operation: "a/API.One", Reason: long},
		{Operation: "a/API.One", Reason: long},
	}}); len(got) != 1 {
		t.Errorf("a duplicated operation did not reach the gate: %v", got)
	}
	// And the accepting half: a clean pack produces nothing, or the gate would
	// only ever be a way to fail.
	if got := declineProblems(stubDeclining{declines: []emulator.Decline{
		{Operation: "a/API.One", Reason: long},
	}}); len(got) != 0 {
		t.Errorf("a clean pack was refused: %v", got)
	}
}

// coverage() must refuse a pack whose refusals are unusable, and this proves the
// call site rather than the function.
//
// The distinction is the whole reason this test exists: declineProblems had its
// own test from the start, and deleting the line that called it left the suite
// green through four audits. What made that untestable was the hardwired pack
// list in newServer(); packsFor is the seam, and this is the test it was added
// for.
func TestCoverageRefusesAPackWithUnusableRefusals(t *testing.T) {
	original := packsFor
	t.Cleanup(func() { packsFor = original })

	packsFor = func(env *emulator.Env) []emulator.Pack {
		return []emulator.Pack{brokenPack{Pack: scaleway.New(env)}}
	}

	// The gate runs after the SDK scan, so the scan has to succeed for the gate
	// to be reached at all. Skipped rather than failed without a checkout: the
	// clones are fetched by `mise run upstream:sync` and are not versioned.
	sdk := filepath.Join("..", "..", ".upstream", "scaleway-sdk-go")
	if _, err := os.Stat(sdk); err != nil {
		t.Skip("no SDK checkout: mise run upstream:sync")
	}

	var out, errOut strings.Builder
	rc := coverage([]string{"--provider", "scaleway", "--sdk", sdk}, &out, &errOut)

	if rc != exitError {
		t.Fatalf("coverage exited %d on a pack declining without a reason, want %d", rc, exitError)
	}
	if !strings.Contains(errOut.String(), "no usable reason") {
		t.Fatalf("the refusal was not named: %q", errOut.String())
	}
}

// brokenPack is a real pack with one unusable refusal grafted on.
type brokenPack struct{ emulator.Pack }

func (b brokenPack) Declined() []emulator.Decline {
	return []emulator.Decline{{Operation: "instance/v1/API.GetDashboard", Reason: "TODO"}}
}
