package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The shape of the nightly govulncheck workflow (#865).
//
// Four outcomes, kept apart on purpose: an advisory touching the pin opens a
// pull request and an issue; a scan that could not conclude opens one
// `scheduled-red` issue through night-report.yml; a clean night says so in
// its log; the question asked by hand stays `mise run vulncheck`. This test
// holds the shape: nightly, reporting, conditional on an advisory, and never
// on a pull request — because a gate whose verdict moves with vuln.go.dev is
// a gate that fails on the network rather than on the code.
func TestTheVulncheckWorkflowReportsEveryNight(t *testing.T) {
	workflow := readWorkflow(t, "vulncheck.yml")

	nightly := regexp.MustCompile(`(?m)^\s+- cron: '\d+ \d+ \* \* \*'`)
	if !nightly.MatchString(workflow) {
		t.Error("vulncheck.yml has no nightly cron schedule")
	}
	if strings.Contains(workflow, "pull_request:") {
		t.Error("vulncheck.yml must never run on a pull request: the scan's verdict moves with vuln.go.dev, not with the code")
	}

	// The scan job is what decides whether the night is red, and its
	// conclusion is written to the summary on every night: a clean night
	// proves itself by saying so, with the database timestamp the scan read,
	// rather than by leaving no trace.
	scan := jobSection(workflow, "scan")
	if scan == "" {
		t.Fatal("vulncheck.yml has no scan job")
	}
	if !strings.Contains(scan, "tools/ci/vulncheck-scan.sh") {
		t.Error("the scan job does not run vulncheck-scan.sh")
	}
	if !strings.Contains(scan, "GITHUB_STEP_SUMMARY") {
		t.Error("the scan job does not write its conclusion to the step summary: a clean night would leave no trace")
	}

	// The propose job opens the pull request, and only on a schedule and
	// only when the scan found an advisory: a pull request on a clean night
	// is a pull request that changes nothing.
	propose := jobSection(workflow, "propose")
	if propose == "" {
		t.Fatal("vulncheck.yml has no propose job")
	}
	if !strings.Contains(propose, "needs.scan.outputs.verdict == 'advisory'") {
		t.Error("the propose job is not conditional on an advisory verdict")
	}
	if !strings.Contains(propose, "github.event_name == 'schedule'") {
		t.Error("the propose job is not schedule-only")
	}
	if !strings.Contains(propose, "tools/ci/vulncheck-bump.sh") {
		t.Error("the propose job does not move the pins with vulncheck-bump.sh")
	}

	// The report job is what carries the signal when the pull request could
	// not be opened, and what closes the issue when the pin has moved past
	// the advisory. A condition that fired only on a finding could never
	// close what it opened.
	report := jobSection(workflow, "report")
	if report == "" {
		t.Fatal("vulncheck.yml has no report job")
	}
	if !strings.Contains(report, "tools/ci/vulncheck-report.sh") {
		t.Error("the report job does not run vulncheck-report.sh")
	}
	if !strings.Contains(report, "needs.scan.outputs.verdict == 'advisory'") ||
		!strings.Contains(report, "needs.scan.outputs.verdict == 'clean'") {
		t.Error("the report job does not run on both an advisory night and a clean night: it could never close what it opened")
	}

	// A red night opens an issue, a green one closes it (#705): the
	// mechanism runtime-proof.yml adopted first, as a reusable workflow,
	// because a block copied into each scheduled caller is a control one
	// caller forgets.
	night := jobSection(workflow, "night")
	if night == "" {
		t.Fatal("vulncheck.yml has no night job")
	}
	if !strings.Contains(night, "uses: ./.github/workflows/night-report.yml") {
		t.Error("the night job does not adopt night-report.yml")
	}
	if !strings.Contains(night, "\n    if: always() && github.event_name == 'schedule'\n") {
		t.Error("the night job does not run always() on a schedule")
	}
	for _, job := range []string{"scan", "propose", "report"} {
		if !strings.Contains(night, job) {
			t.Errorf("the night job does not wait on %q: a scan that could not conclude would leave no issue", job)
		}
	}
}

// The pull-request gate no longer asks a question whose verdict moves with
// vuln.go.dev, and the reason stays written where the question used to be
// asked: a reader of go.yml must find the schedule, and the prohibition on
// the prepush hook and the pre-commit hook, without opening the issue.
func TestTheGoWorkflowNoLongerRunsGovulncheck(t *testing.T) {
	// Read raw, not through readWorkflow: that helper strips whole-line
	// comments, and the evidence here IS the comment — the reason must stay
	// written where the question used to be asked. The step check below is
	// on the non-comment lines, so a commented-out step still fails it.
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "go.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)

	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "govulncheck") {
			t.Errorf("go.yml still runs govulncheck outside a comment: %q", trimmed)
		}
	}
	if !strings.Contains(workflow, ".github/workflows/vulncheck.yml") {
		t.Error("go.yml does not say where the scan moved to: the reason must stay written")
	}
	if !strings.Contains(workflow, "mise run vulncheck") {
		t.Error("go.yml does not say how to ask the question by hand")
	}
	if !strings.Contains(workflow, "not in `mise run prepush`") {
		t.Error("go.yml no longer states that the scan stays out of prepush")
	}
}
