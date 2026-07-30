package emulator

import "testing"

// The interface change on its own would have delivered nothing: a pack can
// satisfy `[]Decline` with an empty Reason on every entry, and the report would
// print a column of blanks. This is the assertion that makes the reason
// mandatory, and TestEveryDeclinedOperationSaysWhy in internal/cli runs it
// against the three real packs.
func TestUnexplainedDeclinesAreFound(t *testing.T) {
	got := UnexplainedDeclines([]Decline{
		{Operation: "a/API.One", Reason: "a local emulator has no inventory and cannot invent one"},
		{Operation: "a/API.Two"},
		{Operation: "a/API.Three", Reason: "   "},
	})
	if len(got) != 2 {
		t.Fatalf("want the two unexplained refusals, got %v", got)
	}
}

func TestBecauseCarriesTheReasonToEveryOperation(t *testing.T) {
	got := Because("the metadata service answers inside the machine", "a/API.One", "a/API.Two")
	if len(got) != 2 {
		t.Fatalf("want two declines, got %d", len(got))
	}
	for _, d := range got {
		if d.Reason == "" {
			t.Errorf("%s lost its reason", d.Operation)
		}
	}
	if ops := DeclinedOperations(got); len(ops) != 2 || ops[0] != "a/API.One" {
		t.Fatalf("operations are %v", ops)
	}
}

// DuplicateDeclines had no positive test: gutting it to `return nil` passed the
// whole suite, so the pack-level assertion proved the packs were clean only if
// the detector worked, and nothing proved the detector worked.
func TestDuplicateDeclinesFindsTheDuplicate(t *testing.T) {
	got := DuplicateDeclines([]Decline{
		{Operation: "a/API.One", Reason: "a reason long enough to pass the guard"},
		{Operation: "a/API.Two", Reason: "another reason long enough to pass"},
		{Operation: "a/API.One", Reason: "a conflicting reason for the same operation"},
	})
	if len(got) != 1 || got[0] != "a/API.One" {
		t.Fatalf("want the duplicated operation, got %v", got)
	}
	if got := DuplicateDeclines([]Decline{{Operation: "a/API.One", Reason: "a reason long enough to pass the guard"}}); len(got) != 0 {
		t.Fatalf("a clean list was reported as duplicated: %v", got)
	}
}

// The token scan, and the case that defeated the first two versions of this
// guard: a placeholder repeated until it satisfied both the exact-match list and
// the word count.
func TestAReasonBuiltFromPlaceholderTokensIsRefused(t *testing.T) {
	for _, bad := range []string{
		"TODO TODO TODO TODO TODO",
		"todo: fill this in later",
		"reason to be decided later",
		"wip decide later somebody else",
	} {
		if got := UnexplainedDeclines([]Decline{{Operation: "a/API.One", Reason: bad}}); len(got) != 1 {
			t.Errorf("the guard accepted %q", bad)
		}
	}
	// And a real reason that happens to contain one such word must pass, or the
	// scan would refuse honest prose.
	ok := "the plan would list nothing today and nobody has decided what a later version should answer instead"
	if got := UnexplainedDeclines([]Decline{{Operation: "a/API.One", Reason: ok}}); len(got) != 0 {
		t.Errorf("the guard refused a real reason containing a stem: %v", got)
	}
}
