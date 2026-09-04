package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/environment"
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
export FEINT_RUN_DIR="$DIR/run"
mkdir -p "$FEINT_RUN_DIR"
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

// TestTheSweepRefusesARuntimeWideSweepWhileAnotherEmulatorAnswers is the
// rule the incident of 2026-09-04 13:01 wrote: the registry holds the run's
// own address and a foreign one that still answers, the stack directory is
// gone, and the runtime-wide clean — the only removal that would reach the
// foreign emulator's machines — must be refused by name, with nothing read
// in its place and no "swept".
func TestTheSweepRefusesARuntimeWideSweepWhileAnotherEmulatorAnswers(t *testing.T) {
	_, out := runDay2(t, fakeFeint+`
mkdir -p "$FEINT_RUN_DIR/127.0.0.1_1" "$FEINT_RUN_DIR/127.0.0.1_4690"
d2_still_answers() { [ "$1" = "http://127.0.0.1:4690" ]; }
if d2_sweep "$DIR/gone" yes "$DIR/feint" 127.0.0.1:1 incus-ovn; then echo "SWEEP-CLAIMED"; fi
cat "$FEINT_CALLS"
`)
	for _, want := range []string{"another emulator answers on 127.0.0.1:4690", "call: stop -addr 127.0.0.1:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the sweep on a shared runtime does not carry %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"call: clean -vm incus-ovn -closing", "call: clean -check", "SWEEP-CLAIMED", "swept,"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the sweep reached the runtime while another emulator answered (%q):\n%s", forbidden, out)
		}
	}
}

// TestAStaleRegistryEntryDoesNotBlockTheSweep is the accepting half: an
// address that no longer answers is a stale registration (an emulator killed
// without its stop), and the run's own address is not foreign. Both are
// walked past, and the sweep proceeds to the runtime.
func TestAStaleRegistryEntryDoesNotBlockTheSweep(t *testing.T) {
	_, out := runDay2(t, fakeFeint+`
mkdir -p "$FEINT_RUN_DIR/127.0.0.1_1" "$FEINT_RUN_DIR/127.0.0.1_4596"
d2_still_answers() { return 1; }
d2_sweep "$DIR/gone" yes "$DIR/feint" 127.0.0.1:1 incus-ovn || echo "SWEEP-FAILED"
cat "$FEINT_CALLS"
`)
	for _, want := range []string{"call: clean -vm incus-ovn -closing", "call: clean -check -vm incus-ovn -closing", "swept, nothing answers on 127.0.0.1:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("a stale registry entry blocked the sweep, missing %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"SWEEP-FAILED", "another emulator answers"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a stale registry entry read as a foreign emulator (%q):\n%s", forbidden, out)
		}
	}
}

// TestTheDay2LegDeclaresAnEmulatorThatCleansUpAfterItself: the declaration the
// leg writes is read back by the same loader `feint up` uses, and it says
// cleanup — so the emulator the leg starts takes its own machines down on
// `feint stop -addr`, which is what the sweep's fallback removes with. The
// example stack itself is left as the maintainer wrote it (no cleanup, mode
// off); only the copy is touched, once, and a second call changes nothing.
func TestTheDay2LegDeclaresAnEmulatorThatCleansUpAfterItself(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "stacks", "scaleway", "feint.yaml"))
	if err != nil {
		t.Fatalf("read the example declaration: %v", err)
	}
	if before, err := environment.Parse(string(example)); err != nil {
		t.Fatalf("the example declaration does not parse: %v", err)
	} else if before.Emulator.Cleanup {
		t.Fatal("the example stack already declares cleanup, so this test no longer measures the leg's own insertion")
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "feint.yaml")
	if err := os.WriteFile(copyPath, example, 0o600); err != nil {
		t.Fatal(err)
	}
	_, out := runDay2(t, `
sed -i "s|127.0.0.1:4599|127.0.0.1:4596|g" "`+copyPath+`"
d2_declare_cleanup "`+copyPath+`" || echo "DECLARE-FAILED"
d2_declare_cleanup "`+copyPath+`" || echo "DECLARE-FAILED"
`)
	if strings.Contains(out, "DECLARE-FAILED") {
		t.Fatalf("d2_declare_cleanup failed:\n%s", out)
	}
	written, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	decl, err := environment.Parse(string(written))
	if err != nil {
		t.Fatalf("the declaration the leg writes does not parse:\n%s\n%v", written, err)
	}
	if !decl.Emulator.Cleanup {
		t.Errorf("the declaration the leg writes does not make its emulator clean up after itself:\n%s", written)
	}
	if decl.Emulator.Addr != "127.0.0.1:4596" {
		t.Errorf("the address the leg wrote was lost: %q", decl.Emulator.Addr)
	}
	if strings.Count(string(written), "cleanup: true") != 1 {
		t.Errorf("cleanup was declared %d times, want once:\n%s", strings.Count(string(written), "cleanup: true"), written)
	}
	leg, err := os.ReadFile("day2.sh")
	if err != nil {
		t.Fatal(err)
	}
	at, up := strings.Index(string(leg), `d2_declare_cleanup "$WORK/feint.yaml"`), strings.Index(string(leg), `"$FEINT" up --runtime`)
	if at < 0 || up < 0 || at > up {
		t.Error("day2.sh does not declare cleanup on its copy before it brings the stack up")
	}
}
