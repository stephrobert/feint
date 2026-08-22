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
// The whole value of this queue is that four zeros name four different jobs. If
// a kind could be inferred from a name, a renamed operation would change the
// work it names, and the queue would be a convention rather than a measurement.
//
// Each case below is a state the record can really hold, and the four are the
// four branches of classifyGap in order.
func TestAGapIsClassifiedFromTheRecordRatherThanTheName(t *testing.T) {
	shape := namedAxis(t, "shape")
	negative := namedAxis(t, "negative")

	cases := []struct {
		name string
		ev   emulator.Evidence
		axis evidenceAxis
		want gapKind
	}{
		{"a violating verdict outranks everything",
			emulator.Evidence{Driven: true, Shape: "violating"}, shape, gapViolating},
		{"undriven, whatever else is true",
			emulator.Evidence{Driven: false, Shape: "unobserved"}, shape, gapUndriven},
		{"driven and unobserved is a recording",
			emulator.Evidence{Driven: true, Shape: "unobserved"}, shape, gapUnrecorded},
		{"driven, not violating, another axis: unexplained rather than guessed",
			emulator.Evidence{Driven: true}, negative, gapUnproven},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyGap(c.ev, c.axis); got != c.want {
				t.Fatalf("classified as %s, want %s", gapKindNames[got], gapKindNames[c.want])
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
	report, err := buildGaps("fixture", art, owners, providers, "", "")
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
	report, err := buildGaps("fixture", art, owners, providers, "", "")
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
	report, err := buildGaps("fixture", art, owners, providers, "", "")
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
	if _, err := buildGaps("fixture", art, owners, providers, "", "no-such-axis"); err == nil {
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
