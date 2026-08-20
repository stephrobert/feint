package emulator_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// A declaration whose reason carries no decision is a comment, and this
// repository has a paragraph about what a comment standing in for a control
// costs. An invariant faces the guard Declined() faces, plus one of its own: a
// kind nothing implements would read as "compared" and compare nothing.
func TestAnInvariantWithoutAUsableReasonIsRefused(t *testing.T) {
	bad := []emulator.Invariant{
		{Operation: "instance/v1/API.CreateServer", Path: "server.name", Kind: emulator.InvariantValue, Reason: ""},
		{Operation: "instance/v1/API.CreateServer", Path: "server.zone", Kind: emulator.InvariantValue, Reason: "TODO"},
		{Operation: "instance/v1/API.CreateServer", Path: "server.tags[]", Kind: "whatever",
			Reason: "a reason long enough to pass the prose guard and still name a kind nothing here implements"},
	}
	got := emulator.UnexplainedInvariants(bad)
	if len(got) != 3 {
		t.Fatalf("%d refusal(s) over three unusable declarations: %v", len(got), got)
	}
	for _, want := range []string{"server.name", "server.zone", "server.tags[]"} {
		if !strings.Contains(strings.Join(got, " "), want) {
			t.Errorf("the refusal does not name %s: %v", want, got)
		}
	}

	good := []emulator.Invariant{{
		Operation: "instance/v1/API.CreateServer", Path: "server.name", Kind: emulator.InvariantValue,
		Reason: "the client names the server in the request, and an answer carrying another name is an argument the API accepted and ignored",
	}}
	if refused := emulator.UnexplainedInvariants(good); len(refused) != 0 {
		t.Errorf("a usable declaration was refused: %v", refused)
	}
}

// Two entries for one field mean two reasons for one decision, and whichever
// consumer walks the slice prints a count that disagrees with the set. The
// same guard DuplicateFieldDeclines applies one level up.
func TestAFieldDeclaredTwiceForOneKindIsRefused(t *testing.T) {
	reason := "Terraform stores this as a list, so a read that reorders it is a plan diff that never converges"
	dup := []emulator.Invariant{
		{Operation: "instance/v1/API.CreateServer", Path: "server.public_ips[].id", Kind: emulator.InvariantOrder, Reason: reason},
		{Operation: "instance/v1/API.CreateServer", Path: "server.public_ips[].id", Kind: emulator.InvariantOrder, Reason: reason},
	}
	if got := emulator.DuplicateInvariants(dup); len(got) != 1 {
		t.Fatalf("%d duplicate(s) reported over one field declared twice: %v", len(got), got)
	}
	// A field declared for two *different* kinds is not a duplicate: a value can
	// be stable and a list ordered, and refusing that would make the mechanism
	// unable to express what a pack knows.
	both := []emulator.Invariant{
		{Operation: "instance/v1/API.CreateServer", Path: "server.public_ips[].id", Kind: emulator.InvariantOrder, Reason: reason},
		{Operation: "instance/v1/API.CreateServer", Path: "server.public_ips[].id", Kind: emulator.InvariantValue, Reason: reason},
	}
	if got := emulator.DuplicateInvariants(both); len(got) != 0 {
		t.Errorf("two kinds on one field were refused as a duplicate: %v", got)
	}
}

// The segment rule is shared with FieldDecline, and the sharing is the point:
// the gate that excuses a field and the replay that compares one must not
// disagree about which field they are naming.
func TestAnInvariantMatchesTheSameWayADeclineDoes(t *testing.T) {
	inv := emulator.Invariant{Operation: "op", Path: "things.*.limits.local", Kind: emulator.InvariantValue}
	dec := emulator.FieldDecline{Operation: "op", Path: "things.*.limits.local"}
	for _, path := range []string{
		"things.DEV1-S.limits.local", // one segment under the wildcard: both match
		"things.limits.local",        // zero: neither may
		"things.a.b.limits.local",    // two: neither may
		"things.DEV1-S.limits",       // shorter: neither may
	} {
		if inv.Matches("op", path) != dec.Matches("op", path) {
			t.Errorf("path %q: the invariant says %v and the decline says %v",
				path, inv.Matches("op", path), dec.Matches("op", path))
		}
	}
	if !inv.Matches("op", "things.DEV1-S.limits.local") {
		t.Error("a wildcard segment no longer matches exactly one segment")
	}
	if inv.Matches("other", "things.DEV1-S.limits.local") {
		t.Error("an invariant matched an operation it does not name")
	}
}

// A pack that declares nothing is not an error: the interface is optional, the
// way machine.Capable is, so absence reads as "compare presence and type only".
func TestAPackDeclaringNoInvariantAnswersNone(t *testing.T) {
	if got := emulator.InvariantsOf(silentPack{}); got != nil {
		t.Errorf("a pack declaring nothing answered %v", got)
	}
}

type silentPack struct{}

func (silentPack) Name() string                    { return "silent" }
func (silentPack) Routes() []emulator.Route        { return nil }
func (silentPack) Declined() []emulator.Decline    { return nil }
func (silentPack) Env(string) emulator.Environment { return emulator.Environment{} }
