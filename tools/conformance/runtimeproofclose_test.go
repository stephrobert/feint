package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

// The runtime proof ends on the host state its own doorstep accepts (#521).
//
// It did not, and it is what the incus-ovn leg failed on both nights it got far
// enough to be asked. The leg's suites left four objects on the host, `feint
// stop` gave none of them back, and the *next* step — the witness gate, whose
// subject is something else entirely — met them at its own doorstep and
// reported "a previous run left …" on a GitHub runner nothing had ever touched.
// Two defects in one line: the run that leaked was not the run that went red,
// and the sentence named a run that never existed.
//
// `mise run conformance` and `tools/conformance/leg.sh` had carried the fixed
// form since #521 — stop on a line of its own, then the question — and the
// workflow had not. This holds all three to it, and holds the workflow to the
// closing spelling in particular: `leftovers`, the doorstep form, would refuse
// on exactly the same objects and blame the wrong run for them, which is a
// green-looking half-fix.
func TestTheRuntimeProofEndsOnItsOwnDoorstep(t *testing.T) {
	root := repoRoot(t)
	body := readFile(t, filepath.Join(root, ".github", "workflows", "runtime-proof.yml"))
	lines := strings.Split(body, "\n")

	stopAt, askedAt := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "run:") && strings.Contains(trimmed, "feint stop --addr 127.0.0.1:4599") {
			stopAt = i
		}
		if stopAt >= 0 && i > stopAt && strings.Contains(trimmed, "guard.sh leftovers-after") {
			askedAt = i
			break
		}
	}
	if stopAt < 0 {
		t.Fatal("the runtime proof never stops its emulator on a step of its own, so nothing can " +
			"ask what the leg left once the emulator is gone (#521)")
	}
	if askedAt < 0 {
		t.Fatalf("the runtime proof never asks the closing doorstep after its stop: whatever its "+
			"suites leave is met by the next step instead, which reports it as a previous run's "+
			"and reddens the wrong subject. It stops at line %d and asks nothing after it.", stopAt+1)
	}

	// And the step must be able to go red on the path it judges. `if: always()`
	// here would fire on a leg that had already failed, where the leftovers are
	// the wreck of a crash and not a leak, and bury the finding under noise —
	// the same reason `mise run conformance` puts this after its trap rather
	// than inside it.
	if condition := stepConditionAbove(lines, askedAt); condition != "" {
		t.Errorf("the closing doorstep carries `%s`: it then reports a failed leg's wreckage as a "+
			"leak, and the run that really leaked is buried under a second red", condition)
	}

	// The doorstep spelling at the same position would be the half-fix: same
	// refusal, wrong culprit named.
	for i := stopAt; i < askedAt; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.Contains(trimmed, "guard.sh leftovers ") {
			t.Errorf("line %d asks the doorstep form after the stop, which blames a previous run "+
				"for what this leg left: %s", i+1, trimmed)
		}
	}
}

// stepConditionAbove returns the `if:` of the step containing the given line,
// or "" when the step carries none. Steps start at a `- name:` entry, so the
// search walks back to it.
func stepConditionAbove(lines []string, at int) string {
	for i := at; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "if:") {
			return trimmed
		}
		if strings.HasPrefix(trimmed, "- name:") {
			return ""
		}
	}
	return ""
}
