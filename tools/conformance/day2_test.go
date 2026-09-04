package conformance

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The Day-2 leg (#673), held at the level a test can hold it: the read-back
// comparator, the bounded wait, the control of the control, the catalogue's
// size, and the order the leg plays its three properties in. What a real
// emulator answers to the catalogue is the leg's own business, under
// `mise run conformance:day2`.

// runDay2 runs a script against day2lib.sh under a bound of its own, and the
// bound is the point. The library's wait is bounded, and the test that says
// so used to prove it only by the package timeout: with the bound neutralised
// the loop diverged, `go test` killed the test binary ten minutes later, and
// the shell went on sleeping under it — two such shells were found alive on
// 2026-09-04, forty minutes in, their copies already deleted. A guard proved
// by a timeout is not proved. Here the process is killed at ten seconds, the
// exit says so, and nothing outlives the test.
func runDay2(t *testing.T, script string) (int, string) {
	t.Helper()
	requireTool(t, "bash")
	requireTool(t, "jq")
	lib, err := filepath.Abs("day2lib.sh")
	if err != nil {
		t.Fatalf("locate day2lib.sh: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", //nolint:gosec // fixed library, test-controlled script
		`set -uo pipefail
fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }
DIR="$1"
. "$2"
`+script,
		"bash", t.TempDir(), lib)
	// The whole process group goes with the shell, so a `sleep` the loop
	// forked does not hold the pipe open past the kill.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return -1, string(out) + "\n(killed: the script did not stop on its own within 10s)"
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run the shell script: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}

// A 200 is not evidence: the comparator finds the wanted value, refuses a
// wrong one, and refuses an absent path rather than reading it as anything.
func TestTheReadBackComparatorRefusesAWriteThatDidNotHappen(t *testing.T) {
	code, out := runDay2(t, `printf '%s' '{"server":{"name":"platform-web-0-day2"}}' | d2_says "rename platform-web-0" .server.name platform-web-0-day2`)
	if code != 0 || !strings.Contains(out, "ok: rename platform-web-0: read back") {
		t.Fatalf("a read that says the change happened was refused (exit %d):\n%s", code, out)
	}
	code, out = runDay2(t, `printf '%s' '{"server":{"name":"platform-web-0"}}' | d2_says "rename platform-web-0" .server.name platform-web-0-day2`)
	if code == 0 || !strings.Contains(out, "does not say the change happened") || !strings.Contains(out, "#654") {
		t.Fatalf("a read that still carries the old value passed (exit %d):\n%s", code, out)
	}
	code, out = runDay2(t, `printf '%s' '{"server":{"name":"platform-web-0"}}' | d2_says "rename platform-web-0" .server.renamed platform-web-0`)
	if code == 0 || !strings.Contains(out, "is 'null'") {
		t.Fatalf("an absent path passed (exit %d):\n%s", code, out)
	}
}

// The wait is bounded, and it says how long it waited.
func TestTheDay2WaitIsBoundedAndSaysHowLongItWaited(t *testing.T) {
	code, out := runDay2(t, `d2_settles 2 "reboot" .a c printf '%s' '{"a":"b"}'`)
	if code == -1 {
		t.Fatalf("the wait did not stop on its own: a budget of 2s ran past the runner's 10s, which is the unbounded loop this test exists to refuse:\n%s", out)
	}
	if code == 0 || !strings.Contains(out, "is still 'b' after 2s, wanted 'c'") {
		t.Fatalf("a value that never arrives was waited for and passed (exit %d):\n%s", code, out)
	}
	// The reader is run in a subshell by the wait, so it counts its calls in
	// a file rather than in a variable that would never persist.
	code, out = runDay2(t, `reader() { echo x >>"$DIR/calls"; if [ "$(wc -l <"$DIR/calls")" -ge 2 ]; then printf '%s' '{"a":"c"}'; else printf '%s' '{"a":"b"}'; fi; }
d2_settles 5 "reboot" .a c reader`)
	if code != 0 {
		t.Fatalf("a value that arrives was not seen (exit %d):\n%s", code, out)
	}
}

// TestTheDay2ReaderControlFindsABrokenComparator is the control of the
// control: with the comparator stubbed to pass everything, the reader control
// must fail, or the wrong value it claims to plant proves nothing.
func TestTheDay2ReaderControlFindsABrokenComparator(t *testing.T) {
	code, out := runDay2(t, `d2_reader_control`)
	if code != 0 {
		t.Fatalf("the reader control fails on the real comparator (exit %d):\n%s", code, out)
	}
	code, out = runDay2(t, `d2_says() { ok "$1: stubbed"; }
d2_reader_control`)
	if code == 0 || !strings.Contains(out, "passed a planted wrong value") {
		t.Fatalf("the control did not notice a comparator that passes everything (exit %d):\n%s", code, out)
	}
}

// The catalogue is many writes, not a smoke test: every step named exists as
// a function, and the catalogue AS PLAYED — every step stubbed to record its
// name, then d2_catalogue_scaleway run — is long enough for the month the
// issue describes. As played and not as declared, because a catalogue that
// declares thirty steps and plays one is the smoke test in disguise.
func TestTheDay2CatalogueIsAMonthOfWrites(t *testing.T) {
	code, out := runDay2(t, `for step in "${D2_SCALEWAY_STEPS[@]}"; do
	declare -F "d2_step_$step" >/dev/null || echo "MISSING $step"
	eval "d2_step_$step() { echo $step >>\"\$DIR/played\"; }"
done
d2_catalogue_scaleway
sort -u "$DIR/played" | wc -l | tr -d ' '`)
	if code != 0 || strings.Contains(out, "MISSING") {
		t.Fatalf("a step of the catalogue has no function (exit %d):\n%s", code, out)
	}
	var played int
	sscanInt(strings.TrimSpace(out), &played)
	if played < 20 {
		t.Fatalf("the catalogue plays %d distinct steps; a month of an operator's changes is more than that:\n%s", played, out)
	}
}

func sscanInt(line string, out *int) {
	n := 0
	for _, r := range line {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	*out = n
}

// TestTheDay2LegPlaysItsThreePropertiesInOrder holds the leg to the order
// that makes it a measurement: the controls before any verdict, the shape
// captured before the catalogue, the catalogue, then the shape compared, the
// emulator's own verification read, and the stack gate replayed on the same
// stack — all before the stack comes down.
func TestTheDay2LegPlaysItsThreePropertiesInOrder(t *testing.T) {
	leg, err := os.ReadFile("day2.sh")
	if err != nil {
		t.Fatalf("read day2.sh: %v", err)
	}
	text := string(leg)
	marks := []string{
		"d2_reader_control",
		"fnl_shape_reader_control",
		`d2_live_shape "$machine" "$WORK/shape-before.txt"`,
		"d2_catalogue_scaleway",
		`"$WORK/shape-before.txt" "$WORK/shape-after.txt"`,
		`guard.sh" verification "$ENDPOINT"`,
		`FEINT_STACK_UP="$WORK"`,
		`"$FEINT" down) >"$WORK/down.log"`,
	}
	last := -1
	for _, mark := range marks {
		at := strings.Index(text, mark)
		if at < 0 {
			t.Fatalf("day2.sh does not carry %q", mark)
		}
		if at < last {
			t.Fatalf("day2.sh plays %q before what must precede it", mark)
		}
		last = at
	}
	if !strings.Contains(text, "the data-plane half was not asked") {
		t.Error("a run with no runtime does not say what it did not prove")
	}
}

// TestTheStackGateJudgesAStackAnotherSuiteBroughtUp: with FEINT_STACK_UP the
// stack gate neither brings a stack up nor takes it down, and asks no
// doorstep question of a host the owner already asked.
func TestTheStackGateJudgesAStackAnotherSuiteBroughtUp(t *testing.T) {
	gate := readGate(t)
	for _, mark := range []string{
		`if [ -n "${FEINT_STACK_UP:-}" ]; then`,
		`WORK="$FEINT_STACK_UP"`,
		"the stack stays up for its owner to bring down",
		`[ -n "${FEINT_STACK_UP:-}" ] || guard_leftovers_for "$RUNTIME" doorstep`,
	} {
		if !strings.Contains(gate, mark) {
			t.Errorf("functional.sh does not carry %q; a Day-2 leg could not replay its verdicts on the stack it mutated", mark)
		}
	}
}

// TestARedReadBackReddensTheLeg: a read after a write that does not say the
// change happened must end the leg, not print and move on. Measured on
// 2026-09-04 on 116d181: the reverse step printed FAIL, the leg played 43
// more writes and exited 0. The write and the read are planted; the read
// answers null where the step wants "wanted", and nothing may run after it.
func TestARedReadBackReddensTheLeg(t *testing.T) {
	// Both read-backs of a pair, each on its own: the planted read answers
	// null, so wanting "wanted" on the set fails the first pipeline, and
	// wanting null on the set then "wanted" on the undo fails the second.
	for _, half := range []struct {
		name, setWant, backWant, step string
	}{
		{name: "set", setWant: "wanted", backWant: "null", step: "FAIL: probe: the read after"},
		{name: "undo", setWant: "null", backWant: "wanted", step: "FAIL: probe (undone): the read after"},
	} {
		t.Run(half.name, func(t *testing.T) {
			code, out := runDay2(t, `
D2_STEP=probe
d2_write() { :; }
d2_read() { printf '%s' '{"ip":{"reverse":null}}'; }
d2_pair "probe" PATCH /p .ip.reverse '{}' `+half.setWant+` '{}' `+half.backWant+`
echo "CONTINUED"
`)
			if code == 0 {
				t.Errorf("a red read-back on the %s left the leg green (exit 0):\n%s", half.name, out)
			}
			if !strings.Contains(out, half.step) {
				t.Errorf("the red read-back on the %s was not reported as such:\n%s", half.name, out)
			}
			if strings.Contains(out, "CONTINUED") {
				t.Errorf("the leg went on after a red read-back on the %s:\n%s", half.name, out)
			}
		})
	}
}

// TestAStepThatFailsStopsTheCatalogue is the second net: a step that returns
// non-zero for any other reason stops the catalogue where it stands.
func TestAStepThatFailsStopsTheCatalogue(t *testing.T) {
	code, out := runDay2(t, `
d2_step_planted() { echo "planted step ran"; return 1; }
d2_step_after() { echo "AFTER-RAN"; }
D2_SCALEWAY_STEPS=(planted after)
d2_catalogue_scaleway
echo "CONTINUED"
`)
	if code == 0 || strings.Contains(out, "AFTER-RAN") || strings.Contains(out, "CONTINUED") {
		t.Errorf("the catalogue went past a step that failed (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "planted step ran") {
		t.Errorf("the planted step did not run at all:\n%s", out)
	}
}

// TestTheReverseStepUndoesWithNullAndExpectsNull holds the step against the
// measurement of #676: the undo sends null and expects null back. A read that
// answers "" on the undo — what this emulator answered before #676 — must fail
// the step, and one that answers null must pass it.
func TestTheReverseStepUndoesWithNullAndExpectsNull(t *testing.T) {
	prelude := `
D2_ZONE=fr-par-1; D2_IPWEB0=ip-web0
d2_write() { printf '%s\n' "$3" >>"$DIR/writes"; }
`
	code, out := runDay2(t, prelude+`
: >"$DIR/reads"
d2_read() { echo x >>"$DIR/reads"; if [ "$(wc -l <"$DIR/reads")" = 1 ]; then printf '%s' '{"ip":{"reverse":"web0.platform.example"}}'; else printf '%s' '{"ip":{"reverse":null}}'; fi; }
d2_step_ip_reverse
echo "PASSED-NULL"
cat "$DIR/writes"
`)
	if code != 0 || !strings.Contains(out, "PASSED-NULL") {
		t.Errorf("the reverse step refused a cloud that clears to null (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, `{"reverse":null}`) {
		t.Errorf("the undo did not send null:\n%s", out)
	}
	code, out = runDay2(t, prelude+`
: >"$DIR/reads"
d2_read() { echo x >>"$DIR/reads"; if [ "$(wc -l <"$DIR/reads")" = 1 ]; then printf '%s' '{"ip":{"reverse":"web0.platform.example"}}'; else printf '%s' '{"ip":{"reverse":""}}'; fi; }
d2_step_ip_reverse
echo "PASSED-EMPTY"
`)
	if code == 0 || strings.Contains(out, "PASSED-EMPTY") {
		t.Errorf("the reverse step accepted a reverse that reads \"\" after being cleared, the pre-#676 answer (exit %d):\n%s", code, out)
	}
}
