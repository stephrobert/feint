package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// axesEarnedWithoutAClient answers, for a population of operations, which axes
// declared earned-by-a-client alone were earned by an operation no client drove.
// A non-empty answer is a contradiction: the declaration says clients are what
// earns the axis, and the population says otherwise.
//
// One function, interrogated by both tests below, for the reason
// tools/falsify/falsify.py's replay_outcome is: a rule tested through a copy of
// itself passes while the real one drifts. The record test asks it about
// coverage/evidence.json, the built test asks it about a population that cannot
// drift, and neither can answer differently from the other.
func axesEarnedWithoutAClient(ops map[string]emulator.Evidence) map[string][]string {
	out := map[string][]string{}
	for _, a := range evidenceAxisList() {
		if a.earner != earnedByAClient {
			continue
		}
		// `driven` is the one axis whose earner IS the population: it is earned
		// by e.Driven, so an undriven operation cannot earn it by construction.
		// Including it would make every answer here trivially empty on that axis.
		if a.Name == "driven" {
			continue
		}
		for op, ev := range ops {
			if !ev.Driven && a.earned(ev) {
				out[a.Name] = append(out[a.Name], op)
			}
		}
		sort.Strings(out[a.Name])
	}
	return out
}

// TestAClientBorneAxisIsCaughtOnAPopulationThatCannotDrift is the guard #766 is
// about, and it is deliberately built rather than read.
//
// # What went wrong, measured on 2026-09-14
//
// TestNoClientBorneAxisIsEarnedWithoutAClient asks the same question of the real
// record. It carries two anti-vacuity guards and both work: the record is not
// empty, and it holds twenty undriven operations.
//
// It stopped biting anyway. Its own comment names the witness it relied on:
// "one — exoscale/v2.get-operation — that earned `shape`". That operation is now
// `"driven": true`, so `shape` lost its only undriven witness and declaring it
// client-borne produced no violation for the test to find.
//
// **The project's own progress disarmed one of its guards.** Driving one more
// operation from a real client is what this repository is for; the side effect
// was a control that could no longer fail. The existing guard is global — "no
// axis earnable without a client was earned by any undriven operation" — so it
// cannot see one axis losing its population while the others keep theirs.
//
// This test gives the rule a population of its own. The axes are named
// explicitly below rather than looped over, and that is the whole difference: a
// loop over the declaration is a loop over the thing under test, so moving an
// axis to the wrong side would move the expectation with it and nothing would
// fail. A first version of this test did exactly that and passed under mutation.
func TestAClientBorneAxisIsCaughtOnAPopulationThatCannotDrift(t *testing.T) {
	// One undriven operation earning every axis. Nothing here comes from
	// coverage/evidence.json, so no progress on the real record can empty it.
	built := map[string]emulator.Evidence{
		"built/v1.everything": {
			Driven:    false, // the whole point: no client drove it
			Probed:    emulator.ProbeResponse,
			Contract:  emulator.ContractClean,
			Dataplane: true,
			Shape:     emulator.ShapeObserved,
			Behaviour: true,
			Negative:  true,
		},
	}

	violations := axesEarnedWithoutAClient(built)

	// The refusing half. These three are earned by something other than a
	// client — a recording for `shape`, validation for the other two — so none
	// may appear here. Declaring one of them client-borne makes this population
	// earn it without a client, and says so.
	for _, axis := range []string{"shape", "probed", "contract"} {
		if ops, found := violations[axis]; found {
			t.Errorf("`%s` is declared earned by a client alone, and an operation no client drove "+
				"earns it: %s\nA zero on that axis would then be retired by a reason about clients, "+
				"and the population says clients are not what earns it.",
				axis, strings.Join(ops, ", "))
		}
	}

	// The accepting half, which matters as much: a declaration marking every
	// axis earnable-without-a-client would report no violation at all, and the
	// loop above would pass on it unchanged.
	var missed []string
	for _, axis := range []string{"dataplane", "behaviour", "negative"} {
		if _, found := violations[axis]; !found {
			missed = append(missed, axis)
		}
	}
	if len(missed) > 0 {
		t.Errorf("%s client-borne and earned by this population, yet reported as no violation: "+
			"`earner` has stopped distinguishing anything, and the checks above would pass on a "+
			"declaration that calls every axis earnable without a client",
			strings.Join(missed, ", "))
	}
}

// TestEachAxisIsWitnessedByTheRecordOrSaysItIsNot is the reporting half.
//
// It does not fail when an axis has no undriven witness: whether the record
// holds one is not under a contributor's control, and driving one more operation
// from a real client must never fail a build. It fails when an axis earnable
// WITHOUT a client is earned by nobody AND nothing says so — because that is the
// state in which TestNoClientBorneAxisIsEarnedWithoutAClient silently stops
// judging that axis, which is what happened to `shape`.
//
// Updating the list is the point: moving an axis into it records that the record
// no longer witnesses it, and the built population above is what judges it from
// then on.
func TestEachAxisIsWitnessedByTheRecordOrSaysItIsNot(t *testing.T) {
	unwitnessed := map[string]string{
		"shape": "exoscale/v2.get-operation was its only undriven witness and is now driven " +
			"(measured 2026-09-14, #766): the project driving one more operation from a real " +
			"client emptied this axis's population",
	}

	art, err := loadEvidenceArtefact("../../coverage/evidence.json")
	if err != nil {
		t.Fatalf("read the evidence artefact: %v", err)
	}
	if art == nil || len(art.Operations) == 0 {
		t.Fatal("the evidence artefact is empty, so this test would measure nothing")
	}

	judged := 0
	for _, a := range evidenceAxisList() {
		if a.earner == earnedByAClient {
			continue
		}
		witnesses := 0
		for _, ev := range art.Operations {
			if !ev.Driven && a.earned(ev) {
				witnesses++
			}
		}
		reason, declared := unwitnessed[a.Name]
		switch {
		case witnesses > 0 && declared:
			t.Errorf("`%s` is listed as unwitnessed and %d undriven operation(s) earn it: "+
				"remove it from the list, or it stops describing the record", a.Name, witnesses)
		case witnesses == 0 && !declared:
			t.Errorf("`%s` is earnable without a client and no undriven operation earns it, "+
				"so TestNoClientBorneAxisIsEarnedWithoutAClient can no longer judge it and "+
				"nothing says so. Add it to `unwitnessed` with the reason, the way `shape` is", a.Name)
		case witnesses == 0 && declared && len(reason) < 40:
			t.Errorf("`%s` is excused with a reason too short to weigh: %q", a.Name, reason)
		}
		judged++
	}

	// Without this, an `earner` comparison that matched everything would skip
	// every axis and leave the loop asserting nothing.
	if judged == 0 {
		t.Fatal("no axis is earnable without a client, so this test judged nothing")
	}
}
