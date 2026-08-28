package conformance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The waiting helpers the runtime suites share (#459).
//
// What they replace is a fixed `sleep N` standing before an assertion, and the
// danger of that replacement is named at the top of
// tools/conformance/shared/waiting.sh: a wait removed at the wrong place turns
// an assertion into a race won by luck, which is worse than the slowness. #559
// is what that looks like when it ships — a dataplane verdict that is red at
// random rather than never.
//
// So the helpers are held here by three properties, and the third is the one
// that keeps the conversion honest on every later day:
//
//  1. a condition that never comes true still fails the wait;
//  2. a condition that does come true is not waited out;
//  3. every fixed `sleep` still standing in a runtime suite says, on the line
//     above it, what it is waiting for — because the ones left are exactly the
//     ones a future reader will be tempted to delete next.
//
// tools/falsify/specs/waiting-is-not-sleeping.json replays 1 and 3 with their
// guard neutralised.

// The half that matters most. A helper whose timeout answered success would
// make every converted assertion vacuous, and silently: the suite would go
// green on a machine that never booted.
func TestAConditionThatNeverComesTrueFailsTheWait(t *testing.T) {
	out := runWaiting(t, `if wait_until 1 false; then echo "verdict=passed"; else echo "verdict=failed"; fi`)
	if !strings.Contains(out, "verdict=failed") {
		t.Errorf("wait_until answered success for a condition that is never true; every assertion "+
			"placed after it would then pass on a machine that never came up:\n%s", out)
	}
}

// The same for the disappearance half: an object that never goes must not be
// reported gone, or a leak reads as a clean teardown.
func TestAnObjectThatNeverGoesFailsTheWait(t *testing.T) {
	out := runWaiting(t, `if wait_gone 1 true; then echo "verdict=passed"; else echo "verdict=failed"; fi`)
	if !strings.Contains(out, "verdict=failed") {
		t.Errorf("wait_gone answered success while its object was still there:\n%s", out)
	}
}

// Without this a helper that refused everything would satisfy the two above.
// The assertion is on the elapsed time as well as on the verdict, because
// waiting out the budget is precisely the defect being removed: a helper that
// answered correctly after thirty seconds would have changed nothing.
func TestAConditionThatComesTrueIsNotWaitedOut(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	if err := os.WriteFile(counter, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("write the counter: %v", err)
	}

	script := `
counter="$1"
third_time() { n="$(cat "$counter")"; n=$((n + 1)); printf '%s\n' "$n" > "$counter"; [ "$n" -ge 3 ]; }
if wait_until 30 third_time; then echo "verdict=passed"; else echo "verdict=failed"; fi
echo "polls=$(cat "$counter")"
`
	start := time.Now()
	out := runWaiting(t, script, counter)
	elapsed := time.Since(start)

	if !strings.Contains(out, "verdict=passed") {
		t.Errorf("wait_until did not see a condition that became true:\n%s", out)
	}
	if !strings.Contains(out, "polls=3") {
		t.Errorf("wait_until did not poll until the condition held:\n%s", out)
	}
	// Three polls of a quarter second, plus a process start. Ten seconds is a
	// third of the budget and still two orders of magnitude above the real
	// cost, so this goes red on a helper that waits out its budget rather than
	// on a loaded machine.
	if elapsed > 10*time.Second {
		t.Errorf("a condition true after three polls took %s: the helper waits out its budget "+
			"rather than the condition, which is the sleep it was meant to replace", elapsed)
	}
}

// waitsOnSilence is the marker a kept `sleep` carries, and the reason it is a
// marker rather than a comment in prose: this test reads it.
const waitsOnSilence = "# waits on silence:"

// Every fixed `sleep` left in a runtime suite says what it waits for (#459).
//
// The suites converted their positive waits — the machine answers, the port
// opens, the pool holds three members — into polls. What could not be
// converted is a wait standing before a verdict drawn from an ABSENCE:
// "unreachable", "no longer answers", "the host holds no network". There the
// fixed wait is the only thing separating "the rule was applied" from "we
// looked too early", and a poll ending at the first negative observation turns
// a race into a pass.
//
// Those waits are therefore the next ones somebody optimises, and a comment
// saying "keep this" is not a control — this repository has paid three times
// for exactly that sentence. So the marker is mechanical: a bare `sleep` in
// these four files fails this test, and a marked one carries its reason in the
// suite where the next reader meets it.
func TestEveryFixedSleepInARuntimeSuiteSaysWhatItWaitsFor(t *testing.T) {
	suites := []string{
		filepath.Join("scaleway", "network.sh"),
		filepath.Join("outscale", "network.sh"),
		filepath.Join("exoscale", "network.sh"),
		filepath.Join("outscale", "balancer.sh"),
	}
	// A `sleep` on a line of its own, or as the whole of a loop's last
	// statement. The poll interval inside shared/waiting.sh is not in these
	// files, so nothing here is exempt.
	sleeper := regexp.MustCompile(`(^|;|&&|\|\|)\s*sleep\s`)

	found := 0
	for _, suite := range suites {
		body, err := os.ReadFile(suite) //nolint:gosec // a fixed path in this directory
		if err != nil {
			t.Fatalf("read %s: %v", suite, err)
		}
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			text := strings.TrimSpace(line)
			if strings.HasPrefix(text, "#") || !sleeper.MatchString(text) {
				continue
			}
			found++
			marked := false
			// The marker sits in the comment block immediately above, which may
			// be several lines: a reason worth writing rarely fits on one.
			for j := i - 1; j >= 0; j-- {
				above := strings.TrimSpace(lines[j])
				if !strings.HasPrefix(above, "#") {
					break
				}
				if strings.Contains(above, waitsOnSilence) {
					marked = true
					break
				}
			}
			if !marked {
				t.Errorf("%s:%d: a fixed `sleep` with nothing saying what it waits for: %s\n"+
					"    Either it stands before a verdict drawn from an absence — then say so "+
					"with a %q comment above it — or the condition exists and it should be a "+
					"wait_until (tools/conformance/shared/waiting.sh)",
					suite, i+1, text, waitsOnSilence)
			}
		}
	}
	// The witness rule: a control that looks for a missing marker must first
	// prove it can find a sleep at all. Zero would mean the loop compared
	// nothing and this test is a comment.
	if found == 0 {
		t.Fatal("no fixed sleep was found in any runtime suite, so this control examined nothing")
	}
}

// runWaiting sources the real shared/waiting.sh under the same
// `set -euo pipefail` the suites run under — the regime that makes a naive
// `helper; echo $?` abort instead of reporting — and runs the script after it.
func runWaiting(t *testing.T, script string, args ...string) string {
	t.Helper()
	requireTool(t, "bash")

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("locate the repository root: %v", err)
	}
	body := "set -euo pipefail\n. tools/conformance/shared/waiting.sh\n" + script

	cmd := exec.Command("bash", append([]string{"-c", body, "waiting-test"}, args...)...) //nolint:gosec // a fixed script
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run the waiting helpers: %v\n%s", err, out)
		}
		t.Fatalf("the harness itself failed (exit %d):\n%s", exitErr.ExitCode(), out)
	}
	return string(out)
}

// The instrument (#587), and the two properties that make its numbers usable.
//
// WHY IT IS IN THE TREE RATHER THAN IN A BRANCH. Every budget in the runtime
// suites is a number nobody has re-derived: #459 converted 36 fixed sleeps into
// polls and chose their budgets by hand from the loops it replaced — `wait_until
// 24` is `for _ in 1..12; do sleep 2; done` transcribed — and nothing since has
// measured one. #587 is the bill: a wait that fails on the maintainer's station,
// passes in CI, and had no artefact anywhere saying what it ever cost.
// WAIT_TRACE is what turns "raise it" into "measure it", and WAIT_SCALE is what
// asks "slow or broken" in one run instead of by editing twenty call sites.
//
// The two halves below are what keep it from becoming a second thing to trust:
//
//  1. unset, it changes nothing — no file, no output, the same verdicts;
//  2. the row reports the budget the CALLER asked for, never the scaled one, or
//     every scaled run would read as a suite whose budgets had already been
//     raised, which is the exact confusion the trace exists to end.
//
// tools/falsify/specs/waiting-is-not-sleeping.json replays both.
func TestTheWaitsRecordWhatTheyCostWhenAskedTo(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.tsv")

	// Budget 1 at scale 3, and the threshold below is derived rather than
	// picked. The wait ends when `SECONDS` reaches budget+1, and `SECONDS`
	// ticks on the shell's own integer boundary, so a shell started at X.99
	// sees it a hundredth of a second later: unscaled the wait lasts between
	// 1 and 2 seconds, scaled between 3 and 4. 2.5 s is the midpoint of the
	// gap, which is the only value that separates them on every host.
	//
	// The first version asserted 3.5 s and went red under `go test -race` at
	// 3.27 s — a threshold inside the scaled band rather than between the two
	// bands, which is the harness failing before its subject. It is written
	// down because that is the mistake this repository keeps paying for.
	script := `
export WAIT_TRACE="$1" WAIT_SCALE=3
if wait_until 1 false; then echo "verdict=passed"; else echo "verdict=failed"; fi
`
	start := time.Now()
	out := runWaiting(t, script, trace)
	elapsed := time.Since(start)

	if !strings.Contains(out, "verdict=failed") {
		t.Fatalf("the instrument changed the verdict of a condition that is never true:\n%s", out)
	}
	body, err := os.ReadFile(trace) //nolint:gosec // a path this test just made
	if err != nil {
		t.Fatalf("the wait wrote no trace though WAIT_TRACE named one: %v", err)
	}
	rows := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(rows) != 1 {
		t.Fatalf("expected one traced wait, got %d:\n%s", len(rows), body)
	}
	fields := strings.Split(rows[0], "\t")
	if len(fields) != 6 {
		t.Fatalf("a trace row is six tab-separated fields (suite, kind, verdict, "+
			"budget asked, seconds, condition), got %d: %q", len(fields), rows[0])
	}
	if fields[0] != "waiting-test" {
		t.Errorf("the row names the suite %q, not the script that ran the wait; a trace whose "+
			"first field cannot tell scaleway/network.sh from outscale/network.sh sums three "+
			"populations into one distribution", fields[0])
	}
	if fields[2] != "EXPIRED" {
		t.Errorf("a wait whose condition never held is traced %q, so the trace disagrees with "+
			"the verdict the suite drew", fields[2])
	}
	// The property worth a test: 1, not 3. The scale is a lever on the run, not
	// a rewrite of what the suite asks for.
	if fields[3] != "1" {
		t.Errorf("the row reports budget %q where the caller asked for 1; under WAIT_SCALE the "+
			"trace would then describe budgets nobody wrote, and a distribution read off it "+
			"would justify raising a number that had already been raised", fields[3])
	}
	if elapsed < 2500*time.Millisecond {
		t.Errorf("wait_until 1 under WAIT_SCALE=3 gave up after %s, which is inside the "+
			"UNSCALED band of one to two seconds: the multiplier never reached the budget, "+
			"so a scaled run measures the unscaled wait", elapsed)
	}

	// The other half. Unset, the instrument must not exist: these suites run in
	// CI and on a maintainer's station with nothing set, and a trace that wrote
	// a file or a line there would be a side effect nobody asked for.
	silent := filepath.Join(dir, "unasked.tsv")
	out = runWaiting(t, `
unset WAIT_TRACE
export WAIT_SCALE=1
if wait_until 1 false; then echo "verdict=passed"; else echo "verdict=failed"; fi
`, silent)
	if !strings.Contains(out, "verdict=failed") {
		t.Errorf("without WAIT_TRACE the wait changed its verdict:\n%s", out)
	}
	if _, err := os.Stat(silent); !os.IsNotExist(err) {
		t.Errorf("a trace file appeared with WAIT_TRACE unset (%v): the instrument is supposed "+
			"to be absent unless asked for", err)
	}
}
