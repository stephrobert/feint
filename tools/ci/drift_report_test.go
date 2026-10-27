package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The drift issue says what the scan found, and closes when there is nothing
// (#800).
//
// The report fired on "coverage changed OR contracts changed" and always said
// the same sentence: "operations upstream that nobody has triaged". On
// 2026-10-27 it said exactly that and then listed "nothing left to triage"
// three times, because what had moved was two counters — #821 served
// ImportSnapshotFromObjectStorage and the baseline had not followed the merge.
//
// An artefact falling behind is a pull request to merge. An undecided operation
// is a decision to make. Reporting the first as the second is how #800 reached
// twenty-one comments, which is the failure #795 named in the other mechanism:
// a notification that arrives whatever happened teaches its reader to skip it.
//
// These tests drive the real script with a stubbed `gh`, so the writes land in
// a log instead of on the repository's issues.

// driftStub answers the one read the script makes and records every write.
const driftStub = `#!/bin/sh
set -eu
case "$1 $2" in
  "issue list")
    cat "${GH_STUB_OPEN}"
    ;;
  "issue comment"|"issue create"|"issue close"|"label create")
    printf '%s\n' "$*" >>"${GH_STUB_LOG}"
    prev=""
    for a in "$@"; do
      if [ "${prev}" = "--body-file" ]; then cat "${a}" >>"${GH_STUB_LOG}"; fi
      prev="${a}"
    done
    ;;
  *)
    echo "unexpected gh call: $*" >&2
    exit 64
    ;;
esac
`

// runDriftReport executes drift-report.sh and returns its exit code, its
// output, and every gh write the stub received. open is what
// `gh issue list --json number --jq '.[0].number'` would answer.
func runDriftReport(t *testing.T, undecided, triage, open string, apply bool) (int, string, string) {
	t.Helper()

	dir := t.TempDir()
	stub := filepath.Join(dir, "gh")
	if err := os.WriteFile(stub, []byte(driftStub), 0o755); err != nil { //nolint:gosec // a stub this test runs
		t.Fatal(err)
	}
	openFile := filepath.Join(dir, "open.txt")
	if err := os.WriteFile(openFile, []byte(open), 0o600); err != nil {
		t.Fatal(err)
	}
	triageFile := filepath.Join(dir, "triage.txt")
	if triage != "" {
		if err := os.WriteFile(triageFile, []byte(triage), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "calls.log")

	args := []string{"drift-report.sh"}
	if apply {
		args = append(args, "--apply")
	}
	args = append(args, undecided, triageFile)

	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"GH_STUB_OPEN="+openFile,
		"GH_STUB_LOG="+log,
		"GITHUB_REPOSITORY=stephrobert/feint",
		"FEINT_RUN_URL=https://example.invalid/run/1",
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if ok := asExitError(err, &exit); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("run drift-report.sh: %v\n%s", err, out)
		}
	}
	written, _ := os.ReadFile(log) //nolint:gosec // a path this test made
	return code, string(out), string(written)
}

// Nothing undecided, an issue open: it closes, and says why.
//
// This is the case that produced twenty-one comments. The mechanism that opens
// on a finding must close on its absence, or the issue outlives its cause and
// reads as a standing problem nobody is fixing.
func TestADriftReportWithNothingUndecidedClosesTheIssue(t *testing.T) {
	code, out, writes := runDriftReport(t, "success", "provider scaleway: nothing left to triage\n", "800\n", true)
	if code != 0 {
		t.Fatalf("exit %d with nothing undecided: %s", code, out)
	}
	if !strings.Contains(writes, "issue close") || !strings.Contains(writes, "800") {
		t.Fatalf("an open issue was not closed when every operation was triaged:\n%s", writes)
	}
	if strings.Contains(writes, "nobody has triaged") {
		t.Errorf("it still claims operations are untriaged while closing:\n%s", writes)
	}
}

// Nothing undecided, no issue open: it says nothing at all.
//
// The ordinary night. A comment here is the noise this change removes.
func TestADriftReportWithNothingUndecidedAndNoIssueIsSilent(t *testing.T) {
	code, out, writes := runDriftReport(t, "success", "provider scaleway: nothing left to triage\n", "", true)
	if code != 0 {
		t.Fatalf("exit %d on a quiet night: %s", code, out)
	}
	if writes != "" {
		t.Errorf("a quiet night still wrote to the issues:\n%s", writes)
	}
	if !strings.Contains(out, "nothing to say") {
		t.Errorf("the verdict does not say it had nothing to report: %s", out)
	}
}

// Something undecided: that is what earns a notification.
//
// The accepting half. A report that never fires would pass the two tests above
// and lose the mechanism entirely — which is the opposite defect, and the one
// this whole scan exists to prevent.
func TestADriftReportWithUndecidedOperationsReports(t *testing.T) {
	const triage = "provider scaleway: 2 operations to triage\n  iam/v1alpha1  tokens  2  API.GetX, API.GetY\n"
	code, out, writes := runDriftReport(t, "failure", triage, "800\n", true)
	if code != 0 {
		t.Fatalf("exit %d with undecided operations: %s", code, out)
	}
	if !strings.Contains(writes, "issue comment") {
		t.Fatalf("undecided operations were not reported:\n%s", writes)
	}
	if !strings.Contains(writes, "nobody has triaged") {
		t.Errorf("the comment does not say what was found:\n%s", writes)
	}
	// The triage view rides along, because "something is undecided" without
	// naming it asks the reader to go and run the scan themselves.
	if !strings.Contains(writes, "API.GetX") {
		t.Errorf("the comment carries no triage view:\n%s", writes)
	}
	if strings.Contains(writes, "issue close") {
		t.Errorf("it closed the issue while something was undecided:\n%s", writes)
	}
}

// With no issue open and something undecided, it opens one.
func TestADriftReportOpensAnIssueWhenNoneExists(t *testing.T) {
	_, _, writes := runDriftReport(t, "failure", "provider scaleway: 1 operation to triage\n", "", true)
	if !strings.Contains(writes, "issue create") {
		t.Errorf("nothing was opened although an operation was undecided:\n%s", writes)
	}
}

// The branch is named, and the pull request is not promised.
//
// The old body said "the refreshed baseline is in the pull request tagged
// chore/upstream-drift". No such pull request exists on a scheduled night:
// opening one is a permission this repository withholds from Actions on
// purpose, so the sentence sent its reader looking for something that was never
// there.
func TestADriftReportDoesNotPromiseAPullRequestActionsCannotOpen(t *testing.T) {
	_, _, writes := runDriftReport(t, "failure", "provider scaleway: 1 operation to triage\n", "800\n", true)
	if !strings.Contains(writes, "chore/upstream-drift") {
		t.Errorf("the comment does not name the branch carrying the baseline:\n%s", writes)
	}
	if strings.Contains(writes, "in the pull request tagged") {
		t.Errorf("the comment still points at a pull request nothing opened:\n%s", writes)
	}
}

// Without --apply it writes nothing, so the decision can be read against any
// past night.
func TestTheDriftReportWritesNothingWithoutApply(t *testing.T) {
	_, out, writes := runDriftReport(t, "failure", "provider scaleway: 1 operation to triage\n", "800\n", false)
	if writes != "" {
		t.Errorf("it wrote without --apply:\n%s", writes)
	}
	if !strings.Contains(out, "the body it would post") {
		t.Errorf("a dry run does not show what it would post: %s", out)
	}
}
