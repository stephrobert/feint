package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The nightly runtime proof asks the emulator what it verified, and gates on
// the answer.
//
// It did not, for as long as the gate existed (#740). The `runtime` job ran the
// four dataplane suites and the leftovers doorstep and nothing else of
// guard.sh, while both local reproductions of those same suites —
// tools/conformance/leg.sh and tools/conformance/day2.sh — call
// `guard.sh verification` before the emulator stops. So a claim the emulator
// itself published as broken reddened a workstation and never a night, and
// `runtime-proof` is the gate #736 wanted a green night from before tagging: a
// green there did not say the claims held.
//
// Three properties, and the first alone would be a comment:
//
//  1. the step exists in the job that boots machines;
//  2. it runs BEFORE the emulator is stopped, because the counters and the
//     claims die with the process (#670);
//  3. it is `if: always()`, so a suite that already failed still gets its
//     claims reported rather than hiding them behind the first red.
func TestTheRuntimeProofAsksWhatTheEmulatorVerified(t *testing.T) {
	workflow := readWorkflow(t, "runtime-proof.yml")

	const step = "tools/conformance/guard.sh verification"
	if !strings.Contains(workflow, step) {
		t.Fatalf("runtime-proof.yml never runs %q: the four dataplane suites pass, the "+
			"emulator publishes a broken claim on /_feint/health, and the night says green", step)
	}

	verifyAt := strings.Index(workflow, step)
	stopAt := strings.Index(workflow, "feint stop --addr 127.0.0.1:4599")
	if stopAt < 0 {
		t.Fatal("runtime-proof.yml no longer stops its emulator; this test's ordering check has lost its subject")
	}
	if verifyAt > stopAt {
		t.Error("the verification step runs after the emulator is stopped: the claims and the " +
			"counters die with the process, so it would ask a dead endpoint and pass on nothing")
	}

	// The `if:` of the step itself, read from the block that declares it rather
	// than from anywhere in the file: a distant `if: always()` would satisfy a
	// naive Contains and prove nothing about this step.
	//
	// And compared as a whole LINE, not as a substring. `if: always() == false`
	// contains `if: always()`, so a substring check calls a step that can never
	// run correctly armed — the falsification for this spec is that exact
	// mutation, and it stayed green until this read the line.
	block := stepBlock(workflow, "What the emulator verified against every plan")
	if block == "" {
		t.Fatal("no step named `What the emulator verified against every plan`: the name is what a " +
			"reader of a job log looks for, and what night-report.yml quotes when it fails")
	}
	if got := conditionOf(block); got != "always()" {
		t.Errorf("the verification step is `if: %s`, want `if: always()`: a suite failing earlier "+
			"would skip it, and the claims of the very run that went wrong are the ones worth reading", got)
	}
	if !strings.Contains(block, step) {
		t.Errorf("the step named for the verification does not run it:\n%s", block)
	}
}

// stepBlock returns the lines of one `- name:` step, up to the next step at the
// same indentation. Returns "" when no step carries that name.
func stepBlock(workflow, name string) string {
	start := strings.Index(workflow, "- name: "+name)
	if start < 0 {
		return ""
	}
	rest := workflow[start+1:]
	if next := strings.Index(rest, "\n      - name: "); next >= 0 {
		return workflow[start : start+1+next]
	}
	return workflow[start:]
}

// conditionOf answers a step's `if:` expression, or "" when it declares none.
//
// The whole line, so a check on it cannot be satisfied by a longer expression
// that merely starts the same way.
func conditionOf(block string) string {
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if after, found := strings.CutPrefix(trimmed, "if:"); found {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return withoutComments(string(body))
}

// withoutComments drops every whole-line YAML comment.
//
// Without it this test reads text rather than steps, and a step commented out
// still satisfies every `strings.Contains` below — which is the difference
// between checking a form and checking a behaviour. The falsification for this
// change is exactly that mutation: comment the step out, keeping every name in
// the file, and the night stops asking what the emulator verified while a
// grep-shaped test stays green.
//
// Whole-line only, on purpose. A `#` inside a value is part of the value, and a
// blanket strip would cut `127.0.0.1:4599 # the emulator` down to something this
// test's ordering check could no longer find.
func withoutComments(workflow string) string {
	lines := strings.Split(workflow, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
