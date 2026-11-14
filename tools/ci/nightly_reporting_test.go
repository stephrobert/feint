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

// TestANightReportCallerWaitsForEveryJobOrNamesTheOnesItJudges is the static
// half of #830: a job that calls night-report.yml either waits for every other
// job of its workflow, or the script is told which jobs to judge — and which
// it never judges. The verdict reads the run it is part of, and a job running
// beside the report enters the verdict or not depending on a few seconds —
// the race the issue measures over fourteen nights. Five of the six callers
// satisfy the first half today; the sixth, runtime-proof.yml, passes
// judged-jobs and ignored-jobs. A judged list that is only a key —
// `judged-jobs: ”` — is not a list: the script reads it as "judge
// everything", so the guard requires a non-empty block, not the key.
func TestANightReportCallerWaitsForEveryJobOrNamesTheOnesItJudges(t *testing.T) {
	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the workflow directory: %v", err)
	}

	needsList := regexp.MustCompile(`(?m)^\s+needs:\s*\[([^\]]+)\]`)
	needsOne := regexp.MustCompile(`(?m)^\s+needs:\s*['"]?([A-Za-z0-9_-]+)['"]?\s*$`)
	needsBlock := regexp.MustCompile(`(?m)^[ \t]*needs:[ \t]*$\n((?:^[ \t]*-[ \t].*$\n?)+)`)
	jobKey := regexp.MustCompile(`(?m)^  ([A-Za-z0-9_-]+):$`)

	callers := 0

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
		jobsAt := strings.Index(workflow, "\njobs:\n")
		if jobsAt < 0 {
			continue
		}
		var ids []string
		for _, m := range jobKey.FindAllStringSubmatch(workflow[jobsAt:], -1) {
			ids = append(ids, m[1])
		}
		for _, id := range ids {
			section := jobSection(workflow, id)
			if !strings.Contains(section, "uses: ./.github/workflows/night-report.yml") {
				continue
			}
			callers++
			// The jobs this reporter waits for, in every spelling of needs:.
			waited := waitedJobs(section, needsList, needsOne, needsBlock)
			var beside []string
			for _, other := range ids {
				if other != id && !waited[other] {
					beside = append(beside, other)
				}
			}
			judged := nonEmptyList(section, "judged-jobs")
			ignored := nonEmptyList(section, "ignored-jobs")
			switch {
			case len(beside) > 0 && !judged:
				t.Errorf("%s: the %s job runs beside %s without waiting for them and passes no non-empty judged-jobs list: "+
					"a job finishing beside the report enters the verdict or not depending on a few seconds (#830)",
					name, id, strings.Join(beside, ", "))
			case judged && !ignored:
				t.Errorf("%s: the %s job passes a judged list but no non-empty ignored-jobs list: "+
					"the script refuses a night whose jobs are neither judged nor ignored, so the missing list is a refused night waiting for the next schedule (#830)",
					name, id)
			}
		}
	}

	// The accepting half: without it, a pattern that matched no caller at all
	// would pass every case above and this test would assert nothing.
	if callers < 6 {
		t.Fatalf("only %d night-report caller(s) found, want at least 6: the caller pattern has "+
			"stopped matching and this test is measuring an empty set", callers)
	}
}

// waitedJobs is the set of job ids a reporting job waits for, in every
// spelling of needs: the flow list, the single name, and the block sequence,
// each with or without quotes. A spelling the parser misses reads as "beside"
// and fails conservatively, which is safe but surprising — hence the spellings
// are held here, with a test, rather than in the guard test's head.
func waitedJobs(section string, needsList, needsOne, needsBlock *regexp.Regexp) map[string]bool {
	waited := map[string]bool{}
	if m := needsList.FindStringSubmatch(section); m != nil {
		for _, job := range strings.Split(m[1], ",") {
			waited[unquote(strings.TrimSpace(job))] = true
		}
		return waited
	}
	if m := needsBlock.FindStringSubmatch(section); m != nil {
		for _, line := range strings.Split(strings.TrimRight(m[1], "\n"), "\n") {
			item := strings.TrimSpace(line)
			item = strings.TrimSpace(strings.TrimPrefix(item, "-"))
			waited[unquote(item)] = true
		}
		return waited
	}
	if m := needsOne.FindStringSubmatch(section); m != nil {
		waited[unquote(m[1])] = true
	}
	return waited
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// nonEmptyList reports whether a `key:` entry in a job section carries at
// least one value. A bare key — `judged-jobs: ”` — passes a Contains check
// while the script reads it as "judge everything", which is the race of #830
// back with a green check: the guard must require a non-empty block, not the
// key. Both spellings count: a literal block with a non-blank entry, or a
// non-empty single-line scalar.
func nonEmptyList(section, key string) bool {
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key+":") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, key+":"))
		switch rest {
		case "|", "|-", "|+":
			for _, next := range lines[i+1:] {
				if strings.TrimSpace(next) == "" {
					continue
				}
				nextIndent := len(next) - len(strings.TrimLeft(next, " "))
				if nextIndent <= indent {
					break
				}
				if t := strings.TrimSpace(next); t != "" && !strings.HasPrefix(t, "#") {
					return true
				}
			}
			return false
		case "", "''", `""`:
			return false
		default:
			return true
		}
	}
	return false
}

func TestNonEmptyListRequiresAValue(t *testing.T) {
	cases := []struct {
		name, section string
		want          bool
	}{
		{"block with names", "      judged-jobs: |\n        incus\n        incus-ovn\n", true},
		{"block with a trailing blank", "      judged-jobs: |\n        incus\n\n", true},
		{"empty quoted scalar", "      judged-jobs: ''\n", false},
		{"empty double-quoted scalar", "      judged-jobs: \"\"\n", false},
		{"null, no value", "      judged-jobs:\n", false},
		{"block with blanks only", "      judged-jobs: |\n\n\n", false},
		{"block with a comment only", "      judged-jobs: |\n        # nothing here\n", false},
		{"single inline name", "      judged-jobs: incus\n", true},
		{"quoted inline name", "      judged-jobs: 'incus'\n", true},
		{"key absent", "      uses: ./.github/workflows/night-report.yml\n", false},
		{"comment mentioning the key", "      # judged-jobs: incus\n", false},
	}
	for _, tc := range cases {
		if got := nonEmptyList(tc.section, "judged-jobs"); got != tc.want {
			t.Errorf("%s: nonEmptyList = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWaitedJobsCoversEveryNeedsSpelling(t *testing.T) {
	needsList := regexp.MustCompile(`(?m)^\s+needs:\s*\[([^\]]+)\]`)
	needsOne := regexp.MustCompile(`(?m)^\s+needs:\s*['"]?([A-Za-z0-9_-]+)['"]?\s*$`)
	needsBlock := regexp.MustCompile(`(?m)^[ \t]*needs:[ \t]*$\n((?:^[ \t]*-[ \t].*$\n?)+)`)
	cases := []struct {
		name, section string
		want          []string
	}{
		{"flow list", "    needs: [runtime, stacks]\n", []string{"runtime", "stacks"}},
		{"flow list quoted", "    needs: [\"runtime\", 'stacks']\n", []string{"runtime", "stacks"}},
		{"single name", "    needs: replay\n", []string{"replay"}},
		{"single name quoted", "    needs: 'replay'\n", []string{"replay"}},
		{"block sequence", "    needs:\n      - runtime\n      - stacks\n", []string{"runtime", "stacks"}},
		{"block sequence quoted", "    needs:\n      - 'runtime'\n      - \"stacks\"\n", []string{"runtime", "stacks"}},
		{"no needs", "    runs-on: ubuntu-24.04\n", nil},
	}
	for _, tc := range cases {
		got := waitedJobs(tc.section, needsList, needsOne, needsBlock)
		if len(got) != len(tc.want) {
			t.Errorf("%s: waited %v, want %v", tc.name, got, tc.want)
			continue
		}
		for _, w := range tc.want {
			if !got[w] {
				t.Errorf("%s: waited %v, want %v", tc.name, got, tc.want)
			}
		}
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
