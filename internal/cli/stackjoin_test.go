package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/stack"
)

// stubStackPack is a pack whose routes, refusals and type table a test writes.
//
// It exists so the join can be driven over cases the real packs cannot produce.
// The one that matters: a table row naming an operation its pack has not
// decided about. TestEveryStackOperationIsOneThatPackDecided makes that a
// failing build for the committed tables, so without a stub the branch that
// handles it would never be executed — and an unexecuted branch is a claim, not
// a control.
type stubStackPack struct {
	name     string
	routes   []emulator.Route
	declined []emulator.Decline
	table    map[string]string
}

// Env is part of the Pack interface and no part of this test: a pack that
// cannot say how a client reaches it is still a pack whose type table joins.
func (p stubStackPack) Env(string) emulator.Environment { return emulator.Environment{} }

func (p stubStackPack) Name() string                       { return p.name }
func (p stubStackPack) Routes() []emulator.Route           { return p.routes }
func (p stubStackPack) Declined() []emulator.Decline       { return p.declined }
func (p stubStackPack) StackOperations() map[string]string { return p.table }

// An operation the pack has not decided about is unknown, never served.
//
// The direction matters more than the value: a wrong row — a typo, a row that
// outlived the operation it named — must degrade into "nothing here has decided
// it", which makes the verdict unknown and refuses nothing. The opposite
// default would turn every mistake in a table into a promise that the stack
// will apply.
func TestAnOperationThePackHasNotDecidedIsUnknownNotServed(t *testing.T) {
	p := stubStackPack{
		name:     "stub",
		routes:   []emulator.Route{{Operation: "stub/Client.CreateThing"}},
		declined: []emulator.Decline{{Operation: "stub/Client.CreateRefused", Reason: "out of scope here"}},
		table: map[string]string{
			"stub_thing":    "stub/Client.CreateThing",
			"stub_refused":  "stub/Client.CreateRefused",
			"stub_mistyped": "stub/Client.CraeteThing",
		},
	}

	answers := stackAnswers([]emulator.Pack{p})

	for _, c := range []struct {
		typ      string
		standing stack.Standing
	}{
		{"stub_thing", stack.StandingServed},
		{"stub_refused", stack.StandingDeclined},
		{"stub_mistyped", stack.StandingUnknown},
	} {
		got, ok := answers[c.typ]
		if !ok {
			t.Errorf("%s is missing from the answers", c.typ)
			continue
		}
		if got.standing != c.standing {
			t.Errorf("%s: standing = %q, want %q", c.typ, got.standing, c.standing)
		}
	}

	// The refusal carries the pack's own reason, read from Declined() rather
	// than copied into the table. That is what makes a reason rewritten in a
	// pack reach this report with no second edit — #298 measured the four days
	// the copy cost last time.
	if got := answers["stub_refused"].reason; got != "out of scope here" {
		t.Errorf("the refusal reason is %q, not the pack's own", got)
	}
	// And the mistyped row says what is wrong with it, naming the operation.
	if got := answers["stub_mistyped"].reason; got == "" {
		t.Error("a row naming an undecided operation gives no reason, so nobody can tell a typo " +
			"from a product this emulator has not reached yet")
	}
}

// A served row and a refused row in one configuration: the refusal decides.
//
// Drawn over the stub rather than the real packs, so the rule is asserted on
// the arithmetic and not on today's Outscale table.
func TestARefusalOutranksTheRestOfTheInventory(t *testing.T) {
	p := stubStackPack{
		name:     "stub",
		routes:   []emulator.Route{{Operation: "stub/Client.CreateThing"}},
		declined: []emulator.Decline{{Operation: "stub/Client.CreateRefused", Reason: "out of scope here"}},
		table: map[string]string{
			"stub_thing":   "stub/Client.CreateThing",
			"stub_refused": "stub/Client.CreateRefused",
		},
	}
	mod := stack.Read("d", []stack.File{{Name: "main.tf", Source: `
resource "stub_thing" "a" {}
resource "stub_refused" "b" {}
`}})

	rep := stackReport([]emulator.Pack{p}, mod)
	if rep.Verdict != stack.VerdictIncompatible {
		t.Fatalf("verdict = %q, want incompatible", rep.Verdict)
	}
	if len(rep.Refused()) != 1 || rep.Refused()[0].Name != "stub_refused" {
		t.Errorf("refused rows = %+v, want exactly the refused type", rep.Refused())
	}
}

// Ten declarations of one type are one row, not ten.
//
// A report that repeated the question would bury the row that matters under the
// inventory, which is the failure mode #888's own example output warns about
// ("show a concise overall summary, not a wall of 100 lines").
func TestOneRowPerTypeNotPerDeclaration(t *testing.T) {
	p := stubStackPack{
		name:   "stub",
		routes: []emulator.Route{{Operation: "stub/Client.CreateThing"}},
		table:  map[string]string{"stub_thing": "stub/Client.CreateThing"},
	}
	mod := stack.Read("d", []stack.File{{Name: "main.tf", Source: `
resource "stub_thing" "a" {}
resource "stub_thing" "b" { count = 2 }
resource "stub_thing" "c" {}
`}})

	rep := stackReport([]emulator.Pack{p}, mod)
	if len(rep.Findings) != 1 {
		t.Fatalf("findings = %d, want one row for one type: %+v", len(rep.Findings), rep.Findings)
	}
	// And the plan-time instance count of any one of them marks the row, so the
	// report can say the count is not decided here without listing the three.
	if !rep.Findings[0].Counted {
		t.Error("a type declared three times, one of them counted, does not carry the plan-time mark")
	}
}

// A module nobody could read makes the verdict unknown, whatever the types
// that WERE read say.
//
// This is the rule that stops a report covering files nobody opened, and it
// needs its own subject: the repository's own stacks call local modules the
// loader follows, so none of them can exercise it. A registry source does —
// reading it would need an install, which a preflight must not perform.
//
// The first version of this report had the opposite behaviour and answered
// `compatible` over two unread files. The mutation that removes this rule is in
// tools/falsify/specs/a-preflight-reads-declarations.json.
func TestAnUnreadableModuleMakesTheVerdictUnknown(t *testing.T) {
	p := stubStackPack{
		name:   "stub",
		routes: []emulator.Route{{Operation: "stub/Client.CreateThing"}},
		table:  map[string]string{"stub_thing": "stub/Client.CreateThing"},
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
resource "stub_thing" "a" {}
module "remote" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	mod := stack.Load(dir, stack.EngineTerraform)
	rep := stackReport([]emulator.Pack{p}, mod)

	// Every type that was read is served, so without the module rule this
	// would read `compatible`.
	for _, f := range rep.Findings {
		if f.Standing != stack.StandingServed {
			t.Fatalf("a read type is %q, so this test would prove the wrong thing: %+v", f.Standing, f)
		}
	}
	if rep.Verdict != stack.VerdictUnknown {
		t.Errorf("verdict = %q while a module was not read, want unknown", rep.Verdict)
	}
	if len(rep.Unresolved) != 1 || !strings.Contains(rep.Unresolved[0], "remote") {
		t.Errorf("unresolved = %v, want the module named with its reason", rep.Unresolved)
	}
}
