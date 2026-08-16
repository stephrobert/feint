package storetest

import (
	"fmt"
	"sync"
)

// The shared control for the half of the concurrency story a sweep cannot see.
//
// Sweep reads the store once the traffic has stopped and reports what is
// incoherent about it: two resources holding one address, a runtime object named
// twice. What it cannot see is a write the API acknowledged and the store then
// dropped, because the result is perfectly coherent — it is simply not what the
// client was told had happened.
//
// That is the defect a per-target lock prevents, and it kept being written once
// per pack: Outscale ordered its update, Exoscale mutated inside store.Update,
// and Scaleway's took no order at all and lost fields under load (#211). Three
// packs, one property, so the property lives here and each pack declares only
// its own traffic — the same split as Sweep.
//
// The mechanism it catches is stated by the store itself, on Commit: "State,
// Runtime and Attrs are taken from the copy wholesale, so a concurrent write to
// a *different* field of the same resource is still lost". Two handlers reading
// the same resource, changing one field each and committing whole maps built
// from their own stale read: the second erases the first, and both answered 200.

// Write is one concurrent updater in a NoLostUpdate run: how to send its change,
// and how to read back the field it must have left behind.
//
// Got is called once the traffic has stopped, so it reads the settled value. A
// Write whose Apply the API refused is reported as a refusal rather than as a
// lost field: an update that never landed proves nothing about ordering, and
// counting it as a loss would turn a pack's own validation into a race report.
type Write struct {
	// Field names what this updater owns, and appears in the failure.
	Field string
	// Apply sends one update and reports whether the API accepted it.
	Apply func() bool
	// Got reads what the resource carries for Field now.
	Got func() string
	// Want is what Got must return once every updater has finished.
	Want string
}

// Trial builds the writers for one run, on a target nothing else has touched.
//
// A factory rather than a fixed list, and the difference is the whole control.
// The first version of this ran each writer many times against one resource, on
// the reasoning that more contention finds more races. It found none, and
// falsification said so: with the ordering removed, the test stayed green.
//
// Repetition hides this defect instead of exposing it. What is lost is a field
// whose value the winner's copy predates, and after a few rounds every writer's
// reads already carry every other writer's value — so the last commit, whoever
// wins it, writes a map that has everything. The loss is only visible while some
// field still has a value nobody else has read yet, which is to say on the
// first write to a fresh resource.
//
// So each trial gets its own target and each writer fires once.
type Trial func(round int) []Write

// NoLostUpdate runs `trials` independent trials and reports every field a trial
// acknowledged and then lost.
//
// Trials rather than rounds: one trial can be won in an order that happens to
// lose nothing, so a single one proves little either way. Repeating on fresh
// targets is what turns "this interleaving was fine" into "no interleaving loses
// a field", and it is the repetition that costs nothing in signal.
func NoLostUpdate(trials int, build Trial) []string {
	if trials < 1 {
		trials = 1
	}

	var found []string
	for trial := range trials {
		writes := build(trial)
		refusals := make([]string, len(writes))

		var wg sync.WaitGroup
		for i, write := range writes {
			wg.Add(1)
			go func(i int, write Write) {
				defer wg.Done()
				if !write.Apply() {
					// Recorded rather than returned: a goroutine cannot fail a
					// test, and a refusal is the pack's own validation talking,
					// not a race.
					refusals[i] = fmt.Sprintf(
						"%s: the API refused its update on trial %d, so this run measured nothing about it",
						write.Field, trial+1)
				}
			}(i, write)
		}
		wg.Wait()

		for i, write := range writes {
			if refusals[i] != "" {
				found = append(found, refusals[i])
				continue
			}
			if got := write.Got(); got != write.Want {
				found = append(found, fmt.Sprintf(
					"trial %d, %s: the API acknowledged %q and the resource carries %q — a "+
						"concurrent update on another field overwrote it, so this handler is "+
						"not ordered on its target",
					trial+1, write.Field, write.Want, got))
			}
		}
		// One report is the news; a hundred is the same news, harder to read.
		if len(found) > 0 {
			return found
		}
	}
	return nil
}
