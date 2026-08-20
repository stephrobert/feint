package drift_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/drift"
)

func report() drift.Report {
	return drift.Report{
		Provider: "outscale",
		Entries: []drift.Entry{
			{Operation: "osc/Client.CreateLoadBalancerListeners", Status: drift.StatusDeclined,
				Reason: "a data plane this emulator does not run, so answering would describe a balancer nothing forwards through"},
			{Operation: "osc/Client.ReadCatalog", Status: drift.StatusDeclined,
				Reason: "no client this project drives reads a price on its way to creating anything"},
			{Operation: "osc/Client.ListApplications", Status: drift.StatusDeclined,
				Reason: "the emulator accepts every credential on purpose, so policy management would describe an authority nobody holds"},
			{Operation: "osc/Client.ReadVms", Status: drift.StatusImplemented},
			{Operation: "osc/Client.ReadNets", Status: drift.StatusImplemented},
			{Operation: "osc/Client.ReadBrandNewThing", Status: drift.StatusUnknown},
		},
	}
}

func calls(counts map[string]int) drift.Calls {
	return drift.Calls{Count: counts, Clients: map[string]string{}}
}

// #74's headline, in one assertion: eighteen recorded calls outrank an
// operation nobody ever called, and nothing in this repository could say so
// before. The second half is the one that matters — the uncalled decline is not
// at the bottom of the list, it is *not in the list*, because a ranking that
// carries every refusal is the alphabet again.
func TestADeclineAClientCalledOutranksOneNobodyEverDid(t *testing.T) {
	// The counts run against the alphabet on purpose. With the most-called
	// operation also sorting first by name, neutralising the call comparison
	// changed nothing and the falsification of this very guard came back STILL
	// GREEN — the fixture was ranking correctly by accident.
	obs := report().Observe(calls(map[string]int{
		"osc/Client.ReadCatalog":                 18,
		"osc/Client.CreateLoadBalancerListeners": 2,
	}), 0)

	if len(obs.Declined) != 2 {
		t.Fatalf("%d declined row(s), want 2: only what was called is ranked", len(obs.Declined))
	}
	if obs.Declined[0].Operation != "osc/Client.ReadCatalog" {
		t.Errorf("the ranking leads with %q, want the operation called eighteen times "+
			"(the other one sorts first by name, which is the point)", obs.Declined[0].Operation)
	}
	for _, o := range obs.Declined {
		if o.Operation == "osc/Client.ListApplications" {
			t.Errorf("an operation nobody called is in the ranking; it belongs in the count of the silent")
		}
	}
}

// The ordering rule, asserted on its own so a later change to the comparator
// cannot pass by accident: an operation with zero observed calls never sorts
// above one with at least one. #74's second acceptance criterion.
func TestAnOperationNobodyCalledNeverOutranksOneSomebodyDid(t *testing.T) {
	obs := report().Observe(calls(map[string]int{"osc/Client.ReadCatalog": 1}), 0)
	if len(obs.Declined) != 1 || obs.Declined[0].Calls != 1 {
		t.Fatalf("the ranking is %+v, want the single called operation", obs.Declined)
	}
	if obs.DeclinedSilent != 2 {
		t.Errorf("%d silent decline(s), want 2: what is not ranked still has to be counted", obs.DeclinedSilent)
	}
}

// "Nobody called it" and "nobody triaged it" are different facts, and blurring
// them is the defect this view exists to correct. So they are counted apart,
// and the rendering states both in words that cannot be read as each other.
func TestTheSilentAndTheUntriagedAreCountedApart(t *testing.T) {
	obs := report().Observe(calls(map[string]int{"osc/Client.ReadBrandNewThing": 4}), 0)

	if obs.UnknownTotal != 1 || len(obs.Unknown) != 1 || obs.Unknown[0].Calls != 4 {
		t.Fatalf("the untriaged ranking is %+v over a total of %d, want one row with four calls",
			obs.Unknown, obs.UnknownTotal)
	}
	if obs.DeclinedSilent != 3 {
		t.Errorf("%d silent decline(s), want 3", obs.DeclinedSilent)
	}
	if obs.UnknownSilent != 0 {
		t.Errorf("%d silent untriaged, want 0: the only untriaged operation was called", obs.UnknownSilent)
	}

	var out strings.Builder
	if err := obs.WriteObserved(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "declined operation(s) in all") {
		t.Errorf("the report does not state the declined population:\n%s", text)
	}
	if !strings.Contains(text, "nobody has triaged") {
		t.Errorf("the report does not state the untriaged population:\n%s", text)
	}
}

// A recording that calls nothing declined leaves the report empty rather than
// inventing a ranking. #74 asks for exactly this, and it is the direction a
// tool gets wrong when it prints the whole list "for context".
func TestARecordingThatCallsNothingDeclinedRanksNothing(t *testing.T) {
	obs := report().Observe(calls(map[string]int{"osc/Client.ReadVms": 12}), 5)
	if len(obs.Declined) != 0 || len(obs.Unknown) != 0 {
		t.Fatalf("a recording touching only served operations produced a ranking: %+v %+v",
			obs.Declined, obs.Unknown)
	}
	var out strings.Builder
	if err := obs.WriteObserved(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no recorded call reached an operation this pack declines") {
		t.Errorf("the empty case does not say it is empty:\n%s", out.String())
	}
	if obs.Implemented != 1 || obs.ImplementedTotal != 2 {
		t.Errorf("implemented %d of %d, want 1 of 2: a report that ranks nothing still has to say what it saw",
			obs.Implemented, obs.ImplementedTotal)
	}
	if !strings.Contains(out.String(), "5 recorded call(s)") {
		t.Errorf("the calls this provider could not name are not stated:\n%s", out.String())
	}
}

// The decline's own argument travels with the row. A ranking that says "this is
// wanted" without the reason it was refused invites the wrong decision, and
// the reason is data here rather than something a reader looks up in a pack.
func TestARankedDeclineCarriesTheArgumentItQuestions(t *testing.T) {
	obs := report().Observe(calls(map[string]int{"osc/Client.ReadCatalog": 3}), 0)
	if !strings.Contains(obs.Declined[0].Reason, "reads a price") {
		t.Fatalf("the ranked decline carries no reason: %+v", obs.Declined[0])
	}
	var out strings.Builder
	if err := obs.WriteObserved(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reads a price") {
		t.Errorf("the rendering drops the reason:\n%s", out.String())
	}
}
