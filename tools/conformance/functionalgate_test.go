package conformance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The stack gate says which runtime it answers under, and cannot be handed one
// nobody asked for (#504, and #574 one directory over).
//
// What the wrong output looked like is the whole danger: nothing was printed at
// all. `tools/conformance/functional.sh` opened with
//
//	RUNTIME="${FEINT_FUNCTIONAL_RUNTIME:-incus-ovn}"
//
// so `FEINT_VM=incus mise run conformance:functional` applied the stacks under
// OVN, reported green, and the operator read the verdict as the bridge's. That
// is the same line `mise run evidence:update` carried when it manufactured two
// false attributions during an Exoscale diagnosis, at three 1300-second passes
// each — and it is worse here, because the one assertion the two modes disagree
// about, isolation between two VPCs, is this gate's own subject.
//
// Driven against the gate itself rather than against the shared resolver: a
// resolver nothing calls resolves nothing, which is the instrument this
// repository keeps finding. Every case below stops the gate before it starts an
// emulator, so this test needs no runtime, no images and no clients.
func TestTheStackGateResolvesAndAnnouncesItsMode(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		says []string
	}{
		{
			// The budget is announced first, so this one case carries both the
			// default budget line and the refusal.
			name: "machines off is refused by name, and the budget was already said",
			env:  []string{"FEINT_VM=off"},
			says: []string{
				"the stack gate plays 3 pass(es) (this gate's default)",
				"the stack gate was asked for --vm off (FEINT_VM, exported by the caller)",
				"nothing to look at and nothing to look with",
			},
		},
		{
			name: "two different runtimes are refused rather than arbitrated",
			env:  []string{"FEINT_VM=incus-ovn", "FEINT_FUNCTIONAL_RUNTIME=incus", "FEINT_FUNCTIONAL_PASSES=1"},
			says: []string{
				"the stack gate plays 1 pass(es) (FEINT_FUNCTIONAL_PASSES, exported by the caller)",
				"below the default of 3",
				"FEINT_VM=incus-ovn and FEINT_FUNCTIONAL_RUNTIME=incus name two different runtimes for the stack gate",
			},
		},
		{
			name: "a budget that is not a number is refused, never rounded to one pass",
			env:  []string{"FEINT_VM=incus-ovn", "FEINT_FUNCTIONAL_PASSES=zero"},
			says: []string{"FEINT_FUNCTIONAL_PASSES is zero, which is not a number of passes"},
		},
		{
			name: "a budget of zero passes is refused: it would measure nothing and exit 0",
			env:  []string{"FEINT_VM=incus-ovn", "FEINT_FUNCTIONAL_PASSES=0"},
			says: []string{"FEINT_FUNCTIONAL_PASSES is 0, which is not a number of passes"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runStackGate(t, tc.env)
			if code == 0 {
				t.Fatalf("the gate accepted what it must refuse, and would have spent a run on it:\n%s", out)
			}
			for _, want := range tc.says {
				if !strings.Contains(out, want) {
					t.Errorf("the gate never said %q; it said:\n%s", want, out)
				}
			}
		})
	}
}

// The accepting half, without which every case above would pass on a gate that
// refuses everything: a mode it can honour is announced with its provenance and
// carried into the run.
//
// The mode named here is deliberately one no host has, so the gate announces
// it, fails `feint doctor` and skips — a fast, host-independent way to read the
// announcement of an ACCEPTED mode rather than of a refusal.
func TestTheStackGateAnnouncesTheModeItAccepts(t *testing.T) {
	// The gate reaches `feint doctor` only once jq, curl and incus are on PATH,
	// and it says so and exits 0 when they are not. Both endings prove the
	// announcement, which is what this asserts; what must never happen is the
	// announcement being absent.
	_, out := runStackGate(t, []string{"FEINT_VM=incus-nothing-declares-this"})
	want := "the stack gate runs under --vm incus-nothing-declares-this (FEINT_VM, exported by the caller)"
	if !strings.Contains(out, want) {
		t.Errorf("the gate never said %q, so it would answer under a mode nobody reads; it said:\n%s", want, out)
	}
}

// The literal must not come back, which is the direction a behaviour test
// cannot cover: a resolver call plus the old silent default would announce one
// mode and use another.
func TestTheStackGateAsksForItsModeRatherThanPinningIt(t *testing.T) {
	body := shellCode(t, "functional.sh")
	if !strings.Contains(body, "tools/runtime-mode.sh") {
		t.Error("functional.sh no longer asks tools/runtime-mode.sh which runtime it answers " +
			"under: whatever it picks instead, it picks silently (#504)")
	}
	if strings.Contains(body, `RUNTIME="${FEINT_FUNCTIONAL_RUNTIME:`) {
		t.Error("functional.sh pins its runtime from FEINT_FUNCTIONAL_RUNTIME again instead of " +
			"delegating the whole question: that is the line that ignored an exported FEINT_VM " +
			"and, one directory over, manufactured two false attributions (#574)")
	}
}

// The doorstep at BOTH ends, and the closing one was a comment (#504).
//
// The line read
//
//	guard_leftovers_for "$RUNTIME" "the end of the run"
//
// under a comment saying the doorstep question was asked again on the way out.
// It was not: guard_leftovers_for arms `--doorstep` on the literal `doorstep`
// alone, so the closing call asked about DHCP orphans and trapped objects and
// never once asked what machines and networks the pass had left standing. A run
// that leaked a network exited 0 and the next run met the refusal — which is
// exactly the state #521 removed from `mise run conformance`, described here in
// prose and never done.
//
// This reads the scope the file actually passes and then EXECUTES the guard
// with it, so it measures the effect rather than the spelling: any scope that
// does not arm the question fails, whatever it is called.
func TestTheStackGateEndsOnItsOwnDoorstep(t *testing.T) {
	body := shellCode(t, "functional.sh")

	// The reader proves it can find before it judges. Without the loop marker,
	// "no closing call after it" would be vacuously true.
	loopAt := strings.Index(body, `while [ "$pass" -le "$PASSES" ]`)
	if loopAt < 0 {
		t.Fatal("functional.sh no longer runs its stacks in a pass loop: the reader is the " +
			"suspect, not the gate")
	}

	call := regexp.MustCompile(`guard_leftovers_for "\$RUNTIME" ("[^"]*"|\S+)`)
	opening := call.FindStringSubmatch(body[:loopAt])
	if opening == nil {
		t.Fatal("functional.sh never asks the doorstep question before its pass loop: it would " +
			"start on a host an earlier run still holds and die minutes in on an address block")
	}
	closing := call.FindStringSubmatch(body[loopAt:])
	if closing == nil {
		t.Fatal("functional.sh never re-asks the doorstep question inside its pass loop: a pass " +
			"that leaks a machine or a network exits 0 and the next run pays for it (#493, #521)")
	}

	for _, scope := range []struct{ what, arg string }{
		{"the opening doorstep", opening[1]},
		{"the closing doorstep", closing[1]},
	} {
		code, out := bashGuard(t,
			`. "$1"; guard_leftovers_for "$2" `+scope.arg,
			"incus", stubBinary(t, 0))
		if code != 0 {
			t.Fatalf("%s refused a host the stub reports clean (exit %d):\n%s", scope.what, code, out)
		}
		if !strings.Contains(out, "--doorstep") {
			t.Errorf("%s of functional.sh passes %s, which does not ask what machines and "+
				"networks are standing — the question the comment beside it claims to ask:\n%s",
				scope.what, scope.arg, out)
		}
	}
}

// The budget the gate announces is the budget it plays (#504).
//
// A gate that prints "3 pass(es)" and runs one is the lying instrument this
// repository keeps meeting, and it would be worse than no budget at all: the
// number in the log is what a reader would take the green to mean.
//
// Structural, and said out loud rather than dressed as behaviour: driving three
// real passes takes fifteen minutes and a machine runtime, so what a unit test
// can hold is that the loop is bounded by the announced variable and that every
// pass names itself. The behavioural half is the run — `== pass 3 of 3` in the
// output of `FEINT_VM=incus-ovn mise run conformance:functional`.
func TestTheStackGatePlaysTheBudgetItAnnounces(t *testing.T) {
	body := shellCode(t, "functional.sh")

	if !strings.Contains(body, `PASSES="${FEINT_FUNCTIONAL_PASSES:-3}"`) {
		t.Error("the gate no longer defaults to three passes: the number was chosen against a " +
			"defect class that struck 9 times in 13 runs, and a single pass calls it absent " +
			"nearly one time in three (#504)")
	}

	loop := regexp.MustCompile(`while \[ "\$pass" -le (\S+) \]`).FindStringSubmatch(body)
	if loop == nil {
		t.Fatal("functional.sh no longer bounds its pass loop with `while [ \"$pass\" -le … ]`: " +
			"the reader is the suspect, not the gate")
	}
	if loop[1] != `"$PASSES"` {
		t.Errorf("the pass loop is bounded by %s rather than by the budget it announced: the gate "+
			"would print a number of passes and play another one", loop[1])
	}
	if !strings.Contains(body, `echo "== pass $pass of $PASSES"`) {
		t.Error("no pass names itself in the output, so a run that stopped after the first would " +
			"read exactly like a run that played them all")
	}
}

// shellCode is a script with its comment lines removed, because the two tests
// above ask what the gate DOES and this file quotes what it used to do. Read
// whole, `RUNTIME="${FEINT_FUNCTIONAL_RUNTIME:-incus-ovn}"` is present in
// functional.sh forever — inside the paragraph explaining why it was removed —
// and a reader that swallowed the prose would report the defect present for
// good, which is the mirror image of the comment that reads as a fix.
//
// It proves it can find before it judges: a stripped body that no longer holds
// the gate's own shebang means the reader is the suspect.
func shellCode(t *testing.T, path string) string {
	t.Helper()
	var kept []string
	for _, line := range strings.Split(readFile(t, path), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#!") ||
			!strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	body := strings.Join(kept, "\n")
	if !strings.Contains(body, "#!/usr/bin/env bash") {
		t.Fatalf("stripping the comments of %s left no script behind: the reader is the suspect", path)
	}
	return body
}

// runStackGate executes functional.sh with exactly the environment a case
// names, and nothing of the station's own.
//
// The four variables under test are stripped from the inherited environment for
// the reason mode_test.go strips its two: a station that already exports
// FEINT_VM would otherwise decide the verdict, which is the shape of the defect
// itself.
func runStackGate(t *testing.T, env []string) (code int, output string) {
	t.Helper()
	requireTool(t, "bash")

	script, err := filepath.Abs("functional.sh")
	if err != nil {
		t.Fatalf("locate functional.sh: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("functional.sh is not where this test looks (%s): %v", script, err)
	}

	cmd := exec.Command("bash", script) //nolint:gosec // a fixed script in this repository
	cmd.Env = append(gateEnvironment(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run the gate: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}

// gateEnvironment is the parent environment without the variables the gate
// reads, so a case says the whole truth about its own inputs.
func gateEnvironment() []string {
	stripped := []string{
		"FEINT_VM=", "FEINT_FUNCTIONAL_RUNTIME=", "FEINT_FUNCTIONAL_PASSES=",
		"FEINT_FUNCTIONAL_ADDR=", "FEINT_FUNCTIONAL_ZONE=",
	}
	out := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		skip := false
		for _, prefix := range stripped {
			if strings.HasPrefix(entry, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, entry)
		}
	}
	return out
}
