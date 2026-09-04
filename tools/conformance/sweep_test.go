package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sweep at the end of a Day-2 run sees what it sweeps (#673).
//
// Measured on 2026-09-04: the leg went red on #660, the stack gate's own trap
// removed the work directory it had been lent, the leg's trap then ran
// `cd "$WORK" && feint down` against a directory that no longer existed, the
// cd failed, the down never ran, `rm -rf` of an absent directory succeeded,
// and seven machines stayed up behind an exit that reported nothing. This
// repository paid for that exact shape once already (forty-eight live loops
// behind a cleanup that claimed success), so the sweep is now a function with
// a verdict, and these hold the verdict.

// fakeFeint is a feint that records what it was asked and does nothing: the
// sweep's own readings, not the binary's, decide whether "swept" is printed.
// FEINT_CHECK_RC is what `clean -check` answers.
const fakeFeint = `
cat >"$DIR/feint" <<'F'
#!/usr/bin/env bash
echo "call: $*" >>"$FEINT_CALLS"
if [ "$1" = clean ] && [ "$2" = -check ]; then exit "${FEINT_CHECK_RC:-0}"; fi
exit 0
F
chmod +x "$DIR/feint"
export FEINT_CALLS="$DIR/calls"
: >"$FEINT_CALLS"
`

// TestTheSweepRefusesToClaimWhatItDidNotSee: the down answered 0 and the
// emulator still answers, which is the cleanup that claimed success.
func TestTheSweepRefusesToClaimWhatItDidNotSee(t *testing.T) {
	_, out := runDay2(t, fakeFeint+`
mkdir -p "$DIR/work"
d2_still_answers() { return 0; }
if d2_sweep "$DIR/work" yes "$DIR/feint" 127.0.0.1:1 off; then echo "SWEEP-CLAIMED"; fi
cat "$FEINT_CALLS"
`)
	if !strings.Contains(out, "still answers on 127.0.0.1:1") {
		t.Errorf("an emulator that still answers after the sweep was not named:\n%s", out)
	}
	for _, forbidden := range []string{"SWEEP-CLAIMED", "swept,"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the sweep claimed success over an emulator that still answers (%q):\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "call: down") {
		t.Errorf("the down was never attempted from the directory that exists:\n%s", out)
	}
}

// TestTheSweepStopsTheEmulatorWhenTheStackDirectoryIsGone: no feint.yaml to
// read, so the emulator is stopped by address and the runtime swept, and the
// sweep says which way it went.
func TestTheSweepStopsTheEmulatorWhenTheStackDirectoryIsGone(t *testing.T) {
	_, out := runDay2(t, fakeFeint+`
d2_still_answers() { return 1; }
d2_sweep "$DIR/gone" yes "$DIR/feint" 127.0.0.1:1 incus-ovn || echo "SWEEP-FAILED"
cat "$FEINT_CALLS"
`)
	for _, want := range []string{
		"is gone",
		"call: stop -addr 127.0.0.1:1",
		"call: clean -vm incus-ovn -closing",
		"call: clean -check -vm incus-ovn -closing",
		"swept, nothing answers on 127.0.0.1:1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the sweep of a run whose directory is gone does not carry %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"call: down", "SWEEP-FAILED"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the sweep %q where the directory is gone:\n%s", forbidden, out)
		}
	}
}

// TestTheSweepReportsALeakTheRuntimeStillHolds: the emulator stopped, and the
// runtime still holds a machine of this run — the seven-machines case, read
// off `feint clean -check -closing` rather than assumed swept.
func TestTheSweepReportsALeakTheRuntimeStillHolds(t *testing.T) {
	_, out := runDay2(t, fakeFeint+`
export FEINT_CHECK_RC=1
mkdir -p "$DIR/work"
d2_still_answers() { return 1; }
if d2_sweep "$DIR/work" yes "$DIR/feint" 127.0.0.1:1 incus-ovn; then echo "SWEEP-CLAIMED"; fi
`)
	if !strings.Contains(out, "still on the incus-ovn runtime") {
		t.Errorf("a runtime that still holds this run's machines was not named:\n%s", out)
	}
	for _, forbidden := range []string{"SWEEP-CLAIMED", "swept,"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the sweep claimed success over a runtime that still holds machines (%q):\n%s", forbidden, out)
		}
	}
}

// TestTheSweepDownsFromTheDirectoryAndRemovesItLast is the accepting half: a
// down that took, nothing answering, nothing left on the runtime — and the
// directory goes after the down that needed it.
func TestTheSweepDownsFromTheDirectoryAndRemovesItLast(t *testing.T) {
	_, out := runDay2(t, fakeFeint+`
mkdir -p "$DIR/work"
d2_still_answers() { return 1; }
d2_sweep "$DIR/work" yes "$DIR/feint" 127.0.0.1:1 incus-ovn || echo "SWEEP-FAILED"
[ -d "$DIR/work" ] && echo "WORK-SURVIVED"
cat "$FEINT_CALLS"
`)
	for _, want := range []string{"call: down", "call: clean -check -vm incus-ovn -closing", "swept, nothing answers on 127.0.0.1:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("a clean sweep does not carry %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"SWEEP-FAILED", "WORK-SURVIVED", "call: stop"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a clean sweep printed %q:\n%s", forbidden, out)
		}
	}
	// A run that never came up sweeps nothing and says nothing: the trap runs
	// on every exit, including the one before the up.
	_, quiet := runDay2(t, fakeFeint+`
d2_sweep "" "" "$DIR/feint" 127.0.0.1:1 incus-ovn || echo "SWEEP-FAILED"
cat "$FEINT_CALLS"
`)
	if strings.TrimSpace(quiet) != "" {
		t.Errorf("a sweep before the up said or did something:\n%s", quiet)
	}
}

// TestTheStackGateRemovesOnlyTheDirectoryItCreated: functional.sh's trap, run
// as written, on a directory lent to it (FEINT_STACK_UP) and on one it made.
// The function is read out of the script rather than restated, so this holds
// the trap the gate actually installs; the two marks below hold that OWNED
// is set on the branch that creates the directory and cleared on the one
// that borrows it.
func TestTheStackGateRemovesOnlyTheDirectoryItCreated(t *testing.T) {
	gate, err := filepath.Abs("functional.sh")
	if err != nil {
		t.Fatalf("locate functional.sh: %v", err)
	}
	text, err := os.ReadFile(gate)
	if err != nil {
		t.Fatalf("read functional.sh: %v", err)
	}
	for _, mark := range []string{
		"WORK=\"$(mktemp -d)\"\n\t\tOWNED=\"yes\"",
		"WORK=\"$FEINT_STACK_UP\"\n\t\tUP=\"\"\n\t\tOWNED=\"\"",
	} {
		if !strings.Contains(string(text), mark) {
			t.Errorf("functional.sh does not set ownership where WORK is chosen, missing %q", mark)
		}
	}
	_, out := runDay2(t, `
eval "$(sed -n '/^cleanup()/,/^}/p' "`+gate+`")"
FEINT=/bin/true
mkdir -p "$DIR/owner" "$DIR/mine"
WORK="$DIR/owner"; UP=""; OWNED=""
cleanup
[ -d "$DIR/owner" ] && echo "OWNER-SURVIVED"
WORK="$DIR/mine"; UP=""; OWNED="yes"
cleanup
[ -d "$DIR/mine" ] || echo "MINE-REMOVED"
`)
	if !strings.Contains(out, "OWNER-SURVIVED") {
		t.Errorf("the stack gate's trap removed a directory lent to it under FEINT_STACK_UP:\n%s", out)
	}
	if !strings.Contains(out, "MINE-REMOVED") {
		t.Errorf("the stack gate's trap left its own directory behind:\n%s", out)
	}
}
