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
// `gh issue list --label drift --json number,title` would answer: the open
// issues wearing the label, newest first, which is the API's own order.
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

// What `gh issue list --label drift` answers, in the three shapes that matter.
//
// The label is not an identifier: anything about this mechanism wears it. On
// 2026-11-12 three open issues did — #800, which this script opened, plus #874
// and #886, which are humans writing about the mechanism itself.
const (
	noDriftIssue = `[]`

	ourIssueOpen = `[{"number": 800, "title": "Upstream API surface moved"}]`

	// Newest first, the API's own order, so somebody else's issue is the one
	// `.[0]` would have picked.
	someoneElsesIssueOpen = `[{"number": 886, "title": "fix(drift): make undecided backlog, contract changes and scan failures impossible to miss"},` +
		`{"number": 800, "title": "Upstream API surface moved"}]`

	onlySomeoneElsesIssue = `[{"number": 886, "title": "fix(drift): make undecided backlog, contract changes and scan failures impossible to miss"}]`
)

// Somebody else's drift issue is left alone, and ours is still found under it.
//
// The lookup was `--label drift` plus `.[0].number`, and the label is worn by
// every issue about this mechanism. With #886 open and newer than #800, the
// first quiet night would have closed #886 — the issue carrying the scope #892
// deliberately left open — with the words "Every operation upstream is triaged:
// implemented, or declined with a reason". True about the backlog, false about
// the issue it was written on, and the mechanism would have deleted the record
// of its own remaining work.
//
// Both halves are asserted: the one that must not be touched, and ours that
// must still be found beneath it.
func TestTheDriftReportLeavesSomebodyElsesDriftIssueAlone(t *testing.T) {
	_, out, writes := runDriftReport(t, "success", "provider scaleway: nothing left to triage\n",
		someoneElsesIssueOpen, true)
	if strings.Contains(writes, "886") {
		t.Errorf("it wrote to somebody else's drift issue:\n%s", writes)
	}
	if !strings.Contains(writes, "issue close") || !strings.Contains(writes, "800") {
		t.Errorf("it did not close its own issue, which sits under the newer one:\n%s\n%s", out, writes)
	}
}

// And with only somebody else's issue open, a quiet night writes nothing at
// all: there is nothing of ours to close.
func TestAQuietNightWithOnlySomebodyElsesIssueIsSilent(t *testing.T) {
	code, out, writes := runDriftReport(t, "success", "provider scaleway: nothing left to triage\n",
		onlySomeoneElsesIssue, true)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if writes != "" {
		t.Errorf("a quiet night wrote to an issue this script did not open:\n%s", writes)
	}
	if !strings.Contains(out, "nothing to say") {
		t.Errorf("the verdict does not say it had nothing to report: %s", out)
	}
}

// Nothing undecided, an issue open: it closes, and says why.
//
// This is the case that produced twenty-one comments. The mechanism that opens
// on a finding must close on its absence, or the issue outlives its cause and
// reads as a standing problem nobody is fixing.
func TestADriftReportWithNothingUndecidedClosesTheIssue(t *testing.T) {
	code, out, writes := runDriftReport(t, "success", "provider scaleway: nothing left to triage\n", ourIssueOpen, true)
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
	code, out, writes := runDriftReport(t, "success", "provider scaleway: nothing left to triage\n", noDriftIssue, true)
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
	code, out, writes := runDriftReport(t, "failure", triage, ourIssueOpen, true)
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
	_, _, writes := runDriftReport(t, "failure", "provider scaleway: 1 operation to triage\n", noDriftIssue, true)
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
	_, _, writes := runDriftReport(t, "failure", "provider scaleway: 1 operation to triage\n", ourIssueOpen, true)
	if !strings.Contains(writes, "chore/upstream-drift") {
		t.Errorf("the comment does not name the branch carrying the baseline:\n%s", writes)
	}
	if strings.Contains(writes, "in the pull request tagged") {
		t.Errorf("the comment still points at a pull request nothing opened:\n%s", writes)
	}
}

// A scan that could not conclude closes nothing and says nothing.
//
// The argument used to be a step outcome, which is "success", "failure",
// "cancelled" or "skipped", and everything that was not "failure" took the
// closing branch. So a scan that was killed — a lost runner, a cancelled job,
// the 210-minute ceiling the falsify night hit on 2026-11-12 — closed the drift
// issue with "Every operation upstream is triaged: implemented, or declined
// with a reason."
//
// That sentence would then be a verdict drawn from an absence of measurement,
// which is the failure this repository has paid for most. The backlog step now
// answers with three values and this is the third one.
func TestADriftReportThatCouldNotMeasureNeitherClosesNorComments(t *testing.T) {
	for _, verdict := range []string{"cancelled", "skipped", "error"} {
		code, out, writes := runDriftReport(t, verdict, "provider scaleway: nothing left to triage\n", ourIssueOpen, true)
		if code != 0 {
			t.Fatalf("exit %d on verdict %q: %s", code, verdict, out)
		}
		if writes != "" {
			t.Errorf("verdict %q wrote to the issues although nothing was measured:\n%s", verdict, writes)
		}
		if !strings.Contains(out, "did not conclude") {
			t.Errorf("verdict %q does not say the scan could not conclude: %s", verdict, out)
		}
	}
}

// An absent verdict fails loudly instead of quietly.
//
// The distinction matters: "cancelled" is a verdict the scan reported, and the
// case above is right to be quiet about it. An EMPTY argument means the step
// that was supposed to answer never ran at all, so there is no verdict to be
// quiet about. The script exits non-zero, the report job fails, and the night
// job (#705) reports a scan that could not conclude — which is the mechanism
// that exists for this, instead of a silence this script would be inventing.
func TestADriftReportWithNoVerdictAtAllFailsLoudly(t *testing.T) {
	code, _, writes := runDriftReport(t, "", "provider scaleway: nothing left to triage\n", ourIssueOpen, true)
	if code == 0 {
		t.Error("a missing verdict exited zero: the step that answers it never ran, and nothing says so")
	}
	if writes != "" {
		t.Errorf("a missing verdict still wrote to the issues:\n%s", writes)
	}
}

// The report says the backlog may be old, because it is drawn from what is
// undecided and not from what arrived.
//
// Without this the comment reads "the nightly scan found operations upstream",
// which sends its reader looking for last night's upstream change. The two
// baremetal commitment operations arrived fourteen nights earlier.
func TestADriftReportDoesNotClaimTheBacklogIsNew(t *testing.T) {
	_, _, writes := runDriftReport(t, "failure", "provider scaleway: 2 operations to triage\n", ourIssueOpen, true)
	if !strings.Contains(writes, "may have been here a while") {
		t.Errorf("the comment presents a standing backlog as last night's arrival:\n%s", writes)
	}
}

// Without --apply it writes nothing, so the decision can be read against any
// past night.
func TestTheDriftReportWritesNothingWithoutApply(t *testing.T) {
	_, out, writes := runDriftReport(t, "failure", "provider scaleway: 1 operation to triage\n", ourIssueOpen, false)
	if writes != "" {
		t.Errorf("it wrote without --apply:\n%s", writes)
	}
	if !strings.Contains(out, "the body it would post") {
		t.Errorf("a dry run does not show what it would post: %s", out)
	}
}
