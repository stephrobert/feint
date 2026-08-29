package conformance

import (
	"os"
	"strings"
	"testing"
)

// The nightly's positive control, held by its structure rather than by its
// comments.
//
// The question this answers was the maintainer's, on 2026-08-29: when a night
// goes red, nothing says whether it is US or THE WORLD. Nine red nights that
// month, and at least three were the environment — an upstream image whose DHCP
// client changed behaviour, a CDN a container could not reach, leftovers from a
// leg. Untangling it took a day by hand.
//
// So the last release runs beside main. Three properties make that worth
// anything, and each of them is one edit away from being lost:
//
//  1. the control must NOT fail the job it rides, or `report` opens "the night
//     is red" about a tag nobody can fix — #538's defect exactly: one title,
//     five causes, none advancing;
//  2. the control's conclusion must be CONSUMED, or it is a green nobody
//     receives, which is what #604 is about;
//  3. the verdict job must NOT be a dependency of `report`, or the two subjects
//     fold back together.

const nightlyWorkflow = "../../.github/workflows/runtime-proof.yml"

func TestTheNightlyControlCannotOpenTheIssueAboutMain(t *testing.T) {
	source, err := os.ReadFile(nightlyWorkflow)
	if err != nil {
		t.Fatalf("read %s: %v", nightlyWorkflow, err)
	}
	workflow := string(source)

	// 1. The gate is non-blocking on the control leg alone. A bare
	// `continue-on-error: true` would silence main's own red, which is the
	// opposite defect and just as easy to write.
	// It must be on the JOB, not on the gate step alone. Measured on run
	// 33249879475, the first that played the control: it died at `Build the
	// machine images the stacks boot`, BEFORE the gate — so the job failed, and
	// tools/ci/night-report.sh reads every completed job of the run. On a
	// scheduled night that would have opened "the night is red" about a tag
	// nobody can fix, which is the poisoning this whole design claimed to
	// prevent.
	jobHead, _, _ := strings.Cut(workflow[strings.Index(workflow, "\n  stacks:\n"):], "steps:")
	if !strings.Contains(jobHead, "continue-on-error: ${{ matrix.control }}") {
		t.Error("the control leg can fail the stacks JOB, so a red control opens the issue about " +
			"main: a step-level continue-on-error covers nothing that happens before the gate")
	}
	if strings.Contains(jobHead, "continue-on-error: true") {
		t.Error("the stacks job is non-blocking for main too, which silences the very red the " +
			"nightly exists to report")
	}

	// 2. Somebody reads it. A conclusion no job consumes is the shape #604 names:
	// an instrument that exits with a verdict nothing receives.
	//
	// Asserted on the JOB rather than on the phrase, and that distinction was
	// measured: the first version looked for "Us, or the world?" anywhere, and
	// deleting the job left the phrase standing in the summary it writes, so the
	// mutation survived. A test that matches prose matches its own echo.
	if !strings.Contains(workflow, "\n  verdict:\n") {
		t.Error("no job reads the two legs, so the control is a second green nobody can interpret")
	}
	if !strings.Contains(workflow, "needs: [stacks]") {
		t.Error("the verdict job does not depend on the legs it reads, so it can publish before " +
			"either of them has a conclusion")
	}
	// And it publishes on a dispatch too. `report` and `streak` are schedule-only
	// because they open an issue and count a streak — facts about the nightly
	// SERIES — while this one reads the run it belongs to. Measured: the first
	// workflow_dispatch after the control landed (33249879475) ran the control
	// leg and would have printed nothing about it, which is a reading withheld
	// from the one person who asked for it.
	if i := strings.Index(workflow, "\n  verdict:\n"); i >= 0 {
		window := workflow[i:min(i+1200, len(workflow))]
		head, _, _ := strings.Cut(window, "steps:")
		if strings.Contains(head, "github.event_name == 'schedule'") {
			t.Error("the verdict job is schedule-only, so a dispatched run measures the control " +
				"and then says nothing about it")
		}
	}
	if !strings.Contains(workflow, `select(.name | startswith("stacks ("))`) {
		t.Error("the verdict job does not read the legs' real conclusions from the API, so it " +
			"cannot see a leg whose failure was made non-blocking")
	}

	// 3. The subjects stay apart. `report` opens the issue about main; the
	// verdict tells two populations apart. A `needs` that joined them would give
	// the issue two subjects.
	for _, job := range []string{"  report:", "  streak:"} {
		i := strings.Index(workflow, job)
		if i < 0 {
			t.Fatalf("the %q job is gone, so this test measures nothing about it", strings.TrimSpace(job))
		}
		window := workflow[i:min(i+400, len(workflow))]
		if strings.Contains(window, "verdict") {
			t.Errorf("%s depends on the verdict job, so the issue it opens would carry two "+
				"subjects — which is #538", strings.TrimSpace(job))
		}
	}

	// The control names its own subject. A control that did not say which tag it
	// ran would publish a verdict nobody can reproduce.
	if !strings.Contains(workflow, "git describe --tags --abbrev=0 --match 'v*'") {
		t.Error("the control does not resolve the last release tag, so it does not know its own subject")
	}
	if !strings.Contains(workflow, "fetch-depth: 0") {
		t.Error("the checkout does not fetch history, so `git describe` in the control has no tags to find")
	}
}
