package ci

import (
	"regexp"
	"strings"
	"testing"
)

// A verdict about a whole run cannot be declared by the name of the leg (#789).
//
// The omission half of the field check (#88) asks whether *no answer of this
// run* carried a declared field. That is a statement about the run's entire
// population, so `tools/conformance/score.sh` only judges when the caller
// declares the run complete, through FEINT_FIELD_GATE. The declaration was
// sound; what was missing is that it was made from `matrix.client == 'fields'`
// — an intention, fixed before any work happened — and never compared with
// what the leg managed to do.
//
// Measured twice in ten days, both times on an upstream failure nobody here
// controls:
//
//  1. 2026-09-21 on #788: the Terraform provider registry answered 500, step 16
//     of 25 failed, steps 17 through 25 were skipped — the Outscale and Exoscale
//     Terraform suites, the example stacks, the quick start, fault injection, the
//     declared catalogue, the recorded refusals, the declared environment and the
//     probe. `Report what was exercised` runs `if: always()`, so it ran, judged,
//     and printed six Outscale operations as omitting fields that only the suites
//     which never started could have produced.
//  2. 2026-09-11 on the metrics pull request: same shape, a connection reset
//     instead of a 500, the same six operations named. Green on the second
//     attempt with nothing changed.
//
// Both reds were unreproducible locally, because `mise run conformance` drives
// every suite against one emulator — the population the verdict assumes. The
// richest run is the one on the workstation, and that is what makes this class
// of defect so easy to ship: per CLAUDE.md, a control whose verdict is about
// "this run" must be proved on the poorest run that will trigger it.
//
// So the rule is a class rule, and this test holds the class rather than the
// one step: wherever the gate is turned on, it is turned on by an effect of the
// run, and the step producing that effect must itself be skippable.
func TestTheFieldGateCannotBeDeclaredByTheLegsNameAlone(t *testing.T) {
	for _, name := range workflowFiles(t) {
		workflow := readWorkflow(t, name)
		for job, body := range jobsOf(workflow) {
			for _, declaration := range fieldGateDeclarations(body) {
				// Turning the gate off needs no precondition: score.sh then
				// prints the findings and judges none of them, which is safe
				// whatever the run did.
				if alwaysOff(declaration) {
					continue
				}
				sentinel := sentinelOf(declaration)
				if sentinel == "" {
					t.Errorf("%s: job %q turns FEINT_FIELD_GATE on from %q, which names no "+
						"step of the run: a leg called `fields` that died at step 16 of 25 is "+
						"still called `fields`, and the gate blames the suites that never ran",
						name, job, declaration)
					continue
				}
				// Naming a step is not enough: the gate must depend on that step
				// and on nothing else. `steps.whole.outputs.ran == '1' ||
				// matrix.client == 'fields'` reads as a belt-and-braces
				// improvement and is the original defect intact, because the
				// right-hand side is true on exactly the runs the left-hand side
				// exists to exclude.
				//
				// The leg's identity is already carried by the sentinel's own
				// `if:`, so the gate has no business reading the matrix a second
				// time. Found by `/falsify` refusing a mutation that deleted
				// `steps` — the refusal said the mutation was too destructive,
				// and the conservative version it asked for passed this test.
				if strings.Contains(declaration, "matrix.") {
					t.Errorf("%s: job %q decides FEINT_FIELD_GATE from %q, which still reads the "+
						"matrix: the matrix is what the leg was asked to do and the step is what it "+
						"did, so an `or` between them is satisfied by every run that was cut short",
						name, job, declaration)
					continue
				}
				step, found := stepWithID(body, sentinel)
				if !found {
					t.Errorf("%s: job %q reads steps.%s to decide the gate and declares no step "+
						"with that id: an unset output is the empty string, so the gate silently "+
						"never judges and the check this guards stops existing",
						name, job, sentinel)
					continue
				}
				// The whole mechanism is that GitHub skips this step when an
				// earlier one failed. `if: always()` or `if: failure()` would
				// make it run anyway, and the precondition would be satisfied
				// by precisely the runs it exists to exclude.
				// conditionOf is the package's own reader, shared with
				// the runtime-verification guard: one definition of what a
				// step's `if:` is, so the two cannot drift apart.
				if condition := conditionOf(step); runsDespiteFailure(condition) {
					t.Errorf("%s: job %q gates on steps.%s, whose own condition is %q: it runs "+
						"after an earlier step failed, so it reports a complete run on a run that "+
						"was cut short — the defect of #789 with one more step in front of it",
						name, job, sentinel, condition)
				}
			}
		}
	}
}

var (
	// `FEINT_FIELD_GATE: <anything to end of line>`.
	fieldGate = regexp.MustCompile(`(?m)^\s*FEINT_FIELD_GATE:\s*(.+)$`)
	// `steps.<id>.` inside an expression.
	sentinelRef = regexp.MustCompile(`steps\.([A-Za-z0-9_-]+)\.`)
)

// fieldGateDeclarations answers every value FEINT_FIELD_GATE is given in a job.
func fieldGateDeclarations(body string) []string {
	var found []string
	for _, match := range fieldGate.FindAllStringSubmatch(body, -1) {
		found = append(found, strings.TrimSpace(match[1]))
	}
	return found
}

// alwaysOff reports whether a declaration can only ever be the off value.
func alwaysOff(declaration string) bool {
	unquoted := strings.Trim(declaration, `'"`)
	return unquoted == "0"
}

// sentinelOf answers the step id a declaration reads, or "" when it reads none.
func sentinelOf(declaration string) string {
	if match := sentinelRef.FindStringSubmatch(declaration); match != nil {
		return match[1]
	}
	return ""
}

// stepWithID answers the body of the step declaring `id: <want>`.
//
// Steps are split on `- name:` rather than parsed: this package reads workflows
// as text throughout, and a YAML dependency to settle one boundary would be the
// first external one in the repository.
func stepWithID(body, want string) (string, bool) {
	for _, step := range strings.Split(body, "- name:") {
		for _, line := range strings.Split(step, "\n") {
			if strings.TrimSpace(line) == "id: "+want {
				return step, true
			}
		}
	}
	return "", false
}

// runsDespiteFailure reports whether a condition makes a step run after an
// earlier step of the job failed.
func runsDespiteFailure(condition string) bool {
	return strings.Contains(condition, "always()") ||
		strings.Contains(condition, "failure()") ||
		strings.Contains(condition, "cancelled()")
}
