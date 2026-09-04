package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Day-2 leg (#673), held at the level a test can hold it: the read-back
// comparator, the bounded wait, the control of the control, the catalogue's
// size, and the order the leg plays its three properties in. What a real
// emulator answers to the catalogue is the leg's own business, under
// `mise run conformance:day2`.

func runDay2(t *testing.T, script string) (int, string) {
	t.Helper()
	requireTool(t, "bash")
	requireTool(t, "jq")
	lib, err := filepath.Abs("day2lib.sh")
	if err != nil {
		t.Fatalf("locate day2lib.sh: %v", err)
	}
	return runShell(t, t.TempDir(), lib, script)
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
