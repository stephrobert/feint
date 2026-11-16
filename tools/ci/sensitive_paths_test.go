package ci

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sensitive-paths.sh (#988) decides whether a pull request from outside the
// project may change a sensitive path. These tests drive the real script with
// a stubbed `gh` that serves the three API answers it reads (the pull request,
// its file list, its reviews), and an event payload written per case.
//
// One test per question the script asks, each with an input that fails that
// question only: a payload that fails several at once cannot tell them apart,
// and removing one refusal would change no verdict. Every refusal is replayed
// with its control removed by
// tools/falsify/specs/an-outside-pull-request-waits-on-a-sensitive-path.json.

const sensitiveGhStub = `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"${GH_STUB_LOG}"
[[ "$1" == api ]] || { echo "unexpected gh call: $*" >&2; exit 64; }
case "$*" in
  *"/pulls/"*"/files"*) answer="${GH_STUB_DIR}/files.json" ;;
  *"/pulls/"*"/reviews"*) answer="${GH_STUB_DIR}/reviews.json" ;;
  *"/pulls/"*) answer="${GH_STUB_DIR}/pull.json" ;;
  *) echo "unexpected gh api call: $*" >&2; exit 64 ;;
esac
# No fixture is an API failure, the way gh reports one: a message and exit 1.
[[ -f "${answer}" ]] || { echo "HTTP 502: Bad Gateway" >&2; exit 1; }
cat "${answer}"
`

const (
	testHeadSHA  = "0123456789abcdef0123456789abcdef01234567"
	testOlderSHA = "fedcba9876543210fedcba9876543210fedcba98"
)

type prCase struct {
	action      string
	association string
	files       []map[string]string // nil: the files call fails
	changed     int                 // -1: len(files)
	reviews     []map[string]any    // nil: the reviews call fails
	apiHead     string              // the head the API reports; "" is testHeadSHA
	noPull      bool                // the pull request call fails
	raw         string              // when set, the files answer verbatim
	rawReviews  string              // when set, the reviews answer verbatim
	author      string              // the pull request's author; "" is "someone"
	headRepo    string              // the head's repository; "" is the base, "-" is null (a deleted fork)
}

type prResult struct {
	code           int
	stdout, stderr string
	ghCalls        []string
}

func file(name string) map[string]string { return map[string]string{"filename": name} }

// review is one entry of GET /pulls/{n}/reviews, field-trimmed to what the
// script reads.
func review(id int, login, association, state, commit string) map[string]any {
	return map[string]any{
		"id": id, "user": map[string]any{"login": login},
		"author_association": association, "state": state, "commit_id": commit,
	}
}

func runSensitivePaths(t *testing.T, c prCase) prResult {
	t.Helper()
	script, err := filepath.Abs("sensitive-paths.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(sensitiveGhStub), 0o755); err != nil {
		t.Fatal(err)
	}

	changed := c.changed
	if changed < 0 {
		changed = len(c.files)
	}
	apiHead := c.apiHead
	if apiHead == "" {
		apiHead = testHeadSHA
	}
	var author any = c.author
	switch c.author {
	case "":
		author = "someone"
	case "-":
		author = nil
	}
	var headRepo any = map[string]any{
		"full_name": "stephrobert/feint", "owner": map[string]any{"login": "stephrobert"},
	}
	switch c.headRepo {
	case "":
	case "-":
		headRepo = nil
	default:
		owner, _, _ := strings.Cut(c.headRepo, "/")
		headRepo = map[string]any{"full_name": c.headRepo, "owner": map[string]any{"login": owner}}
	}
	// The event carries what both triggers carry, and no changed_files: the
	// pull request object of a pull_request_review event has none.
	writeJSON(t, filepath.Join(dir, "event.json"), map[string]any{
		"action": c.action,
		"pull_request": map[string]any{
			"number": 42, "author_association": c.association,
			"user": map[string]any{"login": author},
			"base": map[string]any{"repo": map[string]any{"full_name": "stephrobert/feint"}},
			"head": map[string]any{"sha": testHeadSHA, "repo": headRepo},
		},
	})
	if !c.noPull {
		writeJSON(t, filepath.Join(dir, "pull.json"), map[string]any{
			"number": 42, "changed_files": changed, "head": map[string]any{"sha": apiHead},
		})
	}
	switch {
	case c.raw != "":
		writeRaw(t, filepath.Join(dir, "files.json"), c.raw)
	case c.files != nil:
		// --slurp: an array of pages. Two pages when there is enough to split,
		// so the count across pages is exercised.
		pages := [][]map[string]string{c.files}
		if len(c.files) > 1 {
			pages = [][]map[string]string{c.files[:1], c.files[1:]}
		}
		writeJSON(t, filepath.Join(dir, "files.json"), pages)
	}
	switch {
	case c.rawReviews != "":
		writeRaw(t, filepath.Join(dir, "reviews.json"), c.rawReviews)
	case c.reviews != nil:
		writeJSON(t, filepath.Join(dir, "reviews.json"), []any{c.reviews})
	}

	logPath := filepath.Join(dir, "gh.log")
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_STUB_DIR="+dir, "GH_STUB_LOG="+logPath,
		"GITHUB_EVENT_PATH="+filepath.Join(dir, "event.json"),
		"GITHUB_REPOSITORY=stephrobert/feint",
	)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("the script did not run: %v", err)
		}
		code = exit.ExitCode()
	}
	var calls []string
	if data, err := os.ReadFile(logPath); err == nil {
		calls = strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	return prResult{code: code, stdout: stdout.String(), stderr: stderr.String(), ghCalls: calls}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, path, string(data))
}

func writeRaw(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// expect asserts the exit code AND the reason: exit 2 answers several
// questions, and a case that reaches it through the wrong branch measures
// nothing.
func expect(t *testing.T, r prResult, code int, reason string) {
	t.Helper()
	if r.code != code || !strings.Contains(r.stdout+r.stderr, reason) {
		t.Errorf("want exit %d with %q, got exit %d\nstdout:\n%s\nstderr:\n%s",
			code, reason, r.code, r.stdout, r.stderr)
	}
}

const (
	reasonTrusted   = "trusted, nothing to check"
	reasonClean     = "is a sensitive path"
	reasonUnread    = "need the maintainer's review"
	reasonApproved  = "approved on head commit " + testHeadSHA
	reasonStale     = "commits were pushed after the approval"
	reasonBlocked   = "a trusted reviewer requests changes"
	reasonTruncated = "and the pull request announces"
	reasonMoved     = "the head moved from"
)

// sensitive is an outside pull request touching one sensitive path, with the
// reviews given: the frame of every review question below.
func sensitive(action string, reviews ...map[string]any) prCase {
	if reviews == nil {
		reviews = []map[string]any{}
	}
	return prCase{
		action: action, association: "CONTRIBUTOR",
		files: []map[string]string{file("mise.toml")}, changed: -1, reviews: reviews,
	}
}

// The accepting half of the trust question: the three associations GitHub
// gives the project's own people pass without a single API call, even on a
// sensitive path and without any review.
func TestTheProjectsOwnPeoplePassAtOnce(t *testing.T) {
	for _, association := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
		r := runSensitivePaths(t, prCase{
			action: "opened", association: association,
			files: []map[string]string{file(".github/workflows/go.yml")}, changed: -1,
		})
		expect(t, r, 0, reasonTrusted)
		if len(r.ghCalls) != 0 {
			t.Errorf("%s: a trusted author still cost API calls: %v", association, r.ghCalls)
		}
	}
}

// Every other association is outside, including one GitHub does not have
// today: an unrecognised value must refuse, not pass. Dependabot (NONE) is
// outside on purpose (the script's comment says why). Each case touches a
// sensitive path with no review, so the trust question is the only one that
// can let it through.
func TestAnyoneElseIsJudged(t *testing.T) {
	for _, association := range []string{
		"CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", "FIRST_TIMER", "NONE", "MANNEQUIN", "MAINTAINER",
	} {
		c := sensitive("opened")
		c.association = association
		expect(t, runSensitivePaths(t, c), 2, reasonUnread)
	}
}

// The accepting half of the path question: an outside pull request that
// touches nothing sensitive passes, including names one character away from a
// listed one.
func TestAnOutsidePullRequestOffTheListPasses(t *testing.T) {
	r := runSensitivePaths(t, prCase{
		action: "opened", association: "CONTRIBUTOR",
		files: []map[string]string{
			file("internal/core/store/store.go"), file("docs/limits.md"),
			file("internal/cli/doctor_test.go"), file("tools/conformance/README.md"),
			file("Dockerfile.md"), file(".githubx/notes"), file("examples/go.mod.txt"),
			file("docs/requirements.txt"), file("tools/reviewer.md"),
		},
		changed: -1,
	})
	expect(t, r, 0, "none of the 9 changed files "+reasonClean)
}

// Every pattern of the list bites, read from the list itself rather than
// copied here: each pattern is turned into a path it must catch, `*` becoming
// a nested name so the pattern is shown to reach any depth. And every path
// named by the issue and by cycle 1 of its review is on it, so a line deleted
// from the list is a red test.
func TestEveryListedPatternCatchesAPath(t *testing.T) {
	data, err := os.ReadFile("sensitive-paths.txt")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		paths = append(paths, strings.ReplaceAll(line, "*", "deep/er/name"))
	}
	if len(paths) == 0 {
		t.Fatal("sensitive-paths.txt lists no pattern: this test would assert nothing")
	}
	named := []string{
		".github/workflows/go.yml", ".github/dependabot.yml", "tools/ci/fetch.sh",
		"tools/conformance/scaleway/cli.sh", "tools/falsify/falsify.py",
		"tools/falsify/specs/x.json", "tools/ci/sensitive-paths.txt", "go.mod", "go.sum",
		"mise.toml", ".pre-commit-config.yaml", "Dockerfile", ".golangci.yml",
		".plumber.yaml", ".poutine.yml",
		// cycle 1: the hash-locked Python requirements CI installs, the Python
		// project files `uv run` resolves, and the code that judges a pull request
		"tools/ci/commitizen-requirements.txt", "tools/ci/uv-requirements.txt",
		"pyproject.toml", "uv.lock",
		"tools/ci/job_bounds_test.go", "tools/review/main.go", "tools/review/README.md",
		"tools/testplan/rules.go",
	}
	for _, path := range append(paths, named...) {
		r := runSensitivePaths(t, prCase{
			action: "opened", association: "CONTRIBUTOR",
			files: []map[string]string{file(path)}, changed: -1, reviews: []map[string]any{},
		})
		expect(t, r, 2, reasonUnread)
		if !strings.Contains(r.stdout, path) {
			t.Errorf("%s: refused without being named in the output:\n%s", path, r.stdout)
		}
	}
}

// A rename reports the new path as filename and the old one as
// previous_filename. Moving a workflow out of .github/ deletes it, so the old
// path is judged too.
func TestARenameOutOfASensitivePathIsJudged(t *testing.T) {
	r := runSensitivePaths(t, prCase{
		action: "opened", association: "CONTRIBUTOR",
		files: []map[string]string{{
			"filename": "docs/old-workflow.yml", "previous_filename": ".github/workflows/go.yml",
		}},
		changed: -1, reviews: []map[string]any{},
	})
	expect(t, r, 2, reasonUnread)
}

// The accepting half of the approval: the owner's approval on the head commit.
func TestAnApprovalOnTheHeadCommitPasses(t *testing.T) {
	r := runSensitivePaths(t, sensitive("submitted",
		review(1, "stephrobert", "OWNER", "APPROVED", testHeadSHA)))
	expect(t, r, 0, reasonApproved)
}

// Approval, then a push: the review keeps the commit_id GitHub recorded, which
// is no longer the head.
func TestAnApprovalOnAnOlderCommitIsRefused(t *testing.T) {
	r := runSensitivePaths(t, sensitive("synchronize",
		review(1, "stephrobert", "OWNER", "APPROVED", testOlderSHA)))
	expect(t, r, 2, reasonStale)
}

// The race the label could not see: the contributor pushes the head one second
// before the maintainer submits an approval written while reading the previous
// commit. The run is the review's, and the review is bound to the previous
// commit, so the head it never read is refused.
func TestAPushJustBeforeTheApprovalIsRefused(t *testing.T) {
	r := runSensitivePaths(t, sensitive("submitted",
		review(7, "stephrobert", "OWNER", "APPROVED", testOlderSHA)))
	expect(t, r, 2, reasonStale)
}

// An approval from outside the project is no approval.
func TestAnOutsideReviewerCannotApprove(t *testing.T) {
	r := runSensitivePaths(t, sensitive("submitted",
		review(1, "a-friend", "CONTRIBUTOR", "APPROVED", testHeadSHA)))
	expect(t, r, 2, reasonUnread)
}

// The same reviewer approves, then requests changes: the latest review
// decides, even when the approval is on the head commit.
func TestALaterChangesRequestedWithdrawsTheApproval(t *testing.T) {
	r := runSensitivePaths(t, sensitive("submitted",
		review(1, "stephrobert", "OWNER", "APPROVED", testHeadSHA),
		review(2, "stephrobert", "OWNER", "CHANGES_REQUESTED", testHeadSHA)))
	expect(t, r, 2, reasonBlocked)
}

// The latest is the highest id, not the last one listed: GitHub lists by id
// today, and a reordering must not resurrect an approval.
func TestTheLatestReviewIsChosenById(t *testing.T) {
	r := runSensitivePaths(t, sensitive("submitted",
		review(9, "stephrobert", "OWNER", "CHANGES_REQUESTED", testHeadSHA),
		review(3, "stephrobert", "OWNER", "APPROVED", testHeadSHA)))
	expect(t, r, 2, reasonBlocked)
}

// The accepting half of "latest decides": a reviewer who requested changes and
// then approved the head has lifted their own request, and a comment after an
// approval withdraws nothing.
func TestALaterApprovalLiftsTheReviewersOwnRequest(t *testing.T) {
	r := runSensitivePaths(t, sensitive("submitted",
		review(1, "stephrobert", "OWNER", "CHANGES_REQUESTED", testOlderSHA),
		review(2, "stephrobert", "OWNER", "APPROVED", testHeadSHA),
		review(3, "stephrobert", "OWNER", "COMMENTED", testHeadSHA)))
	expect(t, r, 0, reasonApproved)
}

// Two trusted reviewers: one approves the head, the other's latest review
// requests changes. The request blocks, as it does on GitHub's own page.
func TestAnotherTrustedReviewerCanBlock(t *testing.T) {
	r := runSensitivePaths(t, sensitive("submitted",
		review(1, "stephrobert", "OWNER", "APPROVED", testHeadSHA),
		review(2, "a-collaborator", "COLLABORATOR", "CHANGES_REQUESTED", testHeadSHA)))
	expect(t, r, 2, reasonBlocked)
}

// Two reviewers, one outside: an outsider's request for changes blocks nothing,
// or anyone could hold any pull request red.
func TestAnOutsiderCannotBlock(t *testing.T) {
	r := runSensitivePaths(t, sensitive("submitted",
		review(1, "stephrobert", "OWNER", "APPROVED", testHeadSHA),
		review(2, "a-passer-by", "NONE", "CHANGES_REQUESTED", testHeadSHA)))
	expect(t, r, 0, reasonApproved)
}

// A dismissed review is the reviewer's latest word: an approval given before a
// review that was later dismissed does not come back.
func TestADismissedReviewLeavesNoApproval(t *testing.T) {
	r := runSensitivePaths(t, sensitive("dismissed",
		review(1, "stephrobert", "OWNER", "APPROVED", testHeadSHA),
		review(2, "stephrobert", "OWNER", "DISMISSED", testHeadSHA)))
	expect(t, r, 2, reasonUnread)
}

// The files endpoint stops at 3000 entries. A list shorter than the pull
// request announces is not judged, even when what it shows is harmless: the
// missing file could be the sensitive one.
func TestAShortFileListIsNotJudged(t *testing.T) {
	r := runSensitivePaths(t, prCase{
		action: "opened", association: "CONTRIBUTOR",
		files: []map[string]string{file("docs/limits.md")}, changed: 3001,
	})
	expect(t, r, 1, reasonTruncated)
}

// The run belongs to the event's head. If the pull request moved on, its files
// and reviews describe another commit: nothing is judged, and the run of the
// new head does the judging. Here the files are harmless, so only this
// question can stop it.
func TestAHeadThatMovedIsNotJudged(t *testing.T) {
	r := runSensitivePaths(t, prCase{
		action: "submitted", association: "CONTRIBUTOR", apiHead: testOlderSHA,
		files: []map[string]string{file("docs/limits.md")}, changed: -1,
	})
	expect(t, r, 1, reasonMoved)
}

// Three outcomes: a failed API call and an answer of unknown shape are "cannot
// judge", never "nothing sensitive" nor "no approval".
func TestAnUnreadableAnswerIsNotJudged(t *testing.T) {
	r := runSensitivePaths(t, prCase{action: "opened", association: "CONTRIBUTOR", noPull: true})
	expect(t, r, 1, "pull request 42 could not be read")

	r = runSensitivePaths(t, prCase{action: "opened", association: "CONTRIBUTOR", changed: 1})
	expect(t, r, 1, "file list of pull request 42 could not be read")

	r = runSensitivePaths(t, prCase{
		action: "opened", association: "CONTRIBUTOR", changed: 0, raw: `{"message":"Not Found"}`,
	})
	expect(t, r, 1, "file list of pull request 42 has a shape this script does not know")

	c := sensitive("submitted")
	c.reviews = nil
	expect(t, runSensitivePaths(t, c), 1, "reviews of pull request 42 could not be read")

	c = sensitive("submitted")
	c.rawReviews = `[[{"state":"APPROVED"}]]`
	expect(t, runSensitivePaths(t, c), 1, "reviews of pull request 42 have a shape this script does not know")

	r = runSensitivePaths(t, prCase{
		action: "opened", association: "",
		files: []map[string]string{file("mise.toml")}, changed: -1,
	})
	expect(t, r, 1, "no author_association")
}

// A path is the contributor's text. Printed raw, a path holding a newline
// would put a line starting with `::` in the log, which the runner reads as a
// workflow command. No line the script prints may start with `::` except its
// own stop-commands markers.
func TestAPathCannotForgeAWorkflowCommand(t *testing.T) {
	r := runSensitivePaths(t, prCase{
		action: "opened", association: "CONTRIBUTOR",
		files:   []map[string]string{file(".github/x\n::error title=forged::forged")},
		changed: -1, reviews: []map[string]any{},
	})
	expect(t, r, 2, reasonUnread)
	opened := false
	for _, line := range strings.Split(r.stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "::stop-commands::"):
			opened = true
		case strings.HasPrefix(line, "::") && opened && strings.HasSuffix(line, "::") && !strings.Contains(line, " "):
			opened = false // the end marker
		case strings.HasPrefix(line, "::"):
			t.Errorf("a path reached the log as a workflow command: %q", line)
		}
	}
	if !strings.Contains(r.stdout, "::stop-commands::") {
		t.Errorf("the paths were printed without stopping workflow commands:\n%s", r.stdout)
	}
}

// The workflow wakes on what changes the verdict: a push and a review, both
// its kinds (an approval submitted and an approval dismissed). Its job name is
// the context the ruleset is to require. And it never runs main's workflow
// with a write token.
func TestTheWorkflowListensToReviewsUnderItsRequiredName(t *testing.T) {
	workflow := readWorkflow(t, "sensitive-paths.yml")
	for _, want := range []string{
		"\n    name: Sensitive paths\n",
		"\n  pull_request:\n    types: [opened, synchronize, reopened]\n",
		"\n  pull_request_review:\n    types: [submitted, dismissed]\n",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("sensitive-paths.yml lacks %q", want)
		}
	}
	for _, refused := range []string{"pull_request_target", "labeled", "checks: read"} {
		if strings.Contains(workflow, refused) {
			t.Errorf("sensitive-paths.yml still holds %q", refused)
		}
	}
}

// Trust follows who can push to the head (cycle 2 of #988). A collaborator's
// pull request from a branch of a stranger's fork is pushed to by the
// stranger, so it is judged as an outside one: sensitive path, no review,
// refused.
func TestACollaboratorOnAStrangersForkIsJudged(t *testing.T) {
	c := sensitive("synchronize")
	c.association, c.author, c.headRepo = "COLLABORATOR", "a-collaborator", "a-stranger/feint"
	expect(t, runSensitivePaths(t, c), 2, reasonUnread)
}

// A head whose repository is gone (a deleted fork) has nobody to vouch for who
// pushed it: judged as outside, so an approval on its head is still a way out.
func TestAHeadFromADeletedForkIsJudged(t *testing.T) {
	c := sensitive("synchronize")
	c.association, c.author, c.headRepo = "MEMBER", "a-member", "-"
	expect(t, runSensitivePaths(t, c), 2, reasonUnread)

	c.reviews = []map[string]any{review(1, "stephrobert", "OWNER", "APPROVED", testHeadSHA)}
	expect(t, runSensitivePaths(t, c), 0, reasonApproved)
}

// The accepting half: a collaborator's own fork is pushed to by the
// collaborator alone, and stays trusted without any API call.
func TestACollaboratorsOwnForkIsTrusted(t *testing.T) {
	c := sensitive("synchronize")
	c.association, c.author, c.headRepo = "COLLABORATOR", "a-collaborator", "a-collaborator/feint"
	r := runSensitivePaths(t, c)
	expect(t, r, 0, reasonTrusted)
	if len(r.ghCalls) != 0 {
		t.Errorf("a trusted author still cost API calls: %v", r.ghCalls)
	}
}

// A review by a deleted account comes back as `user: null`. It decides
// nothing, and it does not make the rest of the list unjudgeable: here it is a
// member's request for changes, and the owner's approval of the head stands.
func TestAReviewFromADeletedAccountIsIgnored(t *testing.T) {
	ghost := review(1, "", "MEMBER", "CHANGES_REQUESTED", testHeadSHA)
	ghost["user"] = nil
	r := runSensitivePaths(t, sensitive("submitted",
		ghost, review(2, "stephrobert", "OWNER", "APPROVED", testHeadSHA)))
	expect(t, r, 0, reasonApproved)
}

// The fail-open the comparison would hide: with no author login and no head
// repository, both sides of "owner == author" are null and equal. A payload
// that names no author is not judged, never trusted.
func TestAPayloadWithNoAuthorIsNotTrusted(t *testing.T) {
	c := sensitive("synchronize")
	c.association, c.author, c.headRepo = "MEMBER", "-", "-"
	expect(t, runSensitivePaths(t, c), 1, "no author login")
}
