package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A job that runs one of this repository's scripts has to check the repository
// out (#795).
//
// THREE TIMES, measured, each one a workflow step calling a file that was not
// where the step ran:
//
//  1. 2026-09-25, #794: the stacks control leg moved its working tree to
//     v0.13.0 and then called `tools/ci/fetch.sh`, added after that tag. Exit
//  127. Held now by TestTheControlNeverRunsARepositoryScriptInsideTheTag.
//  2. 2026-10-01, #795: the streak job calls `tools/ci/streak-report.sh` and
//     checks nothing out at all — it reads the Actions API, so it never needed
//     the repository until that step was added. Exit 127, every night for six
//     nights.
//  3. And the second one cost more than a red night. The streak job's failure
//     reddens the scheduled run, a red run resets the consecutive-green count,
//     and the count is what the step announces: the mechanism built so a met
//     criterion would not pass unnoticed destroyed the streak of fourteen it
//     existed to report, and then kept it at zero. A control that breaks its own
//     subject is worse than no control.
//
// So the rule is checked for every job of every workflow, not for the one that
// failed last. The two earlier tests each hold their own case; this one holds
// the class, and it is the class that kept recurring.
func TestEveryJobThatRunsARepositoryScriptChecksItOut(t *testing.T) {
	for _, name := range workflowFiles(t) {
		workflow := readWorkflow(t, name)
		for job, body := range jobsOf(workflow) {
			scripts := repositoryScriptsIn(body)
			if len(scripts) == 0 {
				continue
			}
			// A reusable workflow runs in its own right; the caller provides
			// nothing for it and checks nothing out on its behalf.
			if strings.Contains(body, "uses: ./.github/workflows/") {
				continue
			}
			checkout := strings.Index(body, "uses: actions/checkout@")
			if checkout < 0 {
				t.Errorf("%s: job %q runs %s and never checks the repository out, "+
					"so the step dies with `No such file or directory`, exit 127, before it "+
					"measures anything", name, job, strings.Join(scripts, ", "))
				continue
			}
			// BEFORE, not merely present. A checkout after the call is a checkout
			// that does nothing for it, and asserting presence alone would accept
			// it — falsify refused the mutation that would have shown this, which
			// is how the weakness surfaced.
			if first := firstScriptCallIndex(body); first >= 0 && first < checkout {
				t.Errorf("%s: job %q runs %s before it checks the repository out, so the call "+
					"happens in an empty working directory however correct the rest of the job is",
					name, job, strings.Join(scripts, ", "))
			}
		}
	}
}

// The accepting half, in the same package as the class above: a job that does
// check out must not be accused, or the guard would be satisfied by a workflow
// that runs no scripts at all — which is how a rule stops protecting anything.
func TestAJobThatChecksOutIsNotAccusedOfAnything(t *testing.T) {
	workflow := readWorkflow(t, "runtime-proof.yml")
	jobs := jobsOf(workflow)

	body, found := jobs["runtime"]
	if !found {
		t.Fatal("runtime-proof.yml has no runtime job: this test names the wrong one")
	}
	if len(repositoryScriptsIn(body)) == 0 {
		t.Error("the runtime job runs no repository script any more: this test no longer " +
			"demonstrates that the rule accepts the ordinary case")
	}
	if !strings.Contains(body, "uses: actions/checkout@") {
		t.Error("the runtime job stopped checking out, which would make the rule above vacuous here")
	}
}

// scriptCall matches an invocation of a repository script: a path under tools/
// ending in .sh or .py. Anchored on a word boundary so `mytools/x.sh` does not
// match, and `uses:` lines are excluded by the caller — an action reference is
// not a script this runner executes.
var scriptCall = regexp.MustCompile(`(^|[\s"'(=])\.?/?tools/[A-Za-z0-9._/-]+\.(sh|py)`)

// repositoryScriptsIn answers the repository scripts a job's body invokes.
func repositoryScriptsIn(body string) []string {
	var found []string
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		// An action reference, not a command. `uses: ./.github/...` is handled
		// by the caller; anything else under uses: is an external action.
		if strings.HasPrefix(trimmed, "uses:") {
			continue
		}
		for _, match := range scriptCall.FindAllString(line, -1) {
			script := strings.TrimLeft(match, " \t\"'(=")
			if !seen[script] {
				seen[script] = true
				found = append(found, script)
			}
		}
	}
	return found
}

// firstScriptCallIndex answers where a job's body first invokes a repository
// script, or -1 when it never does. By offset, because the question this test
// asks is one of order.
func firstScriptCallIndex(body string) int {
	at := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "uses:") && scriptCall.MatchString(line) {
			return at
		}
		at += len(line) + 1
	}
	return -1
}

// workflowFiles answers every workflow in .github/workflows.
func workflowFiles(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if name := entry.Name(); strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		t.Fatal("no workflow found: this test would pass by having nothing to look at")
	}
	return names
}

// jobsOf answers every job of a workflow, keyed by name.
//
// Jobs are the two-space keys under `jobs:`, and the next key at that exact
// indentation ends the one before it.
func jobsOf(workflow string) map[string]string {
	jobs := map[string]string{}
	at := strings.Index(workflow, "\njobs:\n")
	if at < 0 {
		return jobs
	}
	body := workflow[at+len("\njobs:\n"):]

	name := ""
	var current []string
	flush := func() {
		if name != "" {
			jobs[name] = strings.Join(current, "\n")
		}
	}
	for _, line := range strings.Split(body, "\n") {
		// A job key: exactly two spaces, then a name, then a colon.
		if key, isKey := jobKey(line); isKey {
			flush()
			name, current = key, nil
			continue
		}
		// A top-level key ends the jobs block.
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		current = append(current, line)
	}
	flush()
	return jobs
}

// jobKey answers a line's job name when the line declares one.
func jobKey(line string) (string, bool) {
	if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
		return "", false
	}
	rest := strings.TrimPrefix(line, "  ")
	name, _, found := strings.Cut(rest, ":")
	if !found || name == "" || strings.ContainsAny(name, " \t#") {
		return "", false
	}
	return name, true
}
