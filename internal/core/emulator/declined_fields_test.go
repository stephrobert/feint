package emulator

import "testing"

// A "*" segment stands for exactly one segment. The widening this refuses is
// the silent kind: a wildcard that also matched zero segments, or several,
// would let a decline written for one dictionary level excuse fields it was
// never a decision about, and the gate would go quiet on a real omission.
func TestAWildcardSegmentMatchesExactlyOneSegment(t *testing.T) {
	d := FieldDecline{Operation: "GET /v1/things", Path: "things.*.limits.local"}

	for _, tc := range []struct {
		operation, path string
		want            bool
	}{
		// The case the wildcard exists for: a dictionary key that is data.
		{"GET /v1/things", "things.alpha.limits.local", true},
		{"GET /v1/things", "things.beta.limits.local", true},
		// One segment, not zero.
		{"GET /v1/things", "things.limits.local", false},
		// One segment, not two.
		{"GET /v1/things", "things.alpha.beta.limits.local", false},
		// Not a prefix match: a deeper field is a different decision.
		{"GET /v1/things", "things.alpha.limits.local.min", false},
		// The operation is part of the identity.
		{"GET /v1/others", "things.alpha.limits.local", false},
	} {
		if got := d.Matches(tc.operation, tc.path); got != tc.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", tc.operation, tc.path, got, tc.want)
		}
	}

	// And a literal path, no wildcard: the common case must still work.
	exact := FieldDecline{Operation: "GET /v1/things", Path: "things[].visibility"}
	if !exact.Matches("GET /v1/things", "things[].visibility") {
		t.Error("an exact path did not match itself")
	}
	if exact.Matches("GET /v1/things", "things[].id") {
		t.Error("an exact path matched a different field")
	}
}

// A field's reason faces the same guard as an operation's: the check is one
// shared function, so an audit that hardens one level hardens the other. This
// asserts the sharing holds in both directions — the refusing half and the
// accepting half.
func TestFieldDeclineReasonsFaceTheSameGuardAsOperations(t *testing.T) {
	for _, bad := range []string{"", "   ", "TODO", "-", "x", "n/a", "out of scope", "see above"} {
		got := UnexplainedFieldDeclines([]FieldDecline{{Operation: "GET /v1/things", Path: "things[].id", Reason: bad}})
		if len(got) != 1 || got[0] != "GET /v1/things things[].id" {
			t.Errorf("the guard accepted %q as a field reason: %v", bad, got)
		}
	}
	ok := "the emulator attaches no local volume, so a bound here would enter arithmetic nothing enforces"
	if got := UnexplainedFieldDeclines([]FieldDecline{{Operation: "GET /v1/things", Path: "things[].id", Reason: ok}}); len(got) != 0 {
		t.Errorf("the guard refused a real reason: %v", got)
	}
}

// Two declines for one field are two reasons for one decision.
func TestDuplicateFieldDeclinesAreNamed(t *testing.T) {
	long := "a reason long enough to pass the shared guard on its own"
	dup := DuplicateFieldDeclines([]FieldDecline{
		{Operation: "GET /v1/things", Path: "things[].id", Reason: long},
		{Operation: "GET /v1/things", Path: "things[].id", Reason: long},
		{Operation: "GET /v1/things", Path: "things[].name", Reason: long},
	})
	if len(dup) != 1 || dup[0] != "GET /v1/things things[].id" {
		t.Errorf("duplicates = %v, want the one duplicated field", dup)
	}
}

// mutePack is a pack that does not implement FieldDecliner.
type mutePack struct{ Pack }

func (mutePack) Name() string { return "mute" }

// A pack that declares nothing declines nothing — absence of the method is
// data, not an error, exactly as machine.CapabilitiesOf reads a mute driver.
func TestAMutePackDeclinesNoFields(t *testing.T) {
	if got := FieldDeclinesOf(mutePack{}); got != nil {
		t.Errorf("a mute pack declined fields: %v", got)
	}
}
