package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestTheFalsifyHarnessHoldsItsOwnSelftest runs `falsify.py --selftest` and
// requires it green, which is what lets a falsify spec mutate the harness.
//
// The harness's guards are Python, and a falsify spec measures a mutation by
// running a Go test: before this test, no spec could aim at falsify.py at all,
// so the checks that refuse a dead fragment, a renamed test or a path spelled
// two ways (#950) were proven by the selftest alone and never replayed.
// tools/falsify/specs/the-falsify-harness-checks-its-own-claims.json mutates
// those checks one at a time and requires this test to go red each time.
//
// Three outcomes: the selftest passed and printed its closing sentence; it
// failed; or it could not be run, which is a failure here and never a skip —
// a skipped test is not a red one, and a falsification of a skipped test
// reads TEST STILL PASSED.
func TestTheFalsifyHarnessHoldsItsOwnSelftest(t *testing.T) {
	script := filepath.Join("..", "..", "tools", "falsify", "falsify.py")
	cmd := exec.Command("python3", "-I", script, "--selftest")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		t.Fatalf("falsify.py --selftest exited %d:\n%s", exit.ExitCode(), failingLines(string(out)))
	case err != nil:
		t.Fatalf("falsify.py --selftest could not be run, so nothing was measured: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "\n!! ") {
		t.Fatalf("falsify.py --selftest exited 0 and still printed a failure:\n%s", failingLines(string(out)))
	}
	if !strings.Contains(string(out), "the rule refuses all ") {
		t.Fatalf("falsify.py --selftest exited 0 without its closing sentence, so it may have run nothing:\n%s", out)
	}
}

func failingLines(out string) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "!! ") || strings.HasPrefix(line, "   ") {
			keep = append(keep, line)
		}
	}
	if len(keep) == 0 {
		return out
	}
	return strings.Join(keep, "\n")
}

// What every task of `mise run prepush` has to be run by on a pull request
// (#936): the exact line a workflow triggered by `pull_request` must carry,
// either as `run: <line>` or as a line of a `run: |` block.
//
// CONTRIBUTING said prepush "is what a pull request will refuse without ever
// depending on a runner's weather", and two of its tasks, falsify:lint and
// falsify:selftest, were run by no pull request at all: a hook only, which a
// fresh clone does not have and `--no-verify` skips. falsify:lint is what
// refuses a mutation naming a test that does not exist (#923), so the guard
// against an unmeasured guard was itself unmeasured on every pull request.
//
// The whole line, not a substring: `run: echo <cmd>` and `run: <cmd> || true`
// carry the command and cannot fail, and both are refused here.
var prepushOnPullRequest = map[string][]string{
	"check": {
		"run: go vet ./...",
		"run: go test -race ./...",
		`"$(go env GOPATH)/bin/golangci-lint" run ./...`,
	},
	"docs:check":       {"run: tools/docs/check.sh"},
	"shapes:check":     {"./feint shapes --check"},
	"corpus:check":     {"run: ./feint corpus --check"},
	"falsify:selftest": {"run: python3 tools/falsify/falsify.py --selftest"},
	"falsify:lint":     {"run: python3 tools/falsify/falsify.py --lint tools/falsify/specs"},
	"release:surface":  {"run: bash --noprofile --norc tools/release/surface.sh"},
	"testplan:check":   {"run: bash --noprofile --norc tools/testplan/check.sh"},
}

// The prepush tasks whose step cannot fail a pull request by itself, and which
// a later step fails by reading its outcome. Each entry lists the lines the
// first step must run; failedByALaterStep says what the later step must be.
//
// drift:check is here because its step, `Coverage report` in drift.yml, is
// continue-on-error: on the schedule, a drift is the signal to open a pull
// request and must not turn the night red. The step that fails a pull request
// is `Fail the pull request on undecided drift`, conditioned by
// `github.event_name == 'pull_request' && steps.report.outcome == 'failure'`.
// The first version of this test proved drift:check by the gate line alone,
// in the continue-on-error step, and neutralising the later step's `exit 1`
// left it green (measured on 2026-11-14). That condition is read here in its
// one spelling, prFailedLater, and in no other.
var prepushFailedByALaterStep = map[string][]string{
	"drift:check": {
		"tools/drift/gate.sh check /tmp/feint > /tmp/drift.txt || status=$?",
		"exit $status",
	},
}

// The prepush tasks no pull request runs, each with the reason. Empty since
// testplan:check joined the Go workflow; an entry here must also be named as
// the exception in both CONTRIBUTING pages, which the test checks.
var prepushLocalOnly = map[string]string{}

// TestEveryPrepushTaskRunsOnAPullRequest holds #936.
//
// What makes it red, each with its own case in the falsify spec named above:
//
//   - a prepush task in neither table: an unrecognised task refuses, it is not
//     assumed to be covered;
//   - a task whose line no `pull_request` workflow carries verbatim, including
//     the workflow losing its `pull_request` trigger and a step made unable to
//     fail with `|| true`;
//   - a table entry for a task prepush no longer runs, so the table cannot
//     outlive what it describes;
//   - a CONTRIBUTING page that does not name a prepush task, or does not name
//     a local-only task as the exception.
//
// What "runs on a pull request" means is carried()'s doc: a step that runs on
// every pull request and fails it when its line fails, which rules out an
// `if:`, continue-on-error, a skipped needed job, a filtered trigger, a line
// under `with:` and a line inside a heredoc.
// TestAConditionedJobDoesNotCountAsRunningOnAPullRequest holds each on planted
// workflows. The one exception is a step failed by a later step that reads its
// outcome (prepushFailedByALaterStep), read in one spelling only and held by
// TestAStepFailedByALaterStepIsReadOnlyInOneSpelling.
//
// What it does NOT hold, said rather than implied: a job that runs on pull
// requests and whose check is not required. release:surface and drift:check
// run in such jobs today ("The release note names what changed", "Compare with
// the upstream SDK"): they run, and a red there does not block a merge.
func TestEveryPrepushTaskRunsOnAPullRequest(t *testing.T) {
	root := filepath.Join("..", "..")
	tasks := prepushTasks(t, root)

	workflows := readWorkflows(t, root)
	for _, task := range tasks {
		lines, mapped := prepushOnPullRequest[task]
		later, failedLater := prepushFailedByALaterStep[task]
		_, local := prepushLocalOnly[task]
		declared := 0
		for _, in := range []bool{mapped, failedLater, local} {
			if in {
				declared++
			}
		}
		switch {
		case declared > 1:
			t.Errorf("%s is declared in more than one table; one question, one answer", task)
			continue
		case local:
			continue
		case failedLater:
			if !failedByALaterStep(workflows, later) {
				t.Errorf("prepush runs %s, and no job run on every pull request has a step running %q "+
					"followed by a step conditioned %q that runs `exit 1`: "+
					"a pull request does not refuse what this task refuses", task, later, prFailedLater("<id>"))
			}
			continue
		case !mapped:
			t.Errorf("prepush runs %s, and this test does not know which pull_request step runs it: "+
				"map it to the step, or declare it local only with the reason", task)
			continue
		}
		for _, want := range lines {
			if !carried(workflows, want) {
				t.Errorf("prepush runs %s, and no workflow triggered by pull_request carries the line %q: "+
					"a pull request does not refuse what this task refuses", task, want)
			}
		}
	}

	inPrepush := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		inPrepush[task] = true
	}
	for _, table := range []map[string]bool{keys(prepushOnPullRequest), keys(prepushFailedByALaterStep), keysOf(prepushLocalOnly)} {
		for task := range table {
			if !inPrepush[task] {
				t.Errorf("%s is described here and prepush no longer runs it: drop it from the table", task)
			}
		}
	}

	for _, page := range []string{"CONTRIBUTING.md", "CONTRIBUTING.fr.md"} {
		body, err := os.ReadFile(filepath.Join(root, page))
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		text := strings.Join(strings.Fields(string(body)), " ")
		for _, task := range tasks {
			if !strings.Contains(text, "`"+task+"`") {
				t.Errorf("%s does not name `%s`, which prepush runs: the list a contributor reads is not the list that runs", page, task)
			}
		}
		for task := range prepushLocalOnly {
			if !regexp.MustCompile("(?i)(except|sauf)[^.]*`" + regexp.QuoteMeta(task) + "`").MatchString(text) {
				t.Errorf("%s does not name `%s` as the exception no pull request runs", page, task)
			}
		}
	}
}

// prepushTasks reads the depends list of [tasks.prepush] off mise.toml.
// Reading nothing is a refusal: a run over no task would pass by construction.
func prepushTasks(t *testing.T, root string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	block := regexp.MustCompile(`(?m)^\[tasks\.prepush\]\n(?:[^\[].*\n)*?depends = \[([^\]]*)\]`).FindStringSubmatch(string(body))
	if block == nil {
		t.Fatal("mise.toml has no [tasks.prepush] depends list this test can read, so nothing was judged")
	}
	var tasks []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(block[1], -1) {
		tasks = append(tasks, m[1])
	}
	if len(tasks) == 0 {
		t.Fatal("[tasks.prepush] depends on nothing this test can read, so nothing was judged")
	}
	return tasks
}

// readWorkflows returns the text of every workflow of the repository. Which
// of them run on every pull request is carried()'s question, not this one's.
func readWorkflows(t *testing.T, root string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflow found (%v), so nothing was judged", err)
	}
	out := make(map[string]string)
	triggered := 0
	for _, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		out[filepath.Base(path)] = string(body)
		if prTriggered(string(body)) {
			triggered++
		}
	}
	if triggered == 0 {
		t.Fatal("no workflow is triggered by every pull_request, which this test cannot tell from a parse that failed")
	}
	return out
}

// prTrigger finds `pull_request:` as a key of the `on:` block, with nothing
// written after the colon. A commented line is not a trigger, and an inline
// value (`pull_request: {types: [opened]}`) is a shape this test does not read,
// so it refuses.
var prTrigger = regexp.MustCompile(`(?m)^on:\n(?:(?:  .*|\s*)\n)*?  pull_request:[ \t]*\n`)

// prTriggered says whether the workflow runs on EVERY pull request: the
// trigger is declared and carries no filter. `paths`, `paths-ignore`,
// `branches` and `types` each keep some pull requests out, and a pull request
// they keep out is refused by nothing, so any key under the trigger refuses.
// TestAConditionedJobDoesNotCountAsRunningOnAPullRequest has one case per
// filter.
func prTriggered(body string) bool {
	loc := prTrigger.FindStringIndex(body)
	if loc == nil {
		return false
	}
	for _, line := range strings.Split(body[loc[1]:], "\n") {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		return !strings.HasPrefix(line, "   ")
	}
	return true
}

// prStep is what this test reads of one step: enough to say whether its run
// lines execute, and can fail, on a pull request.
type prStep struct {
	id    string
	cond  string // the step's if:, verbatim, or "" when it has none
	soft  bool   // continue-on-error, set to anything but false
	shell bool   // a shell: of its own, which may drop -e
	// What a shell runs and can fail on: "run: x" for an inline run, then the
	// lines of a `run: |` block that prShellLines keeps.
	run []string
	// The trimmed lines of the run block, without the lines of a heredoc,
	// which are data; prShellLines decides which of them count.
	block []string
}

type prJob struct {
	cond   string
	soft   bool
	shell  bool // a shell: under defaults, of the job or of the workflow
	needs  []string
	unread bool // a needs: this parser cannot read, which refuses
	steps  []prStep
}

var (
	prJobHeader = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	// A key is read the way YAML reads it: `if`, `'if'`, `"if"` and `if :`
	// are one key. Read strictly, the last three were no key at all, so a
	// job or a step conditioned that way counted as unconditioned.
	prJobKey    = regexp.MustCompile(`^    ["']?([A-Za-z0-9_-]+)["']?\s*:\s*(.*)$`)
	prStepKey   = regexp.MustCompile(`^        ["']?([A-Za-z0-9_-]+)["']?\s*:\s*(.*)$`)
	prNeedsItem = regexp.MustCompile(`^      - ([A-Za-z0-9_-]+)\s*$`)
	prNeedsName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	prHeredoc   = regexp.MustCompile(`(?:^|[^<])<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
	prTopKey    = regexp.MustCompile(`^([A-Za-z0-9_-]+):`)
	// A line after which no line of the block can fail the step: errexit
	// turned off, or the shell leaving with 0. `exit $status` and `exit 1`
	// are not in it; a bare `exit` hands back the last status, which after an
	// `echo` is 0, so it is.
	prSetPlusE = regexp.MustCompile(`(?:^|[;&|({]\s*)set\s+(?:-[A-Za-z]+\s+)*(?:\+[A-Za-z]*e|\+o\s+errexit)`)
	prExitZero = regexp.MustCompile(`(?:^|[;&|({]\s*|\b(?:then|else|do)\s+)exit(?:\s+0)?\s*(?:$|[;&|)}#])`)
)

// prShellLines keeps the lines of a run block that run as a command of their
// own and can fail the step, read without interpreting the shell:
//
//   - nothing after a `set +e`, an `exit 0` or a bare `exit`;
//   - not the line a `\` continues into, which is the tail of the command
//     above it (`echo \` then the line: the line is echoed, never run);
//   - not a line whose next line starts with `||`, which may excuse it.
func prShellLines(block []string) []string {
	var out []string
	for i, line := range block {
		if prSetPlusE.MatchString(line) || prExitZero.MatchString(line) {
			break
		}
		continued := i > 0 && strings.HasSuffix(block[i-1], "\\")
		excused := i+1 < len(block) && strings.HasPrefix(block[i+1], "||")
		if !continued && !excused {
			out = append(out, line)
		}
	}
	return out
}

// prParse reads the jobs of a workflow in the layout this repository writes:
// jobs at two spaces, their keys at four, steps at six, step keys at eight,
// block lines deeper. A line in any other layout is not read, so it does not
// count, which refuses rather than passes.
func prParse(body string) map[string]*prJob {
	jobs := make(map[string]*prJob)
	var job *prJob
	var step *prStep
	jobKey, topKey, inJobs, inBlock, heredoc := "", "", false, false, ""
	workflowShell := false
	closeStep := func() {
		if step != nil && job != nil {
			step.run = append(step.run, prShellLines(step.block)...)
			job.steps = append(job.steps, *step)
		}
		step, inBlock, heredoc = nil, false, ""
	}
	for _, line := range strings.Split(body, "\n") {
		text := strings.TrimSpace(line)
		if m := prTopKey.FindStringSubmatch(line); m != nil {
			topKey = m[1]
		}
		if topKey == "defaults" && strings.HasPrefix(text, "shell:") {
			workflowShell = true
		}
		switch {
		case line == "jobs:":
			inJobs = true
			continue
		case !inJobs || text == "":
			continue
		case strings.HasPrefix(text, "#") && !strings.HasPrefix(line, "          "):
			continue // a YAML comment; deeper, it is a line of a block
		case !strings.HasPrefix(line, " "):
			closeStep() // a top-level key ends jobs:
			inJobs, job = false, nil
			continue
		}
		if m := prJobHeader.FindStringSubmatch(line); m != nil {
			closeStep()
			job, jobKey = &prJob{}, ""
			jobs[m[1]] = job
			continue
		}
		if job == nil {
			continue
		}
		if m := prJobKey.FindStringSubmatch(line); m != nil {
			closeStep()
			jobKey = m[1]
			switch jobKey {
			case "if":
				job.cond = m[2]
			case "continue-on-error":
				job.soft = m[2] != "false"
			case "needs":
				job.needs, job.unread = prNeeds(m[2])
			}
			continue
		}
		if jobKey == "needs" {
			if m := prNeedsItem.FindStringSubmatch(line); m != nil {
				job.needs = append(job.needs, m[1])
			} else {
				job.unread = true
			}
			continue
		}
		if jobKey == "defaults" && strings.HasPrefix(text, "shell:") {
			job.shell = true
		}
		if jobKey != "steps" {
			continue
		}
		if strings.HasPrefix(line, "      - ") {
			closeStep()
			step = &prStep{}
			line = "        " + line[len("      - "):]
		}
		if step == nil {
			continue
		}
		if m := prStepKey.FindStringSubmatch(line); m != nil {
			inBlock, heredoc = false, ""
			switch m[1] {
			case "id":
				step.id = m[2]
			case "if":
				step.cond = m[2]
			case "continue-on-error":
				step.soft = m[2] != "false"
			case "shell":
				step.shell = true
			case "run":
				switch v := m[2]; {
				case v == "|" || v == "|-":
					inBlock = true
				case v == "" || strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">"):
					// a block style this test does not read: nothing counts
				default:
					step.run = append(step.run, "run: "+v)
				}
			}
			continue
		}
		// A deeper line belongs to the key above it: a line of the run block,
		// or a line under with:, env: and the rest, which no shell runs.
		if !inBlock || !strings.HasPrefix(line, "          ") {
			continue
		}
		if heredoc != "" {
			if text == heredoc {
				heredoc = ""
			}
			continue
		}
		step.block = append(step.block, text)
		if m := prHeredoc.FindStringSubmatch(text); m != nil {
			heredoc = m[1]
		}
	}
	closeStep()
	if workflowShell {
		for _, j := range jobs {
			j.shell = true
		}
	}
	return jobs
}

// prNeeds reads `needs: a`, `needs: [a, b]`, or an empty value whose items
// follow as a block list. Anything else is unread, which refuses.
func prNeeds(value string) ([]string, bool) {
	if value == "" {
		return nil, false
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		var out []string
		for _, name := range strings.Split(strings.Trim(value, "[]"), ",") {
			name = strings.TrimSpace(name)
			if !prNeedsName.MatchString(name) {
				return nil, true
			}
			out = append(out, name)
		}
		return out, false
	}
	if prNeedsName.MatchString(value) {
		return []string{value}, false
	}
	return nil, true
}

// prRuns says whether a job runs on every pull request its workflow runs on,
// and fails it when it fails: no if:, no continue-on-error, and every job it
// needs runs too. A job that needs a skipped job is skipped with it.
func prRuns(jobs map[string]*prJob, name string, visiting map[string]bool) bool {
	j, ok := jobs[name]
	if !ok || visiting[name] || j.cond != "" || j.soft || j.unread {
		return false
	}
	visiting[name] = true
	defer delete(visiting, name)
	for _, need := range j.needs {
		if !prRuns(jobs, need, visiting) {
			return false
		}
	}
	return true
}

// prRunningJobs returns, for every workflow triggered by every pull request,
// the jobs prRuns accepts, in a stable order.
func prRunningJobs(workflows map[string]string) []*prJob {
	names := make([]string, 0, len(workflows))
	for name := range workflows {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []*prJob
	for _, name := range names {
		if !prTriggered(workflows[name]) {
			continue
		}
		jobs := prParse(workflows[name])
		jobNames := make([]string, 0, len(jobs))
		for jn := range jobs {
			jobNames = append(jobNames, jn)
		}
		sort.Strings(jobNames)
		for _, jn := range jobNames {
			if prRuns(jobs, jn, map[string]bool{}) && !jobs[jn].shell {
				out = append(out, jobs[jn])
			}
		}
	}
	return out
}

// carried says whether some step that runs on every pull request, and fails
// it when the line fails, has `want` as a line a shell runs.
//
// A line counts only in a job and a step that carry no `if:` at all.
// Deciding whether an expression such as `github.event_name == 'schedule' &&
// ...` admits a pull request would mean evaluating GitHub's expression
// language here, and an evaluator that misreads one says "runs on pull
// requests" about a job that never does. So any condition refuses, and so
// does everything else that keeps a line from failing a pull request:
// continue-on-error on the step or the job, a job that needs a job that does
// not run, a trigger filtered by paths, branches or types, a line under
// `with:`, a line inside a heredoc, a `shell:` on the step or under the
// `defaults:` of its job or workflow, and what prShellLines drops.
// TestAConditionedJobDoesNotCountAsRunningOnAPullRequest has a case for each,
// and each was "runs on pull requests" in the first version of this test.
//
// What it does NOT prove, said rather than implied: anything decided by
// running the shell rather than reading it, such as an `exit 0` or a `set +e`
// reached through a function, `eval`, `source`, a `trap` or a called script.
func carried(workflows map[string]string, want string) bool {
	for _, job := range prRunningJobs(workflows) {
		for _, st := range job.steps {
			if st.cond == "" && !st.soft && !st.shell && slices.Contains(st.run, want) {
				return true
			}
		}
	}
	return false
}

// prFailedLater is the one condition this test reads, spelled this way and no
// other: on a pull_request event, when the step whose id it names failed. Its
// meaning is fixed, so reading it is not evaluating GitHub's expression
// language; a different spelling of the same idea refuses.
func prFailedLater(id string) string {
	return "github.event_name == 'pull_request' && steps." + id + ".outcome == 'failure'"
}

// failedByALaterStep says whether, in a job that runs on every pull request,
// one unconditioned step with an id runs every line of `lines`, and a later
// step of the same job, conditioned by prFailedLater on that id and not
// continue-on-error, runs `exit 1`. The first step may be continue-on-error:
// `outcome` is the result before continue-on-error is applied, which is the
// point of reading it.
func failedByALaterStep(workflows map[string]string, lines []string) bool {
	for _, job := range prRunningJobs(workflows) {
		for i, st := range job.steps {
			if st.id == "" || st.cond != "" || st.shell || !containsAll(st.run, lines) {
				continue
			}
			for _, later := range job.steps[i+1:] {
				if later.cond == prFailedLater(st.id) && !later.soft && !later.shell &&
					(slices.Contains(later.run, "exit 1") || slices.Contains(later.run, "run: exit 1")) {
					return true
				}
			}
		}
	}
	return false
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// TestAConditionedJobDoesNotCountAsRunningOnAPullRequest: one planted workflow
// per question, each wrong in that one way only, and the accepting half. A
// body without its own `on:` block is triggered by an unfiltered pull_request.
func TestAConditionedJobDoesNotCountAsRunningOnAPullRequest(t *testing.T) {
	const want = "run: make gate"
	const on = "on:\n  pull_request:\n"
	for _, tc := range []struct {
		name, body string
		counts     bool
	}{
		{"an unconditioned job and step", "jobs:\n  a:\n    runs-on: x\n    steps:\n      - name: g\n        run: make gate\n", true},
		{"a step written as - run:", "jobs:\n  a:\n    steps:\n      - run: make gate\n", true},
		{"a job whose if: keeps it to the schedule", "jobs:\n  a:\n    if: github.event_name == 'schedule'\n    steps:\n      - name: g\n        run: make gate\n", false},
		{"a job whose if: is set after its steps", "jobs:\n  a:\n    steps:\n      - name: g\n        run: make gate\n    if: github.event_name == 'schedule'\n", false},
		{"a step whose if: keeps it off pull requests", "jobs:\n  a:\n    steps:\n      - name: g\n        if: github.event_name != 'pull_request'\n        run: make gate\n", false},
		{"a job whose if: follows two steps conditions the first too", "jobs:\n  a:\n    steps:\n      - run: make gate\n      - run: other\n    if: github.event_name == 'schedule'\n", false},
		{"a conditioned job does not taint the next one", "jobs:\n  a:\n    if: false\n    steps:\n      - run: other\n  b:\n    steps:\n      - run: make gate\n", true},
		{"a step whose if is written with a space before the colon", "jobs:\n  a:\n    steps:\n      - name: g\n        if : false\n        run: make gate\n", false},
		{"a step whose if is single-quoted", "jobs:\n  a:\n    steps:\n      - 'if': false\n        run: make gate\n", false},
		{"a job whose if is double-quoted", "jobs:\n  a:\n    \"if\": false\n    steps:\n      - run: make gate\n", false},
		{"a job whose if is written with a space before the colon", "jobs:\n  a:\n    if : false\n    steps:\n      - run: make gate\n", false},
		{"a line outside jobs:", "# run: make gate\nrun: make gate\njobs:\n  a:\n    steps:\n      - run: other\n", false},

		// continue-on-error, on the step and on the job, and the accepting half.
		{"a step that is continue-on-error", "jobs:\n  a:\n    steps:\n      - name: g\n        continue-on-error: true\n        run: make gate\n", false},
		{"a job that is continue-on-error", "jobs:\n  a:\n    continue-on-error: true\n    steps:\n      - run: make gate\n", false},
		{"continue-on-error: false does not refuse", "jobs:\n  a:\n    continue-on-error: false\n    steps:\n      - name: g\n        continue-on-error: false\n        run: make gate\n", true},

		// needs: a job skipped with the job it needs.
		{"a job that needs a conditioned job", "jobs:\n  a:\n    if: github.event_name == 'schedule'\n    steps:\n      - run: other\n  b:\n    needs: a\n    steps:\n      - run: make gate\n", false},
		{"a job that needs, in a list, a conditioned job", "jobs:\n  a:\n    steps:\n      - run: other\n  c:\n    if: false\n    steps:\n      - run: other\n  b:\n    needs: [a, c]\n    steps:\n      - run: make gate\n", false},
		{"a job that needs an unconditioned job", "jobs:\n  a:\n    steps:\n      - run: other\n  b:\n    needs:\n      - a\n    steps:\n      - run: make gate\n", true},

		// A line no shell runs.
		{"a line under the with: of a uses: step", "jobs:\n  a:\n    steps:\n      - uses: some/action@0000000000000000000000000000000000000000\n        with:\n          run: make gate\n", false},
		{"a line inside a heredoc of a run: block", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          cat <<'EOF' > note.txt\n          run: make gate\n          EOF\n", false},
		{"a line of a run: block after its heredoc ends", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          cat <<EOF > note.txt\n          text\n          EOF\n          run: make gate\n", true},

		// A trigger that keeps some pull requests out.
		{"a pull_request filtered by paths", "on:\n  pull_request:\n    paths: ['docs/**']\njobs:\n  a:\n    steps:\n      - run: make gate\n", false},
		{"a pull_request filtered by paths-ignore", "on:\n  pull_request:\n    paths-ignore: ['**.go']\njobs:\n  a:\n    steps:\n      - run: make gate\n", false},
		{"a pull_request filtered by branches", "on:\n  pull_request:\n    branches: [release]\njobs:\n  a:\n    steps:\n      - run: make gate\n", false},
		{"a pull_request filtered by types", "on:\n  pull_request:\n    types: [labeled]\njobs:\n  a:\n    steps:\n      - run: make gate\n", false},
		{"a pull_request followed by another trigger", "on:\n  pull_request:\n  # every pull request\n  workflow_dispatch:\njobs:\n  a:\n    steps:\n      - run: make gate\n", true},

		// The shell around the line, as far as it is read without running it.
		{"a step with a shell: of its own", "jobs:\n  a:\n    steps:\n      - name: g\n        shell: bash {0}\n        run: make gate\n", false},
		{"a job whose defaults set the shell", "jobs:\n  a:\n    defaults:\n      run:\n        shell: bash {0}\n    steps:\n      - run: make gate\n", false},
		{"a workflow whose defaults set the shell", "defaults:\n  run:\n    shell: bash {0}\njobs:\n  a:\n    steps:\n      - run: make gate\n", false},
		{"a set +e earlier in the block", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          set +e\n          run: make gate\n", false},
		{"an exit 0 earlier in the block", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          test -f x || exit 0\n          run: make gate\n", false},
		{"a bare exit earlier in the block", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          echo done; exit\n          run: make gate\n", false},
		// Refused by the whole-line match, not by prShellLines: a line ending
		// with `\` is never the line asked for. A check for it there was dead,
		// and its falsification read TEST STILL PASSED on 2026-11-14.
		{"a line ending with a backslash is not the line", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          run: make gate \\\n            --quiet\n", false},
		{"a line continued from the line before", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          echo \\\n          run: make gate\n", false},
		{"a line whose next line starts with ||", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          run: make gate\n          || true\n", false},
		{"set -e and set +x earlier, exit 1 and set +e later", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          set -e\n          set +x\n          run: make gate\n          set +e\n          exit 1\n", true},
		{"an exit 0 inside a heredoc is data", "jobs:\n  a:\n    steps:\n      - name: g\n        run: |\n          cat <<'EOF' > s.sh\n          exit 0\n          EOF\n          run: make gate\n", true},
		{"defaults without a shell", "defaults:\n  run:\n    working-directory: x\njobs:\n  a:\n    defaults:\n      run:\n        working-directory: y\n    steps:\n      - run: make gate\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if !strings.HasPrefix(body, "on:") {
				body = on + body
			}
			if got := carried(map[string]string{"w.yml": body}, want); got != tc.counts {
				t.Errorf("carried = %v, want %v for:\n%s", got, tc.counts, body)
			}
		})
	}
}

// TestAStepFailedByALaterStepIsReadOnlyInOneSpelling holds the drift:check
// proof: one case per question failedByALaterStep asks, and the accepting
// half, which is the shape drift.yml writes.
func TestAStepFailedByALaterStepIsReadOnlyInOneSpelling(t *testing.T) {
	lines := []string{"gate || status=$?", "exit $status"}
	gate := "      - name: gate\n        id: report\n        continue-on-error: true\n        run: |\n          status=0\n          gate || status=$?\n          exit $status\n"
	fail := func(cond, extra, exit string) string {
		return "      - name: fail\n        if: " + cond + "\n" + extra + "        run: |\n          echo moved >&2\n          " + exit + "\n"
	}
	good := "github.event_name == 'pull_request' && steps.report.outcome == 'failure'"
	head := "on:\n  pull_request:\njobs:\n  a:\n    steps:\n"
	for _, tc := range []struct {
		name, body string
		counts     bool
	}{
		{"the shape drift.yml writes", head + gate + fail(good, "", "exit 1"), true},
		{"the later step exits 0", head + gate + fail(good, "", "exit 0"), false},
		{"the later step is continue-on-error", head + gate + fail(good, "        continue-on-error: true\n", "exit 1"), false},
		{"the later step reads conclusion, which continue-on-error turns to success", head + gate + fail("github.event_name == 'pull_request' && steps.report.conclusion == 'failure'", "", "exit 1"), false},
		{"the later step names another id", head + gate + fail("github.event_name == 'pull_request' && steps.other.outcome == 'failure'", "", "exit 1"), false},
		{"the later step is written before the gate", head + fail(good, "", "exit 1") + gate, false},
		{"the gate step carries an if:", head + strings.Replace(gate, "        id: report\n", "        id: report\n        if: github.event_name == 'schedule'\n", 1) + fail(good, "", "exit 1"), false},
		{"the gate step no longer hands back its status", head + strings.Replace(gate, "exit $status", "exit 0", 1) + fail(good, "", "exit 1"), false},
		{"the job is conditioned", strings.Replace(head, "    steps:\n", "    if: github.event_name == 'schedule'\n    steps:\n", 1) + gate + fail(good, "", "exit 1"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := failedByALaterStep(map[string]string{"w.yml": tc.body}, lines); got != tc.counts {
				t.Errorf("failedByALaterStep = %v, want %v for:\n%s", got, tc.counts, tc.body)
			}
		})
	}
}

func keys(m map[string][]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func keysOf(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// TestTheTestplanCheckHandsBackItsOwnExitCode: `testplan --check` exits 2 on a
// path no rule triages, and the task and the CI step must hand that 2 back
// (rule 9: 0 ok, 1 error, 2 drift). Through `go run` it read 1, measured on
// 2026-11-14. Driven on a throwaway clone, so the answer does not depend on
// what the working tree happens to hold: one committed file, then one
// untracked file in a directory no rule names, and the accepting half first.
func TestTheTestplanCheckHandsBackItsOwnExitCode(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "tools", "testplan", "check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Through newThrowawayRepo, never a bare exec.Command: the first shape of
	// this test inherited the pre-push hook's GIT_DIR and GIT_INDEX_FILE, and
	// its `git add` wrote into the real worktree's index (2026-11-15).
	repo := newThrowawayRepo(t, t.TempDir())
	clone := repo.Dir
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo.Git("add", "README.md")
	repo.Git("commit", "-q", "-m", "base")

	run := func() (string, int) {
		t.Helper()
		cmd := exec.Command("bash", "--noprofile", "--norc", script)
		cmd.Dir = clone
		// check.sh runs git itself, so it gets the scrubbed environment too:
		// an inherited GIT_DIR would have it plan another repository's diff.
		cmd.Env = repo.Env()
		out, runErr := cmd.CombinedOutput()
		var exit *exec.ExitError
		switch {
		case errors.As(runErr, &exit):
			return string(out), exit.ExitCode()
		case runErr != nil:
			t.Fatalf("check.sh could not be run, so nothing was measured: %v\n%s", runErr, out)
		}
		return string(out), 0
	}

	if out, code := run(); code != 0 || !strings.Contains(out, "Nothing to run") {
		t.Fatalf("a clone with nothing changed: exit %d, want 0 saying nothing changed:\n%s", code, out)
	}

	if err := os.MkdirAll(filepath.Join(clone, "zz_untriaged"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "zz_untriaged", "probe.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := run()
	if code != 2 || !strings.Contains(out, "zz_untriaged/probe.txt") {
		t.Fatalf("an untriaged path: exit %d, want 2 naming it (1 is what `go run` turns 2 into):\n%s", code, out)
	}
}
