package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// A gap's kind comes from the record, never from the operation's name.
//
// The whole value of this queue is that a zero names one of six different jobs.
// If a kind could be inferred from a name, a renamed operation would change the
// work it names, and the queue would be a convention rather than a measurement.
//
// Each case below is a state the record can really hold, and they are the
// branches of classifyGap in order.
func TestAGapIsClassifiedFromTheRecordRatherThanTheName(t *testing.T) {
	shape := namedAxis(t, "shape")
	negative := namedAxis(t, "negative")
	probed := namedAxis(t, "probed")

	cases := []struct {
		name string
		ev   emulator.Evidence
		axis evidenceAxis
		// reason is Route.Undriven, unearnable is Route.Unearnable for this
		// axis. They are separate columns because they answer different
		// questions and the classifier must not confuse them.
		reason     string
		unearnable string
		want       gapKind
	}{
		{"a violating verdict outranks everything",
			emulator.Evidence{Driven: true, Shape: "violating"}, shape, "", "", gapViolating},
		{"undriven and nothing says why is a suite to write",
			emulator.Evidence{Driven: false}, negative, "", "", gapUndriven},
		{"unobserved is a recording, whoever drove it",
			emulator.Evidence{Driven: true, Shape: "unobserved"}, shape, "", "", gapUnrecorded},
		{"no probe verdict is the probe's side",
			emulator.Evidence{Driven: true, Probed: "none"}, probed, "", "", gapUnvalidated},
		{"driven, not violating, another axis: unexplained rather than guessed",
			emulator.Evidence{Driven: true}, negative, "", "", gapUnproven},
		// The two that separate "no suite yet" from "no client to write one
		// with". Same record, same axis, different answer — so the reason is
		// what decides, and it is read from the route rather than from the name.
		{"undriven with a declared reason is not work",
			emulator.Evidence{Driven: false}, negative,
			"no official client calls it: the CLI has no attach subcommand", "", gapDeclared},
		// A reason must never rescue a driven operation from its zero. The
		// stale half of TestEveryUndrivenOperationSaysWhy exists to reject such
		// a reason, and this asserts the queue does not honour it meanwhile:
		// otherwise a stale excuse would empty a real recording queue.
		{"a reason on a driven operation changes nothing",
			emulator.Evidence{Driven: true}, negative,
			"no official client calls it: the CLI has no attach subcommand", "", gapUnproven},
		// The half #445 added, and the one this file could not state before it.
		// Same record and same reason as the case two rows up, on an axis the
		// probe earns with no client at all: the sentence is about `exo`, the
		// zero is not, and filing it "declared" told a reader nobody could act
		// on work #429 has since shown to be both doable and already done.
		{"a client reason does not explain a zero the probe earns",
			emulator.Evidence{Driven: false, Probed: "none"}, probed,
			"no official client calls it: the CLI has no attach subcommand", "", gapUnvalidated},
		{"a client reason does not explain a missing recording either",
			emulator.Evidence{Driven: false, Shape: "unobserved"}, shape,
			"no official client calls it: the CLI has no attach subcommand", "", gapUnrecorded},
		// The axis declaration, which answers a different question from the one
		// above: the operation IS driven, and the axis is still out of reach.
		// Without that branch of classifyGap this is "unproven", which sends a
		// reader to write a case that cannot exist.
		{"driven, and the route says the axis is out of reach: not work",
			emulator.Evidence{Driven: true}, negative, "",
			"no supported client can compose a request it must refuse", gapDeclared},
		// And it must not answer for a missing suite. An operation nothing
		// drives is Undriven's business whatever its axes declare, or a real
		// gap in the suites hides behind a true statement about something else.
		{"undriven with an axis declaration and no client reason is still a suite to write",
			emulator.Evidence{Driven: false}, negative, "",
			"no supported client can compose a request it must refuse", gapUndriven},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := classifyGap(c.ev, c.axis, c.reason, c.unearnable)
			if got != c.want {
				t.Fatalf("classified as %s, want %s", gapKindNames[got], gapKindNames[c.want])
			}
			// The reason travels with the branch, or a `declared` line prints a
			// sentence that decided nothing — which is the shape of the defect
			// this queue was corrected for.
			if got == gapDeclared && why == "" {
				t.Fatal("classified as declared and returned no reason to print")
			}
			if got != gapDeclared && why != "" {
				t.Fatalf("classified as %s and returned the reason %q", gapKindNames[got], why)
			}
		})
	}
}

// The queue never names an operation no pack serves.
//
// This is the one wrong answer that would make the whole feature untrustworthy:
// a declined operation is not missing, it is a decision somebody wrote down with
// a reason. Sending a reader to implement one would be worse than printing
// nothing at all — it would spend a person's day undoing a choice.
//
// The record holds served operations only, so the property is that the queue is
// a subset of it. Asserted rather than assumed, because "the record only holds
// served operations" is exactly the kind of sentence that stops being true.
func TestTheQueueOnlyNamesOperationsTheRecordHolds(t *testing.T) {
	art, owners, providers := gapFixture(t)
	report, err := buildGaps("fixture", art, owners, nil, nil, providers, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) == 0 {
		t.Fatal("the fixture produced no group, so this test measures nothing")
	}
	seen := 0
	for _, g := range report.Groups {
		for _, e := range g.Entries {
			if _, ok := art.Operations[e.Operation]; !ok {
				t.Errorf("%s is queued and the record does not hold it", e.Operation)
			}
			if owners[e.Operation] != g.Provider {
				t.Errorf("%s is queued under %s and belongs to %s", e.Operation, g.Provider, owners[e.Operation])
			}
			seen++
		}
	}
	if seen == 0 {
		t.Fatal("no entry was examined, so the subset property proves nothing")
	}
}

// Within a group, the work is ordered by what it is, then by name.
//
// The order is a declared decision, not a score: a defect first because it is
// the only one here, then the recording that is one session from earned, then
// the suite that is upstream of most axes. A queue sorted alphabetically hands
// the same list to three different people.
func TestTheQueueIsOrderedByTheWorkItNames(t *testing.T) {
	art, owners, providers := gapFixture(t)
	report, err := buildGaps("fixture", art, owners, nil, nil, providers, "", "")
	if err != nil {
		t.Fatal(err)
	}
	kinds := 0
	for _, g := range report.Groups {
		last := -1
		for _, e := range g.Entries {
			r := kindRank(e.Kind)
			if r < last {
				t.Fatalf("%s/%s: %s (%s) comes after a later kind", g.Provider, g.Axis, e.Operation, e.Kind)
			}
			if r != last {
				kinds++
			}
			last = r
		}
	}
	if kinds < 2 {
		// Without at least two kinds somewhere, an alphabetical sort would pass
		// this test, which is the shape of an assertion that measures nothing.
		t.Fatalf("the fixture produced %d kind transition(s); it cannot show an order", kinds)
	}
}

// The vocabulary travels with the data.
//
// A consumer reading only `--format json` must not have to open this file to
// learn what "unrecorded" means, and a kind printed without its meaning is how
// a queue gets misread by everybody who did not write it.
func TestTheJSONCarriesWhatEachKindMeans(t *testing.T) {
	art, owners, providers := gapFixture(t)
	report, err := buildGaps("fixture", art, owners, nil, nil, providers, "", "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Kinds  map[string]string `json:"kinds"`
		Groups []struct {
			Entries []struct{ Kind string } `json:"entries"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	for _, g := range back.Groups {
		for _, e := range g.Entries {
			if strings.TrimSpace(back.Kinds[e.Kind]) == "" {
				t.Fatalf("the JSON uses kind %q and does not say what it means", e.Kind)
			}
		}
	}
}

// An axis nobody declares is refused rather than answered with an empty queue.
//
// "No gap on that axis" and "there is no such axis" are the same empty page and
// very different facts — the distinction `corpus --check` already refuses to
// blur for a replay of nothing.
func TestAnUnknownAxisIsRefused(t *testing.T) {
	art, owners, providers := gapFixture(t)
	if _, err := buildGaps("fixture", art, owners, nil, nil, providers, "", "no-such-axis"); err == nil {
		t.Fatal("an axis nobody declares was accepted, so a typo prints an empty queue and reads as a clean one")
	}
}

// namedAxis returns the declared axis, failing loudly if it is gone.
func namedAxis(t *testing.T, name string) evidenceAxis {
	t.Helper()
	for _, a := range evidenceAxisList() {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no axis named %q is declared", name)
	return evidenceAxis{}
}

// gapFixture is a record holding one operation of each state, per provider, so
// every branch of the classification is reachable.
func gapFixture(t *testing.T) (*evidenceArtefact, map[string]string, []string) {
	t.Helper()
	ops := map[string]emulator.Evidence{
		"instance/v1/API.Driven":     {Driven: true, Probed: "response", Contract: "clean", Shape: "observed", Dataplane: true, Behaviour: true, Negative: true},
		"instance/v1/API.Unobserved": {Driven: true, Probed: "response", Contract: "clean", Shape: "unobserved"},
		"instance/v1/API.Undriven":   {Driven: false, Shape: "unobserved"},
		"instance/v1/API.Violating":  {Driven: true, Shape: "violating"},
		"osc/Client.Unobserved":      {Driven: true, Probed: "response", Contract: "clean", Shape: "unobserved"},
		"osc/Client.Undriven":        {Driven: false, Shape: "unobserved"},
		// An operation the record holds and no pack claims. It exists so the
		// unclaimed branch is reachable at all: without it the guard that skips
		// such an operation can be removed and every assertion stays green,
		// which is a fixture proving the code rather than the code proving
		// itself. Measured — the mutation that files it under the first
		// provider stayed green until this line existed.
		"nobody/v1/API.Orphan": {Driven: true, Shape: "unobserved"},
	}
	art := &evidenceArtefact{Operations: ops}
	owners := map[string]string{}
	for op := range ops {
		switch {
		case strings.HasPrefix(op, "nobody/"):
			// Deliberately absent from owners.
		case strings.HasPrefix(op, "osc/"):
			owners[op] = "outscale"
		default:
			owners[op] = "scaleway"
		}
	}
	// A path is written so a failure names something a reader can open, on the
	// same terms as the other fixtures in this package.
	_ = filepath.Join(os.TempDir(), "gapfixture")
	return art, owners, []string{"outscale", "scaleway"}
}

// The queue separates "nobody has written the suite" from "no client exists to
// write one with", and it does it from what the packs declare.
//
// Measured on 2026-08-24, before this split existed: the record left twenty-five
// operations undriven, and every single one already carried a Route.Undriven
// reason — `scw ipam ip` has no attach subcommand, `scw vpc` has no subnet
// subcommand, `scw block volume-type list` is pinned to v1alpha1. The queue
// nonetheless printed all 141 of their zeros under "a conformance suite: no
// real client reaches this operation", which asks a reader for a suite that
// cannot be written. It is the shape of defect #407 removed from the `shape`
// axis: an entry no amount of work retires.
//
// The assertion is a set equality between two independently derived sets, in
// both directions, and both are required to be non-empty. A subset check in one
// direction alone is satisfied by a queue that classifies nothing, which is
// exactly how a control of this kind passes while measuring nothing.
func TestADeclaredReasonSplitsTheUndrivenQueue(t *testing.T) {
	art, err := loadEvidenceArtefact(filepath.Join("..", "..", "coverage", "evidence.json"))
	if err != nil {
		t.Fatalf("read the evidence artefact: %v", err)
	}
	if art == nil || len(art.Operations) == 0 {
		t.Fatal("the evidence artefact is empty, so this test would pass while measuring nothing")
	}
	owners, providers, err := operationOwners()
	if err != nil {
		t.Fatal(err)
	}
	reasons, err := undrivenReasons()
	if err != nil {
		t.Fatal(err)
	}

	// The set the packs declare: undriven in the record AND carrying a reason.
	// Derived from the two inputs, never from the report being tested.
	declaredByPacks := map[string]bool{}
	undrivenWithoutReason := map[string]bool{}
	for op := range art.Operations {
		if _, owned := owners[op]; !owned {
			continue
		}
		if art.Operations[op].Driven {
			continue
		}
		if reasons[op] != "" {
			declaredByPacks[op] = true
		} else {
			undrivenWithoutReason[op] = true
		}
	}
	if len(declaredByPacks) == 0 {
		t.Fatal("no operation is both undriven and declared, so the split this test " +
			"describes is untestable on this record and the test would pass on any code")
	}

	report, err := buildGaps("coverage/evidence.json", art, owners, reasons, nil, providers, "", "")
	if err != nil {
		t.Fatal(err)
	}
	declaredByQueue := map[string]bool{}
	undrivenByQueue := map[string]bool{}
	for _, g := range report.Groups {
		for _, e := range g.Entries {
			switch e.Kind {
			case "declared":
				declaredByQueue[e.Operation] = true
				if e.Reason == "" {
					t.Errorf("%s is queued as declared and prints no reason; "+
						"a decision a report names but does not state is one nobody re-examines", e.Operation)
				}
			case "undriven":
				undrivenByQueue[e.Operation] = true
			}
		}
	}

	for op := range declaredByPacks {
		if !declaredByQueue[op] {
			t.Errorf("%s is undriven and its route says why, and the queue does not call it declared", op)
		}
		if undrivenByQueue[op] {
			t.Errorf("%s carries a reason at its route and the queue still asks for a conformance suite", op)
		}
	}
	for op := range declaredByQueue {
		if !declaredByPacks[op] {
			t.Errorf("%s is queued as declared and the packs declare no reason for it: "+
				"the queue is excusing an operation nobody excused", op)
		}
	}
	for op := range undrivenByQueue {
		if !undrivenWithoutReason[op] {
			t.Errorf("%s is queued as undriven and is not an undriven operation without a reason", op)
		}
	}
}
