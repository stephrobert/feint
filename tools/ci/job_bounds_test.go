package ci

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Four rules every workflow of this repository is held to, each asked of every
// job of every file rather than of the one that was found wanting (#988):
//
//  1. a job bounds its own run with `timeout-minutes`;
//  2. no job runs on a moving image (`ubuntu-latest` and its siblings);
//  3. a workflow grants nothing at the top (`permissions: {}`), so each job
//     names the scopes it needs and a job added later starts with none;
//  4. no `${{ }}` expression is pasted into a `run:` script: values reach the
//     shell through `env:`, where they are data and not code.
//
// What each one looked like before, measured on 2026-11-16 against
// origin/main 78bd9d8: 23 jobs in 12 files carried no `timeout-minutes`, so each
// could hold a runner for GitHub's default of 360 minutes; adoption-metrics.yml
// ran on `ubuntu-latest` and granted `contents: read` at the top; three `run:`
// lines interpolated an expression (runtime-proof.yml `matrix.mode` and
// `steps.count.outputs.streak`, drift.yml `needs.drift.outputs.undecided`).
// The issue named two of the three; the scan below found the third.
//
// Each test comes in two halves: a table that asks one question per case, with
// an input that fails that question only, and the walk over the real workflows.
// tools/falsify/specs/every-job-is-bounded.json removes each question in turn
// and requires its case to go red.

// githubDefaultTimeout is the bound GitHub applies to a job that declares none,
// in minutes. A declared value above it bounds nothing more than silence did.
const githubDefaultTimeout = 360

// jobTimeoutLine matches a job-level `timeout-minutes`: four spaces, exactly,
// under a job declared at two. A step's own `timeout-minutes` sits deeper and
// bounds that step only, so it does not answer for the job.
var jobTimeoutLine = regexp.MustCompile(`^    timeout-minutes:(.*)$`)

// jobLevelUses matches a job that calls a reusable workflow instead of running
// steps. GitHub refuses `timeout-minutes` on such a job: the called workflow's
// own jobs carry it.
var jobLevelUses = regexp.MustCompile(`^    uses:\s*(\S+)`)

// jobTimeoutProblem answers why a job does not bound its own run, or "" when it
// does. Every shape it does not recognise is a refusal.
func jobTimeoutProblem(body string) string {
	var values []string
	for _, line := range strings.Split(body, "\n") {
		if m := jobLevelUses.FindStringSubmatch(line); m != nil {
			// A workflow of this repository is walked by the test in its own
			// right, so its jobs answer for themselves. One from elsewhere is
			// not, and exempting it would leave a job nobody bounds.
			if strings.HasPrefix(m[1], "./.github/workflows/") {
				return ""
			}
			return fmt.Sprintf("calls %s, a workflow outside this repository whose jobs this test cannot read", m[1])
		}
		if m := jobTimeoutLine.FindStringSubmatch(line); m != nil {
			values = append(values, m[1])
		}
	}
	if len(values) == 0 {
		return "no job-level timeout-minutes: the job can hold a runner for GitHub's default of 360 minutes"
	}
	if len(values) > 1 {
		return fmt.Sprintf("timeout-minutes declared %d times at job level, a shape this test does not read", len(values))
	}
	// The last value, and only when there is one: each check above is a
	// condition, and removing one must leave a refusal for a wrong reason, never
	// an index out of range that fails every case alike.
	value := ""
	if len(values) > 0 {
		value = stripInlineComment(values[len(values)-1])
	}
	minutes, err := strconv.Atoi(value)
	if err != nil || minutes < 1 {
		return fmt.Sprintf("timeout-minutes is %q, which is not a positive whole number of minutes", value)
	}
	if minutes > githubDefaultTimeout {
		return fmt.Sprintf("timeout-minutes is %d, above GitHub's default of %d, so it bounds nothing", minutes, githubDefaultTimeout)
	}
	return ""
}

// stripInlineComment drops a trailing ` # comment` and the surrounding blanks.
func stripInlineComment(value string) string {
	if before, _, found := strings.Cut(value, " #"); found {
		value = before
	}
	return strings.TrimSpace(value)
}

func TestJobTimeoutProblemAsksEachQuestion(t *testing.T) {
	const head = "    name: x\n    runs-on: ubuntu-24.04\n"
	const steps = "    steps:\n      - uses: actions/checkout@0000000000000000000000000000000000000000 # v7\n"
	cases := []struct {
		name, body, want string // want "" means accepted
	}{
		// The accepting half: a control that refuses everything would pass every
		// refusing case below and stop every workflow of the repository.
		{"a measured bound with its comment", head + "    timeout-minutes: 15 # measured\n" + steps, ""},
		{"the smallest bound", head + "    timeout-minutes: 1\n" + steps, ""},
		{"GitHub's default, written down", head + "    timeout-minutes: 360\n" + steps, ""},
		{"a call to a workflow of this repository", "    uses: ./.github/workflows/night-report.yml\n", ""},

		{"no bound at all", head + steps, "no job-level timeout-minutes"},
		{"a bound on a step only", head + "    steps:\n      - run: make\n        timeout-minutes: 10\n",
			"no job-level timeout-minutes"},
		{"steps that use actions, and no bound", head + steps + "      - uses: ./.github/actions/local\n",
			"no job-level timeout-minutes"},
		{"declared twice", head + "    timeout-minutes: 10\n    timeout-minutes: 20\n" + steps, "declared 2 times"},
		{"zero", head + "    timeout-minutes: 0\n" + steps, "not a positive whole number"},
		{"an expression", head + "    timeout-minutes: ${{ inputs.minutes }}\n" + steps, "not a positive whole number"},
		{"empty", head + "    timeout-minutes:\n" + steps, "not a positive whole number"},
		{"above the default", head + "    timeout-minutes: 361\n" + steps, "above GitHub's default"},
		{"a call to a workflow elsewhere",
			"    uses: someone/else/.github/workflows/x.yml@0000000000000000000000000000000000000000\n",
			"outside this repository"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := jobTimeoutProblem(c.body)
			switch {
			case c.want == "" && got != "":
				t.Errorf("refused a job that bounds its run: %s", got)
			case c.want != "" && !strings.Contains(got, c.want):
				t.Errorf("answered %q, want a refusal naming %q", got, c.want)
			}
		})
	}
}

func TestEveryJobBoundsItsOwnRun(t *testing.T) {
	checked := 0
	for _, name := range workflowFiles(t) {
		for job, body := range jobsOf(readWorkflow(t, name)) {
			checked++
			if problem := jobTimeoutProblem(body); problem != "" {
				t.Errorf("%s: job %q: %s. Measure the job's recent runs and bound it at about "+
					"three times the longest, with the measurement in a comment", name, job, problem)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no job found in any workflow: this test would pass by having nothing to look at")
	}
}

// movingImage matches a runner label that follows GitHub's own schedule rather
// than this repository's.
var movingImage = regexp.MustCompile(`\b(ubuntu|macos|windows)-latest\b`)

// runnerLabelProblems answers every line that names a moving image, anywhere in
// the workflow: `runs-on:` and a matrix both reach the runner. YAML comments
// are not configuration and are skipped.
func runnerLabelProblems(workflow string) []string {
	var found []string
	for i, line := range strings.Split(workflow, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if label := movingImage.FindString(stripInlineComment(line)); label != "" {
			found = append(found, fmt.Sprintf("line %d names %s", i+1, label))
		}
	}
	return found
}

func TestRunnerLabelProblemAsksEachQuestion(t *testing.T) {
	cases := []struct {
		name, workflow string
		refused        bool
	}{
		{"a pinned image", "    runs-on: ubuntu-24.04\n", false},
		// At column 0 and with no ` #` inside: a comment the inline-comment cut
		// cannot remove, so only the whole-line rule accepts it.
		{"a moving image named in a whole-line comment", "# ubuntu-latest moves under the job\n    runs-on: ubuntu-24.04\n", false},
		{"a moving image after a pinned one, in an inline comment", "    runs-on: macos-15 # was macos-latest\n", false},

		{"runs-on a moving image", "    runs-on: ubuntu-latest\n", true},
		{"a moving image reached through a matrix",
			"    runs-on: ${{ matrix.runner }}\n    strategy:\n      matrix:\n        runner:\n          - macos-latest\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runnerLabelProblems(c.workflow)
			if c.refused != (len(got) > 0) {
				t.Errorf("refused=%v, want %v (%v)", len(got) > 0, c.refused, got)
			}
		})
	}
}

func TestNoJobRunsOnAMovingImage(t *testing.T) {
	for _, name := range workflowFiles(t) {
		// Raw, so the line a refusal names is the line of the file.
		for _, problem := range runnerLabelProblems(rawWorkflow(t, name)) {
			t.Errorf("%s: %s, which GitHub moves to a new release on its own schedule: pin the "+
				"image the jobs were measured on", name, problem)
		}
	}
}

// topPermissionsProblem answers why a workflow grants something at the top, or
// "" when it grants nothing there.
func topPermissionsProblem(workflow string) string {
	var values []string
	for _, line := range strings.Split(workflow, "\n") {
		if rest, found := strings.CutPrefix(line, "permissions:"); found {
			values = append(values, stripInlineComment(rest))
		}
	}
	if len(values) == 0 {
		return "no top-level permissions: a job that names none inherits the repository default"
	}
	if len(values) > 1 {
		return fmt.Sprintf("top-level permissions declared %d times, a shape this test does not read", len(values))
	}
	value := ""
	if len(values) > 0 {
		value = values[len(values)-1]
	}
	if value == "{}" {
		return ""
	}
	if value == "" {
		return "grants a block of scopes at the top, which every job inherits unless it names its own"
	}
	return fmt.Sprintf("grants %q at the top, which every job inherits unless it names its own", value)
}

func TestTopPermissionsProblemAsksEachQuestion(t *testing.T) {
	const jobs = "jobs:\n  x:\n    permissions:\n      contents: write\n"
	cases := []struct {
		name, workflow, want string // want "" means accepted
	}{
		{"nothing at the top, a scope on the job", "on: push\npermissions: {}\n" + jobs, ""},
		{"nothing at the top, with a comment", "on: push\npermissions: {} # per job\n" + jobs, ""},

		{"no top-level permissions", "on: push\n" + jobs, "no top-level permissions"},
		{"a block at the top", "on: push\npermissions:\n  contents: read\n" + jobs, "a block of scopes"},
		{"a named grant at the top", "on: push\npermissions: read-all\n" + jobs, `grants "read-all"`},
		{"declared twice", "on: push\npermissions: {}\npermissions: {}\n" + jobs, "declared 2 times"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := topPermissionsProblem(c.workflow)
			switch {
			case c.want == "" && got != "":
				t.Errorf("refused a workflow that grants nothing at the top: %s", got)
			case c.want != "" && !strings.Contains(got, c.want):
				t.Errorf("answered %q, want a refusal naming %q", got, c.want)
			}
		})
	}
}

func TestEveryWorkflowGrantsNothingAtTheTop(t *testing.T) {
	for _, name := range workflowFiles(t) {
		if problem := topPermissionsProblem(readWorkflow(t, name)); problem != "" {
			t.Errorf("%s: %s. Write `permissions: {}` at the top and the scopes on each job", name, problem)
		}
	}
}

// runKey matches a `run:` key, in a step's mapping or as the first key of a
// list item.
var runKey = regexp.MustCompile(`^(\s*)(- )?run:(.*)$`)

// runExpressions answers every line of a `run:` script that carries a `${{ }}`
// expression, and how many `run:` keys it read. GitHub substitutes the
// expression into the script text before the shell sees it, shell comments
// included, so every line of the script counts.
func runExpressions(workflow string) (found []string, runs int) {
	lines := strings.Split(workflow, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			continue // a YAML comment, outside any script
		}
		m := runKey.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		runs++
		keyIndent := len(m[1]) + len(m[2])
		value := strings.TrimSpace(m[3])
		if strings.Contains(value, "${{") {
			found = append(found, fmt.Sprintf("line %d: %s", i+1, strings.TrimSpace(lines[i])))
		}
		if !strings.HasPrefix(value, "|") && !strings.HasPrefix(value, ">") {
			continue
		}
		for i+1 < len(lines) && scriptContinues(lines[i+1], keyIndent) {
			i++
			if strings.Contains(lines[i], "${{") {
				found = append(found, fmt.Sprintf("line %d: %s", i+1, strings.TrimSpace(lines[i])))
			}
		}
	}
	return found, runs
}

// scriptContinues answers whether a line still belongs to a block scalar whose
// key sits at keyIndent: a blank line, or one indented deeper than the key.
func scriptContinues(line string, keyIndent int) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	return len(line)-len(strings.TrimLeft(line, " ")) > keyIndent
}

func TestRunExpressionsAsksEachQuestion(t *testing.T) {
	cases := []struct {
		name, workflow string
		lines          []int // the lines that must be reported, and no other
	}{
		{"a value through env", "      - name: x\n        env:\n          MODE: ${{ matrix.mode }}\n" +
			"        run: guard.sh \"$MODE\"\n", nil},
		{"a condition and an action input", "      - name: x\n        if: ${{ github.event_name == 'push' }}\n" +
			"        with:\n          ref: ${{ github.sha }}\n        run: make\n", nil},
		{"a script that ends where the next key starts", "      - name: x\n        run: |\n          make\n" +
			"        env:\n          A: ${{ inputs.a }}\n", nil},

		{"an inline script", "      - name: x\n        run: guard.sh \"${{ matrix.mode }}\"\n", []int{2}},
		{"a block script, past a blank line", "      - name: x\n        run: |\n          set -eu\n\n" +
			"          report.sh \"${{ steps.count.outputs.streak }}\"\n", []int{5}},
		{"a folded script", "      - name: x\n        run: >-\n          echo ${{ needs.a.outputs.b }}\n", []int{3}},
		{"a list item that starts with run", "      - run: echo ${{ github.event.pull_request.title }}\n", []int{1}},
		{"a shell comment in a script", "      - name: x\n        run: |\n          # ${{ inputs.a }}\n          make\n", []int{3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, runs := runExpressions(c.workflow)
			if runs == 0 {
				t.Fatal("read no run: key, so this case measures nothing")
			}
			var want []string
			for _, n := range c.lines {
				want = append(want, fmt.Sprintf("line %d:", n))
			}
			if len(got) != len(want) {
				t.Fatalf("reported %v, want exactly lines %v", got, c.lines)
			}
			for i := range want {
				if !strings.HasPrefix(got[i], want[i]) {
					t.Errorf("reported %q, want %q", got[i], want[i])
				}
			}
		})
	}
}

func TestNoExpressionIsPastedIntoAShell(t *testing.T) {
	runs := 0
	for _, name := range workflowFiles(t) {
		// Raw, not readWorkflow: that drops every whole-line comment, and a shell
		// comment inside a script is substituted like any other line of it.
		found, n := runExpressions(rawWorkflow(t, name))
		runs += n
		for _, line := range found {
			t.Errorf("%s: %s: the value becomes shell code before the shell sees it. Pass it "+
				"through the step's env: and read it as \"$NAME\"", name, line)
		}
	}
	if runs == 0 {
		t.Fatal("no run: key found in any workflow: this test would pass by having nothing to look at")
	}
}

// rawWorkflow answers a workflow as it is on disk, comments included, so a line
// number in a refusal is the line a reader opens.
func rawWorkflow(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
