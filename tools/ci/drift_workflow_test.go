package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The drift scan runs every night and reports through the night-report
// mechanism (#705), and the two outcomes it has to keep apart are held here
// rather than asserted in the workflow's comments.
//
// What is held, read off the workflow as text since a workflow only runs on
// GitHub's runners:
//
//   - the schedule is nightly, not Monday: the automation the whole project
//     hangs on had a blind window of up to seven days;
//   - a job adopts .github/workflows/night-report.yml after every other job,
//     on the schedule alone, under always(): a night where the scan could not
//     conclude — a clone that would not fetch, a build that broke, a push that
//     was refused — opens one issue, updated, closed by the first green night,
//     instead of leaving a job log nobody opens;
//   - the pull request stays conditional on a real diff: a nightly job that
//     opened a pull request every night would teach everyone to close them
//     unread, which is the failure mode the mechanism exists to avoid. The
//     surface moving is the mechanism working, not a red night, and
//     TestADriftNightWhereTheSurfaceMovedIsNotARedNight holds that half on
//     the script.
func TestTheDriftWorkflowReportsEveryNight(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "drift.yml"))
	if err != nil {
		t.Fatalf("read drift.yml: %v", err)
	}
	workflow := string(body)

	nightly := regexp.MustCompile(`(?m)^\s+- cron: '\d+ \d+ \* \* \*'`)
	if !nightly.MatchString(workflow) {
		t.Error("the drift scan is not on a nightly cron: the blind window between an upstream " +
			"change and anyone learning of it is the week #705 measured")
	}

	night := jobSection(workflow, "night")
	if night == "" {
		t.Fatal("drift.yml has no `night` job: a night where the scan could not conclude leaves a job log and nothing else")
	}
	if !strings.Contains(night, "uses: ./.github/workflows/night-report.yml") {
		t.Error("the night job does not adopt night-report.yml, the mechanism runtime-proof.yml already uses")
	}
	if !strings.Contains(night, "\n    if: always() && github.event_name == 'schedule'\n") {
		t.Error("the night job is not `always()` on the schedule alone: a failed scan would skip the very " +
			"job that reports it, or a pull request run would shout about a run somebody is reading")
	}
	needs := regexp.MustCompile(`(?m)^\s+needs:\s*\[([^\]]+)\]`).FindStringSubmatch(night)
	if needs == nil {
		t.Fatal("the night job needs nothing, so it judges a run whose jobs have not finished")
	}
	for _, job := range []string{"drift", "propose", "report"} {
		if !strings.Contains(needs[1], job) {
			t.Errorf("the night job does not wait for `%s`: a failure there would be judged green", job)
		}
	}

	propose := jobSection(workflow, "propose")
	if !strings.Contains(propose, "\n    if: github.event_name == 'schedule' && needs.drift.outputs.changed == 'true'\n") {
		t.Error("the pull request is no longer conditional on a real diff: a pull request every night " +
			"teaches everyone to close them unread")
	}
}

// jobSection is the text of one job of a workflow, from its key to the next
// job's key or the end of the file.
func jobSection(workflow, job string) string {
	start := strings.Index(workflow, "\n  "+job+":\n")
	if start < 0 {
		return ""
	}
	rest := workflow[start+1:]
	next := regexp.MustCompile(`(?m)^  [a-z][a-z-]*:\n`).FindStringIndex(rest[len(job)+4:])
	if next == nil {
		return rest
	}
	return rest[:len(job)+4+next[0]]
}
