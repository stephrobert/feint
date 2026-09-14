package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// silence is how a scheduled workflow may report nowhere, and why. Two answers,
// held apart the way coverage/contract-only.json already holds them: "declined"
// is a decision somebody took, "backlog" is a question nobody has answered yet.
// Folding them together is how a thing nobody decided starts reading as a thing
// somebody chose.
//
// A workflow absent from this map and adopting no reporting job is one that can
// fail every night and tell nobody, which is the failure the mechanism exists
// to prevent.
type silence struct {
	status string // "declined" or "backlog"
	reason string
}

var silentByDecision = map[string]silence{
	"codeql.yml": {"declined",
		"uploads SARIF to the repository's Security tab, which is its report: a finding appears " +
			"there whether or not the run was read"},
	"osv-scanner.yml": {"declined",
		"uploads SARIF to the Security tab like codeql.yml, and a vulnerable dependency is a " +
			"finding there rather than a red night"},
	"scorecard.yml": {"declined",
		"uploads SARIF to the Security tab and publishes a score whose history is the report; a " +
			"night it could not run leaves the previous score standing, visibly stale"},
	"conformance.yml": {"backlog",
		"runs on pull requests as well as nightly, so its red legs are read on the pull request " +
			"they block; night-report.yml's own comment lists it as a caller whose turn has not come"},
	"corpus-cloud.yml": {"backlog",
		"needs a credential and creates real objects on a real account, so who should be told when " +
			"it fails is a question about the account holder rather than about this repository"},
	"plumber.yml": {"backlog",
		"runs `plumber analyze --min-score B --score-push` and pushes a score; whether a night it " +
			"could not run deserves an issue here has not been decided, and saying so beats inventing " +
			"a reason (noticed 2026-09-14 while fixing #737)"},
	"tap.yml": {"backlog",
		"its red is clearable only by whoever can push to the tap, which is why it is off the " +
			"pull-request path; whether that also means no issue here has not been decided " +
			"(noticed 2026-09-14 while fixing #737)"},
}

// TestEveryScheduledWorkflowReportsOrSaysWhyNot is the control that was missing
// on 2026-09-14.
//
// falsify.yml had `timeout-minutes: 30` from when the suite was smaller, and the
// suite grew to 1271 mutations across 199 specs. It was killed by its own
// timeout for twenty-four consecutive nights, the last green one being
// 18 August 2026, and **nobody was told**: the workflow adopted no reporting
// job, and `cancelled` is not `failure`, so even a mechanism watching for red
// would have seen nothing.
//
// drift.yml has TestTheDriftWorkflowReportsEveryNight to hold its own half. This
// asks the same question of every scheduled workflow at once, because a control
// written per caller is a control one caller is missing — which is exactly what
// happened here.
func TestEveryScheduledWorkflowReportsOrSaysWhyNot(t *testing.T) {
	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the workflow directory: %v", err)
	}

	nightly := regexp.MustCompile(`(?m)^\s+- cron: `)
	scheduled := 0

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") || name == "night-report.yml" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		workflow := string(body)
		if !nightly.MatchString(workflow) {
			continue
		}
		scheduled++

		adopts := strings.Contains(workflow, "night-report.yml")
		excuse, excused := silentByDecision[name]

		switch {
		case adopts && excused:
			t.Errorf("%s adopts night-report.yml and is also listed as silent: remove it from "+
				"silentByDecision, or the list stops describing the tree", name)
		case !adopts && !excused:
			t.Errorf("%s runs on a schedule, adopts no reporting job, and says nowhere why. "+
				"A night it fails is a job log nobody opens: falsify.yml was cancelled by its own "+
				"timeout for twenty-four nights that way. Adopt night-report.yml with a six-line "+
				"job, or add it to silentByDecision with a status and a reason", name)
		case excused && excuse.status != "declined" && excuse.status != "backlog":
			t.Errorf("%s carries status %q: only \"declined\" (somebody decided) and \"backlog\" "+
				"(nobody has yet) say something a reader can act on", name, excuse.status)
		case excused && len(excuse.reason) < 60:
			t.Errorf("%s is excused with a reason too short to weigh: %q", name, excuse.reason)
		}
	}

	// The accepting half. Without it, a regexp that matched no workflow at all
	// would pass every case above and this file would assert nothing.
	if scheduled < 3 {
		t.Fatalf("only %d scheduled workflow(s) found, want at least 3: the cron pattern has "+
			"stopped matching and this test is measuring an empty set", scheduled)
	}
}

// TestTheFalsifySuiteIsGivenTimeToFinish holds the other half of #737.
//
// The budget is not a preference: 1271 mutations, each a tree copy plus a build
// plus one test run, clocked locally at 29 seconds per spec on a warm cache,
// which projects to about 97 minutes. Thirty minutes could not finish it, and a
// job killed mid-replay reports `cancelled`, which reads as nobody's fault.
func TestTheFalsifySuiteIsGivenTimeToFinish(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "falsify.yml"))
	if err != nil {
		t.Fatalf("read falsify.yml: %v", err)
	}
	m := regexp.MustCompile(`(?m)^\s+timeout-minutes:\s*(\d+)`).FindStringSubmatch(string(body))
	if m == nil {
		t.Fatal("falsify.yml declares no timeout-minutes: an unbounded replay is a runner held all night")
	}
	got := m[1]
	if len(got) < 3 || got < "100" {
		t.Errorf("falsify.yml allows %s minutes for 199 specs clocked at ~29s each. "+
			"Thirty minutes is what cancelled it for twenty-four nights (#737)", got)
	}
}
