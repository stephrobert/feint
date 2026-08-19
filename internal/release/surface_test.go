package release

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/drift"
)

// entries is the shorthand the cases below are written in: an operation and the
// verdict the artefact carries for it.
func entries(pairs ...string) []drift.Entry {
	out := make([]drift.Entry, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, drift.Entry{Operation: pairs[i], Status: drift.Status(pairs[i+1])})
	}
	return out
}

func classOf(t *testing.T, changes []Change, operation string) Class {
	t.Helper()
	for _, c := range changes {
		if c.Operation == operation {
			return c.Class
		}
	}
	t.Fatalf("no change reported for %s; the diff found %d others", operation, len(changes))
	return ""
}

func unnamed(v Verdict) []string {
	out := make([]string, 0, len(v.Unnamed))
	for _, c := range v.Unnamed {
		out = append(out, c.Operation)
	}
	return out
}

// The 0.9.0 case, replayed on the artefacts of the two tags it happened
// between.
//
// v0.9.0 mounted instance/v2alpha1 private network interfaces; its CHANGELOG
// section contains neither `v2alpha1` nor `private-network-interfaces`, and the
// consumer it mattered to spent a day probing two binaries side by side to
// learn what this diff answers in milliseconds.
func TestARouteThatArrivesUnnamedIsRefused(t *testing.T) {
	before := entries("instance/v1/API.ListPrivateNICs", "implemented")
	after := entries(
		"instance/v1/API.ListPrivateNICs", "implemented",
		"instance/v2alpha1/API.ListPrivateNetworkInterfaces", "implemented",
	)
	changes := Compare("scaleway", before, after)
	if got := classOf(t, changes, "instance/v2alpha1/API.ListPrivateNetworkInterfaces"); got != Served {
		t.Fatalf("the arriving family is classified %q, not %q", got, Served)
	}

	// The note 0.9.0 actually published: two other subjects, and not one word
	// about the family that arrived with them.
	silent := "## [Unreleased]\n\n### Fixed\n\n- acknowledged writes survive their concurrent siblings\n"
	verdict := Audit(changes, silent, nil)
	if verdict.OK() {
		t.Fatal("a release that mounts a whole API version and never mentions it passes: " +
			"this gate would not have caught the case it exists for")
	}
	if got := unnamed(verdict); len(got) != 1 || got[0] != "instance/v2alpha1/API.ListPrivateNetworkInterfaces" {
		t.Fatalf("the refusal names %v, not the operation that arrived", got)
	}

	// The accepting half: the note a reader could have grepped. Both spellings
	// pass, because both are what somebody would write.
	for _, note := range []string{
		"- serves `instance/v2alpha1/API.ListPrivateNetworkInterfaces`\n",
		"- `instance/v2alpha1`: `API.ListPrivateNetworkInterfaces` answers\n",
	} {
		if v := Audit(changes, note, nil); !v.OK() {
			t.Errorf("a note reading %q is refused, and it names the operation the way a release note does", note)
		}
	}
}

// A refusal withdrawn is a change of its own, and the one that costs silently.
//
// The downstream report listed three refusals it had designed around, all three
// of which had quietly become features. Nothing went red for any of them, which
// is why this transition is not a shade of "newly served" here.
func TestARefusalWithdrawnIsNamedAsMuchAsARouteAdded(t *testing.T) {
	before := entries("osc/Client.CreateLoadBalancer", "declined")
	after := entries("osc/Client.CreateLoadBalancer", "implemented")
	changes := Compare("outscale", before, after)

	if got := classOf(t, changes, "osc/Client.CreateLoadBalancer"); got != RefusalWithdrawn {
		t.Fatalf("declined → implemented is classified %q, not %q", got, RefusalWithdrawn)
	}
	if !RefusalWithdrawn.MustBeNamed() {
		t.Fatal("a refusal withdrawn need not be named: a consumer who read the refusal, " +
			"believed it and built around it has no way left to learn it is gone")
	}
	if v := Audit(changes, "### Added\n\n- nothing about load balancers\n", nil); v.OK() {
		t.Error("a release that stops refusing an operation passes without saying so")
	}
	if v := Audit(changes, "- Outscale serves `osc/Client.CreateLoadBalancer`\n", nil); !v.OK() {
		t.Error("the note names the operation and the gate still refuses it")
	}
}

// The dangerous direction. A consumer discovers this one in a red pipeline.
func TestAnOperationThatStopsBeingServedIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after []drift.Entry
	}{
		{"declined again", entries("instance/v1/API.CreateServer", "declined")},
		{"gone from the artefact", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes := Compare("scaleway", entries("instance/v1/API.CreateServer", "implemented"), tc.after)
			if got := classOf(t, changes, "instance/v1/API.CreateServer"); got != Withdrawn {
				t.Fatalf("an operation that stopped being served is classified %q, not %q", got, Withdrawn)
			}
			if v := Audit(changes, "### Added\n\n- an unrelated sentence\n", nil); v.OK() {
				t.Error("a release stops serving an operation and says nothing: this is the " +
					"direction that breaks somebody's pipeline")
			}
		})
	}
}

// And the two transitions that must not be required, because requiring them is
// how a gate teaches everybody to skip its section.
//
// Widening the drift scan to the lb and vpcgw products brought 137 operations
// in already declined. Nothing a consumer could reach changed, and a note
// listing them would bury the three transitions that matter.
func TestAnArrivalThatIsAlreadyDeclinedNeedNotBeNamed(t *testing.T) {
	changes := Compare("scaleway", nil, entries(
		"lb/v1/API.CreateLB", "declined",
		"lb/v1/ZonedAPI.CreateLB", "implemented",
	))
	if got := classOf(t, changes, "lb/v1/API.CreateLB"); got != NewlyDeclined {
		t.Fatalf("an operation that arrives declined is classified %q, not %q", got, NewlyDeclined)
	}
	if NewlyDeclined.MustBeNamed() || Dropped.MustBeNamed() {
		t.Fatal("an arrival that is already declined, or a declined operation upstream dropped, " +
			"is required in the note: the section that matters would be buried under 137 lines " +
			"nobody can act on")
	}

	verdict := Audit(changes, "- serves `lb/v1/ZonedAPI.CreateLB`\n", nil)
	if !verdict.OK() {
		t.Fatalf("the note names the served operation and the gate still refuses: %v", unnamed(verdict))
	}
	if len(verdict.Reported) != 1 {
		t.Fatalf("the declined arrival is not reported at all (%d reported): a run must say what "+
			"it saw, not only what it refused", len(verdict.Reported))
	}
}

// A family mentioned in passing does not name its operations.
//
// This is the difference between a gate and a formality. `instance/v1` appears
// in almost every note this repository has ever written; if that string alone
// counted, a stowaway operation in an already-discussed family would pass for
// ever.
func TestAFamilyMentionedInPassingDoesNotNameItsOperations(t *testing.T) {
	changes := Compare("scaleway", nil, entries("instance/v1/API.SetPlacementGroupServers", "implemented"))

	if v := Audit(changes, "- an `instance/v1` fix, about something else entirely\n", nil); v.OK() {
		t.Error("naming the family alone passes, so any operation added to a family the note " +
			"happens to mention is invisible")
	}
	if v := Audit(changes, "- `instance/v1`: `API.SetPlacementGroupServers` answers\n", nil); !v.OK() {
		t.Error("family and method both present is refused, and that is how a note is written")
	}
}

// Tokens, not substrings.
func TestAPrefixDoesNotNameALongerOperation(t *testing.T) {
	cases := []struct {
		text      string
		operation string
		want      bool
	}{
		{"`vpcgw/v2/API.CreateIPs` is served", "vpcgw/v2/API.CreateIP", false},
		{"`vpcgw/v2/API.CreateIP` is served", "vpcgw/v2/API.CreateIP", true},
		{"`instance/v1alpha1` moved", "instance/v1/API.CreateServer", false},
		{"`instance/v1` and `API.CreateServer`", "instance/v1/API.CreateServer", true},
		{"the `exoscale/v2.list-instances-extended` id", "exoscale/v2.list-instances", false},
		{"the `exoscale/v2.list-instances` id", "exoscale/v2.list-instances", true},
		// A sentence ends in a full stop, and that must not stop a match.
		{"served by `osc/Client.CreateLoadBalancer`.", "osc/Client.CreateLoadBalancer", true},
	}
	for _, tc := range cases {
		if got := Names(tc.text, tc.operation); got != tc.want {
			t.Errorf("Names(%q, %q) = %v, want %v", tc.text, tc.operation, got, tc.want)
		}
	}
}

// An exemption that excuses nothing is stale, and a stale exemption is a gate
// that quietly stopped covering what it names.
//
// The same rule tools/compat/accepted.json lives under, for the same reason: it
// fired there for the first time the day after v0.9.0 was tagged, and retiring
// the entry was the triage the gate was asking for.
func TestAnExemptionThatExcusesNothingIsStale(t *testing.T) {
	changes := Compare("scaleway", nil, entries("instance/v1/API.CreateServer", "implemented"))
	exempt := []Exemption{
		{Operation: "instance/v1/API.CreateServer", Reason: "named in the previous release already"},
		{Operation: "instance/v1/API.LongGone", Reason: "written for a window that has closed"},
	}
	verdict := Audit(changes, "### Added\n\n- nothing relevant\n", exempt)
	if len(verdict.Excused) != 1 {
		t.Fatalf("the live exemption excused %d changes, want 1", len(verdict.Excused))
	}
	if len(verdict.Stale) != 1 || verdict.Stale[0].Operation != "instance/v1/API.LongGone" {
		t.Fatalf("the exemption that covers nothing is not reported stale: %+v", verdict.Stale)
	}
	if verdict.OK() {
		t.Error("a stale exemption passes, so the list can keep names nothing checks any more")
	}
}

// And an exemption with no reason is not an exemption.
func TestAnExemptionWithoutAReasonIsRefused(t *testing.T) {
	changes := Compare("scaleway", nil, entries("instance/v1/API.CreateServer", "implemented"))
	verdict := Audit(changes, "nothing", []Exemption{{Operation: "instance/v1/API.CreateServer", Reason: "  "}})
	if len(verdict.Unreasoned) != 1 {
		t.Fatalf("an exemption carrying no reason is accepted: %+v", verdict)
	}
	if len(verdict.Excused) != 0 {
		t.Error("it excused a change as well, so a blank line in the list silences an operation")
	}
	if verdict.OK() {
		t.Error("the run passes with an unreasoned exemption, which is a silence with a filename")
	}
}

// The window is what is above the compared version's heading, and nothing else.
func TestTheSectionStopsAtTheComparedVersion(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n- the new work\n\n## [0.9.0]\n\n- `instance/v1/API.CreateServer` was named here\n"

	section, err := Section(changelog, "0.9.0")
	if err != nil {
		t.Fatalf("Section: %v", err)
	}
	if strings.Contains(section, "named here") {
		t.Error("the window includes the compared release's own section, so a name written for " +
			"the previous release excuses an operation that changed after it")
	}
	if !strings.Contains(section, "the new work") {
		t.Error("the window excludes the section describing what changed since the tag")
	}

	if _, err := Section(changelog, "0.8.0"); err == nil {
		t.Error("a changelog with no heading for the compared version is scanned whole rather " +
			"than refused: every operation ever mentioned would count as named")
	}
}
