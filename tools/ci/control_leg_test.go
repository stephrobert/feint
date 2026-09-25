package ci

import (
	"strings"
	"testing"
)

// The positive control must not run the repository's CI scripts from inside the
// tag it checked out (#794, found by #125's dispatch run).
//
// `runtime-proof.yml`'s stacks job runs twice: once on main, and once with the
// working tree moved to the last release tag, as a positive control — the thing
// that tells "our night is red" from "the world is red". The move is a
// `git checkout --detach`, so every step after it reads files as that tag had
// them.
//
// On 2026-09-25, #794 moved every workflow download onto `tools/ci/fetch.sh`.
// That file exists on main and not in v0.13.0, so the control died on
//
//	tools/ci/fetch.sh: No such file or directory
//	Process completed with exit code 127
//
// thirty-five seconds in, before a single stack was applied. Nothing on a pull
// request could have caught it: the stacks job only runs on the nightly
// schedule and on dispatch, and the control leg carries `continue-on-error`, so
// the failure does not even redden the job it belongs to — it was found by
// reading the run, which is the reading this test replaces.
//
// The rule it holds: everything that provisions the RUNNER runs before the
// move, everything that is the PRODUCT runs after it. `tools/ci/` is the CI
// harness, which lives on main by definition; `tools/conformance/` and the rest
// are the product, and running the tag's copy of those is the control's whole
// point.
func TestTheControlNeverRunsARepositoryScriptInsideTheTag(t *testing.T) {
	workflow := readWorkflow(t, "runtime-proof.yml")

	job := jobBlock(workflow, "stacks")
	if job == "" {
		t.Fatal("runtime-proof.yml has no stacks job: this test names the wrong one")
	}

	// The move itself, by the command that performs it rather than by the step's
	// title: a renamed step must not silently retire this check.
	cut := strings.Index(job, "git checkout --detach")
	if cut < 0 {
		t.Fatal("the stacks job no longer detaches onto a tag: either the control " +
			"leg is gone, or it moved somewhere this test cannot see it")
	}

	// The accepting half first, so a job that simply stopped having a control
	// cannot pass by being empty.
	if !strings.Contains(job[:cut], "tools/ci/fetch.sh") {
		t.Error("no CI helper runs before the control moves to the tag: the tools are " +
			"meant to be installed from main, for both legs, so that the witness differs " +
			"from its subject in the code alone")
	}

	for _, line := range strings.Split(job[cut:], "\n") {
		if strings.Contains(line, "tools/ci/") {
			t.Errorf("the control runs a CI script after checking out the tag, and a tag that "+
				"predates that script fails with exit 127 before it measures anything:\n  %s",
				strings.TrimSpace(line))
		}
	}
}

// jobBlock answers one job's body, from its key to the next job's.
//
// Jobs are the two-space keys under `jobs:`, so the next one at that exact
// indentation ends this one. Bounded on purpose: asserting over the whole file
// would forbid `tools/ci/` in jobs that never check out a tag, which is most of
// them and is perfectly correct there.
func jobBlock(workflow, name string) string {
	start := strings.Index(workflow, "\n  "+name+":\n")
	if start < 0 {
		return ""
	}
	rest := workflow[start+1:]
	for offset := 0; ; {
		next := strings.Index(rest[offset:], "\n  ")
		if next < 0 {
			return workflow[start:]
		}
		at := offset + next + len("\n  ")
		// A job key, not a deeper line: one more space means it is inside this job.
		if at < len(rest) && rest[at] != ' ' && strings.Contains(lineAt(rest, at), ":") {
			return rest[:offset+next]
		}
		offset = at
	}
}

// lineAt answers the line starting at an index.
func lineAt(s string, at int) string {
	if end := strings.IndexByte(s[at:], '\n'); end >= 0 {
		return s[at : at+end]
	}
	return s[at:]
}
