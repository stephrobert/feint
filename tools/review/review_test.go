package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// These tests drive the program through a stubbed `gh` that serves one fixture
// per API call, the way tools/ci tests sensitive-paths.sh. The stub refuses
// any call it has no fixture for, and records it: a test whose fixture is
// missing must not pass as an "API that fails".
//
// One case per question: each detection has an input that passes every other
// rule and fails this one only (TestEveryRuleAsksOneQuestion), and each has an
// accepting twin, because a scanner that reports everything passes every
// attack case. Every important detection and every "cannot judge" branch is
// replayed with its control removed by
// tools/falsify/specs/an-outside-pull-request-is-read-before-it-runs.json.

const ghStub = `#!/usr/bin/env bash
set -eu
key="$(printf '%s' "$*" | tr -c 'A-Za-z0-9' '_')"
if [[ -f "${GH_STUB_DIR}/${key}.fail" ]]; then
  echo "HTTP 502: Bad Gateway" >&2
  exit 1
fi
if [[ ! -f "${GH_STUB_DIR}/${key}.json" ]]; then
  printf '%s\n' "$*" >>"${GH_STUB_DIR}/unexpected"
  echo "stub: no fixture for: $*" >&2
  exit 64
fi
cat "${GH_STUB_DIR}/${key}.json"
`

var stubKey = regexp.MustCompile(`[^A-Za-z0-9]`)

const (
	repo     = "acme/widget"
	number   = 7
	headSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	baseSHA  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

type obj = map[string]any

// world is everything the stub serves for one pull request. Each test starts
// from cleanWorld, which reports nothing, and changes one thing.
type world struct {
	pr        obj
	files     []obj
	head      map[string]string
	base      map[string]string
	treeTrunc bool
	user      obj
	total     int
	day       int
	repos     []obj
	commits   []obj
	open      []obj
	closed    []obj
	other     map[int][]obj
	fail      []string          // keys answered with a failure
	raw       map[string]string // keys answered verbatim
	paths     string            // a sensitive-path list; empty: the repository's
}

func add(lines ...string) *string {
	p := "@@ -1,0 +1," + itoa(len(lines)) + " @@\n+" + strings.Join(lines, "\n+")
	return &p
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func cleanWorld() *world {
	return &world{
		pr: obj{
			"number": number, "title": "fix: a thing", "state": "open",
			"created_at": "2026-11-16T10:00:00Z", "merged_at": nil,
			"user": obj{"login": "alice", "type": "User"}, "author_association": "NONE",
			"head": obj{"sha": headSHA}, "base": obj{"sha": baseSHA},
			"changed_files": 1, "commits": 1,
		},
		files: []obj{{
			"filename": "internal/x/x.go", "status": "modified", "additions": 1, "deletions": 0, "changes": 1,
			"patch": "@@ -10,2 +10,3 @@\n func f() error {\n+\treturn nil\n }",
		}},
		head: map[string]string{"internal/x/x.go": "100644"},
		base: map[string]string{"internal/x/x.go": "100644"},
		user: obj{"login": "alice", "type": "User", "created_at": "2020-01-01T00:00:00Z",
			"name": "Alice", "bio": nil, "followers": 3, "public_repos": 4},
		total: 12, day: 2,
		repos: []obj{
			{"fork": true, "pushed_at": "2026-11-16T09:00:00Z"},
			{"fork": false, "pushed_at": "2026-11-16T09:30:00Z"},
			{"fork": true, "pushed_at": "2026-10-01T09:00:00Z"},
		},
		commits: []obj{{"sha": otherSHA, "commit": obj{"message": "fix: a thing\n\nAssisted-by: tool (model 1)\n"}}},
		open: []obj{
			{"number": number, "user": obj{"login": "alice", "type": "User"}, "author_association": "NONE", "merged_at": nil},
			{"number": 8, "user": obj{"login": "bob", "type": "User"}, "author_association": "NONE", "merged_at": nil},
		},
		// #5 is the only outside merged pull request; #4 (the owner) and #2
		// (a bot) must not be compared, and the stub refuses their file list.
		closed: []obj{
			{"number": 5, "merged_at": "2026-11-01T00:00:00Z", "user": obj{"login": "mallory", "type": "User"}, "author_association": "CONTRIBUTOR"},
			{"number": 4, "merged_at": "2026-11-01T00:00:00Z", "user": obj{"login": "acme", "type": "User"}, "author_association": "OWNER"},
			{"number": 3, "merged_at": nil, "user": obj{"login": "eve", "type": "User"}, "author_association": "NONE"},
			{"number": 2, "merged_at": "2026-11-01T00:00:00Z", "user": obj{"login": "dependabot[bot]", "type": "Bot"}, "author_association": "NONE"},
		},
		other: map[int][]obj{5: {{"filename": "internal/y/y.go", "status": "added", "changes": 1, "patch": *add("package y")}}},
		raw:   map[string]string{},
	}
}

func tree(m map[string]string, truncated bool) obj {
	entries := []obj{}
	for p, mode := range m {
		entries = append(entries, obj{"path": p, "mode": mode})
	}
	return obj{"truncated": truncated, "tree": entries}
}

func (w *world) fixtures() map[string]any {
	f := map[string]any{
		"api repos/acme/widget/pulls/7":                                       w.pr,
		"api --paginate --slurp repos/acme/widget/pulls/7/files?per_page=100": []any{w.files},
		"api repos/acme/widget/git/trees/" + headSHA + "?recursive=1":         tree(w.head, w.treeTrunc),
		"api repos/acme/widget/git/trees/" + baseSHA + "?recursive=1":         tree(w.base, false),
		"api users/alice": w.user,
		"api search/issues?q=author:alice+type:pr&per_page=1":                                      obj{"total_count": w.total, "incomplete_results": false},
		"api search/issues?q=author:alice+type:pr+created:2026-11-16&per_page=1":                   obj{"total_count": w.day, "incomplete_results": false},
		"api users/alice/repos?sort=pushed&direction=desc&per_page=100":                            w.repos,
		"api --paginate --slurp repos/acme/widget/pulls/7/commits?per_page=100":                    []any{w.commits},
		"api --paginate --slurp repos/acme/widget/pulls?state=open&per_page=100":                   []any{w.open},
		"api repos/acme/widget/pulls?state=closed&sort=created&direction=desc&per_page=100&page=1": w.closed,
		"api repos/acme/widget/pulls?state=closed&sort=created&direction=desc&per_page=100&page=2": []any{},
	}
	for n, fs := range w.other {
		f["api --paginate --slurp repos/acme/widget/pulls/"+itoa(n)+"/files?per_page=100"] = []any{fs}
	}
	return f
}

type result struct {
	code           int
	stdout, stderr string
	unexpected     string
}

func stubDir(t *testing.T, w *world) (dir string, bin string) {
	t.Helper()
	dir = t.TempDir()
	for key, v := range w.fixtures() {
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stubKey.ReplaceAllString(key, "_")+".json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for key, body := range w.raw {
		if err := os.WriteFile(filepath.Join(dir, stubKey.ReplaceAllString(key, "_")+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range w.fail {
		if err := os.WriteFile(filepath.Join(dir, stubKey.ReplaceAllString(key, "_")+".fail"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binDir := t.TempDir()
	bin = filepath.Join(binDir, "gh")
	if err := os.WriteFile(bin, []byte(ghStub), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, bin
}

func pathsFile(t *testing.T, w *world) string {
	t.Helper()
	if w.paths == "" {
		p, err := filepath.Abs("../ci/sensitive-paths.txt")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := filepath.Join(t.TempDir(), "paths.txt")
	if err := os.WriteFile(p, []byte(w.paths), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runWorld(t *testing.T, w *world, extra ...string) result {
	t.Helper()
	dir, bin := stubDir(t, w)
	c := &client{bin: bin, env: []string{"GH_STUB_DIR=" + dir, "PATH=" + os.Getenv("PATH")}}
	args := append([]string{"--repo", repo, "--paths-file", pathsFile(t, w)}, extra...)
	args = append(args, "7")
	var out, errb bytes.Buffer
	code := run(args, &out, &errb, c)
	unexpected, _ := os.ReadFile(filepath.Join(dir, "unexpected"))
	return result{code: code, stdout: out.String(), stderr: errb.String(), unexpected: string(unexpected)}
}

func runJSON(t *testing.T, w *world) (result, report) {
	t.Helper()
	res := runWorld(t, w, "--json")
	var r report
	if res.code != exitCannot {
		if err := json.Unmarshal([]byte(res.stdout), &r); err != nil {
			t.Fatalf("--json printed no report (%v):\n%s", err, res.stdout)
		}
	}
	return res, r
}

func noUnexpected(t *testing.T, res result) {
	t.Helper()
	if res.unexpected != "" {
		t.Fatalf("the program made calls the stub has no fixture for (a harness fault, not a verdict):\n%s", res.unexpected)
	}
}

func rules(fs []finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	sort.Strings(out)
	return out
}

// The accepting half: a pull request that holds nothing the rules know exits
// 0, says so, and still prints its account facts and its assembly bound.
func TestACleanPullRequestHasNothingToReadFirst(t *testing.T) {
	res, r := runJSON(t, cleanWorld())
	noUnexpected(t, res)
	if res.code != exitClean {
		t.Fatalf("exit %d, want 0; stderr:\n%s\n%s", res.code, res.stderr, res.stdout)
	}
	if r.Verdict != "nothing-to-read-first" || len(r.Diff.Findings) != 0 || len(r.Assembly.Links) != 0 {
		t.Fatalf("a clean pull request reported something: %+v", r)
	}
	if r.Account.PRsTotal.Total != 12 || r.Account.PRsThatDay.Total != 2 || r.Account.ForksPushedDay != 1 ||
		r.Account.WithoutTrailer != 0 || r.Account.Commits[0].AssistedBy != "tool (model 1)" || !r.Account.HasName || r.Account.HasBio {
		t.Errorf("the account facts are not the fixture's: %+v", r.Account)
	}
	if len(r.Assembly.Compared) != 1 || r.Assembly.Compared[0].Number != 5 || r.Assembly.MergedRead != 3 {
		t.Errorf("the assembly compared %+v over %d merged, want only #5 over 3 (the owner's and the bot's are not outside accounts)", r.Assembly.Compared, r.Assembly.MergedRead)
	}
	if !strings.Contains(r.Assembly.Bound, "all 3 merged") {
		t.Errorf("the bound does not say all merged pull requests were read: %q", r.Assembly.Bound)
	}
	text := runWorld(t, cleanWorld())
	if text.code != exitClean || !strings.Contains(text.stdout, "nothing the rules know (exit 0)") {
		t.Errorf("the human report of a clean pull request:\n%s", text.stdout)
	}
}

func TestACommitWithoutTheTrailerIsCounted(t *testing.T) {
	w := cleanWorld()
	w.commits = []obj{{"sha": otherSHA, "commit": obj{"message": "fix: a thing\n\nSigned-off-by: x\n"}}}
	res, r := runJSON(t, w)
	noUnexpected(t, res)
	if res.code != exitClean || r.Account.WithoutTrailer != 1 || r.Account.Commits[0].AssistedBy != "" {
		t.Fatalf("exit %d, account %+v: a commit with no Assisted-by: must be counted, and is a fact, not a finding", res.code, r.Account)
	}
}

func TestAnExecCallIsFound(t *testing.T) {
	w := cleanWorld()
	w.files[0]["patch"] = "@@ -10,2 +10,3 @@\n func f() error {\n+\treturn exec.Command(name).Run()\n }"
	res, r := runJSON(t, w)
	noUnexpected(t, res)
	if res.code != exitFindings {
		t.Fatalf("exit %d, want 2:\n%s%s", res.code, res.stdout, res.stderr)
	}
	want := finding{Rule: "exec", File: "internal/x/x.go", Line: 11, Excerpt: "return exec.Command(name).Run()", Detail: "process execution: exec.Command"}
	if len(r.Diff.Findings) != 1 || r.Diff.Findings[0] != want {
		t.Fatalf("findings %+v, want exactly %+v", r.Diff.Findings, want)
	}
}

func TestANewURLIsFoundWithItsHost(t *testing.T) {
	w := cleanWorld()
	w.files[0]["patch"] = "@@ -10,2 +10,3 @@\n func f() error {\n+\tconst u = \"https://drop.evil.example:8443/x?y=1\"\n }"
	res, r := runJSON(t, w)
	noUnexpected(t, res)
	if res.code != exitFindings || len(r.Diff.Findings) != 1 || r.Diff.Findings[0].Rule != "url" ||
		r.Diff.Findings[0].Detail != "new URL, host drop.evil.example:8443" {
		t.Fatalf("exit %d, findings %+v: want one url finding naming its host", res.code, r.Diff.Findings)
	}
	// The accepting twin: a link already present in a removed line of the
	// same file is an edit around it, not a new host.
	w.files[0]["patch"] = "@@ -10,2 +10,2 @@\n func f() error {\n-\tconst u = \"https://drop.evil.example:8443/x?y=1\" // old\n+\tconst u = \"https://drop.evil.example:8443/x?y=1\"\n }"
	res, r = runJSON(t, w)
	if res.code != exitClean || len(r.Diff.Findings) != 0 {
		t.Fatalf("exit %d, findings %+v: an unchanged URL is not new", res.code, r.Diff.Findings)
	}
}

func TestASensitivePathIsFound(t *testing.T) {
	w := cleanWorld()
	w.files[0]["filename"] = ".github/workflows/ci.yml"
	w.files[0]["patch"] = "@@ -10,2 +10,3 @@\n jobs:\n+  # a comment\n   x:"
	w.head = map[string]string{".github/workflows/ci.yml": "100644"}
	w.base = w.head
	res, r := runJSON(t, w)
	noUnexpected(t, res)
	want := finding{Rule: "sensitive", File: ".github/workflows/ci.yml", Detail: "matches .github/*"}
	if res.code != exitFindings || len(r.Diff.Findings) != 1 || r.Diff.Findings[0] != want {
		t.Fatalf("exit %d, findings %+v, want exactly %+v", res.code, r.Diff.Findings, want)
	}
}

// Moving a workflow out of .github/ deletes a workflow: the previous path is
// judged as well as the new one.
func TestARenameOutOfASensitivePathIsFound(t *testing.T) {
	w := cleanWorld()
	w.files[0] = obj{"filename": "docs/old-ci.yml", "previous_filename": ".github/workflows/ci.yml", "status": "renamed", "changes": 0}
	w.head = map[string]string{"docs/old-ci.yml": "100644"}
	w.base = map[string]string{".github/workflows/ci.yml": "100644"}
	res, r := runJSON(t, w)
	noUnexpected(t, res)
	if res.code != exitFindings || strings.Join(rules(r.Diff.Findings), ",") != "sensitive" || r.Diff.Findings[0].File != ".github/workflows/ci.yml" {
		t.Fatalf("exit %d, findings %+v: the previous path of a rename must be judged", res.code, r.Diff.Findings)
	}
}

func TestEveryListedPatternCatchesAPath(t *testing.T) {
	ps, err := loadPatterns("../ci/sensitive-paths.txt")
	if err != nil {
		t.Fatal(err)
	}
	// One path per pattern of the repository's list, chosen so that `*`
	// must cross a `/` where the pattern has one.
	for path, want := range map[string]bool{
		".github/workflows/a/b.yml": true, "tools/x/y/z.sh": true, "tools/a/b.py": true,
		"tools/falsify/specs/x.json": true, "go.mod": true, "go.sum": true, "mise.toml": true,
		"Dockerfile": true, "tools/ci/sensitive-paths.txt": true,
		"internal/go.mod": false, "docs/tools/x.sh": false, "tools/x.go": false, "README.md": false,
	} {
		if _, got := sensitive(path, ps); got != want {
			t.Errorf("sensitive(%q) = %v, want %v", path, got, want)
		}
	}
}

// A pattern with a bracket means something in bash that this translation does
// not reproduce. Refused, rather than matched as a literal and missing a path
// the CI check catches.
func TestAnUnknownPatternShapeCannotBeJudged(t *testing.T) {
	w := cleanWorld()
	w.paths = "go.mod\n.github/[a-z]*\n"
	res := runWorld(t, w)
	if res.code != exitCannot || !strings.Contains(res.stderr, "has a shape this program does not translate") {
		t.Fatalf("exit %d, stderr %q: want 1 naming the pattern", res.code, res.stderr)
	}
	w.paths = "# only a comment\n\n"
	res = runWorld(t, w)
	if res.code != exitCannot || !strings.Contains(res.stderr, "holds no pattern") {
		t.Fatalf("exit %d, stderr %q: an empty list is not a list that matches nothing", res.code, res.stderr)
	}
}

// Every API read, failed on its own. A read that fails is never an empty
// answer: the program exits 1, names the call, and prints no report.
func TestAFailedAPICannotBeJudged(t *testing.T) {
	for name, key := range map[string]string{
		"pull":         "api repos/acme/widget/pulls/7",
		"files":        "api --paginate --slurp repos/acme/widget/pulls/7/files?per_page=100",
		"head-tree":    "api repos/acme/widget/git/trees/" + headSHA + "?recursive=1",
		"base-tree":    "api repos/acme/widget/git/trees/" + baseSHA + "?recursive=1",
		"user":         "api users/alice",
		"search-total": "api search/issues?q=author:alice+type:pr&per_page=1",
		"search-day":   "api search/issues?q=author:alice+type:pr+created:2026-11-16&per_page=1",
		"repos":        "api users/alice/repos?sort=pushed&direction=desc&per_page=100",
		"commits":      "api --paginate --slurp repos/acme/widget/pulls/7/commits?per_page=100",
		"open":         "api --paginate --slurp repos/acme/widget/pulls?state=open&per_page=100",
		"closed":       "api repos/acme/widget/pulls?state=closed&sort=created&direction=desc&per_page=100&page=1",
		"other-files":  "api --paginate --slurp repos/acme/widget/pulls/5/files?per_page=100",
	} {
		t.Run(name, func(t *testing.T) {
			w := cleanWorld()
			w.fail = []string{key}
			res := runWorld(t, w)
			noUnexpected(t, res)
			if res.code != exitCannot {
				t.Fatalf("exit %d, want 1:\n%s", res.code, res.stdout)
			}
			if res.stdout != "" {
				t.Errorf("a run that cannot be judged printed a report:\n%s", res.stdout)
			}
			if !strings.Contains(res.stderr, "cannot judge: gh "+key+" failed") || !strings.Contains(res.stderr, "HTTP 502") {
				t.Errorf("stderr does not name the failed call and its answer: %q", res.stderr)
			}
			js, _ := runJSON(t, w)
			if js.code != exitCannot || !strings.Contains(js.stdout, `"verdict": "cannot-judge"`) {
				t.Errorf("--json: exit %d, stdout %q", js.code, js.stdout)
			}
		})
	}
}

// A truncated or reshaped answer is not an empty list.
func TestATruncatedAnswerCannotBeJudged(t *testing.T) {
	filesKey := "api --paginate --slurp repos/acme/widget/pulls/7/files?per_page=100"
	cases := map[string]struct {
		change func(*world)
		reason string
	}{
		"cut-json":  {func(w *world) { w.raw[filesKey] = `[[{"filename":"internal/x/x.go","status":"modi` }, "not the JSON expected"},
		"not-pages": {func(w *world) { w.raw[filesKey] = `{"message":"Not Found"}` }, "not the JSON expected"},
		"no-filename": {func(w *world) {
			w.raw[filesKey] = `[[{"status":"modified","patch":"@@ -1 +1 @@\n+x"}]]`
		}, "has no filename or status"},
		"fewer-files":       {func(w *world) { w.pr["changed_files"] = 2 }, "listed 1 changed files of pull request 7 and the pull request announces 2"},
		"fewer-commits":     {func(w *world) { w.pr["commits"] = 3 }, "listed 1 commits of pull request 7 and the pull request announces 3"},
		"tree-truncated":    {func(w *world) { w.treeTrunc = true }, "is truncated by the API"},
		"no-count":          {func(w *world) { delete(w.pr, "changed_files") }, "does not announce its changed_files"},
		"bad-patch-line":    {func(w *world) { w.files[0]["patch"] = "@@ -1,1 +1,2 @@\n x\n?y" }, "has a shape this program does not know"},
		"patch-before-hunk": {func(w *world) { w.files[0]["patch"] = "+x" }, "comes before any hunk header"},
		"search-shape": {func(w *world) {
			w.raw["api search/issues?q=author:alice+type:pr&per_page=1"] = `{"message":"API rate limit exceeded"}`
		}, "answered without total_count"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := cleanWorld()
			c.change(w)
			res := runWorld(t, w)
			noUnexpected(t, res)
			if res.code != exitCannot || res.stdout != "" || !strings.Contains(res.stderr, c.reason) {
				t.Fatalf("exit %d, stdout %q, stderr %q: want 1 with %q", res.code, res.stdout, res.stderr, c.reason)
			}
		})
	}
}

var rawControl = regexp.MustCompile("[\x00-\x09\x0b-\x1f\x7f]|\u202e|\u0085")

func TestAControlCharacterInAFileNameIsEscaped(t *testing.T) {
	name := "docs/a\x1b]0;pwned\x07\u202e.md"
	w := cleanWorld()
	w.files[0]["filename"] = name
	w.files[0]["patch"] = "@@ -1,0 +1,1 @@\n+hello"
	w.head = map[string]string{name: "100644"}
	w.base = map[string]string{}
	w.pr["title"] = "fix\x1b[2J"
	text := runWorld(t, w)
	noUnexpected(t, text)
	js, r := runJSON(t, w)
	for label, out := range map[string]string{"text": text.stdout, "json": js.stdout} {
		if loc := rawControl.FindStringIndex(out); loc != nil {
			t.Errorf("the %s report carries a raw control character at byte %d: %q", label, loc[0], out[max(0, loc[0]-20):min(len(out), loc[1]+20)])
		}
	}
	if !strings.Contains(text.stdout, `docs/a\x1b]0;pwned\x07\u202e.md`) || !strings.Contains(text.stdout, `fix\x1b[2J`) {
		t.Errorf("the escaped name or title is not in the report:\n%s", text.stdout)
	}
	if text.code != exitFindings || strings.Join(rules(r.Diff.Findings), ",") != "filename" {
		t.Errorf("exit %d, findings %+v: a hidden character in a path is a finding", text.code, r.Diff.Findings)
	}
}

func TestAFileThatBecomesExecutableIsFound(t *testing.T) {
	w := cleanWorld()
	w.head = map[string]string{"internal/x/x.go": "100755"}
	res, r := runJSON(t, w)
	noUnexpected(t, res)
	if res.code != exitFindings || strings.Join(rules(r.Diff.Findings), ",") != "mode" {
		t.Fatalf("exit %d, findings %+v: want one mode finding", res.code, r.Diff.Findings)
	}
	w.base = map[string]string{"internal/x/x.go": "100755"} // already executable on the base
	res, r = runJSON(t, w)
	if res.code != exitClean || len(r.Diff.Findings) != 0 {
		t.Fatalf("exit %d, findings %+v: a file executable before the pull request is not new", res.code, r.Diff.Findings)
	}
	w.head = map[string]string{"internal/x/x.go": "120000"}
	w.base = map[string]string{"internal/x/x.go": "100644"}
	if _, r = runJSON(t, w); len(r.Diff.Findings) != 1 || !strings.Contains(r.Diff.Findings[0].Detail, "symbolic link") {
		t.Fatalf("findings %+v: a file that becomes a symbolic link is a finding", r.Diff.Findings)
	}
}

func TestAFileWithoutADiffIsFound(t *testing.T) {
	w := cleanWorld()
	w.files[0] = obj{"filename": "assets/blob.bin", "status": "added", "changes": 0}
	w.head = map[string]string{"assets/blob.bin": "100644"}
	w.base = map[string]string{}
	res, r := runJSON(t, w)
	noUnexpected(t, res)
	if res.code != exitFindings || strings.Join(rules(r.Diff.Findings), ",") != "unread" {
		t.Fatalf("exit %d, findings %+v: a file served with no diff was not read, and must say so", res.code, r.Diff.Findings)
	}
}

// An identifier added here and used by the merged code of another outside
// account, in both directions, and by another open pull request of the same
// author. Part 1 holds nothing: the exit code is 2 for the assembly alone.
func TestAnIdentifierUsedByAnotherPullRequestIsFound(t *testing.T) {
	t.Run("declared-here-used-by-merged", func(t *testing.T) {
		w := cleanWorld()
		w.files[0]["patch"] = *add("func decodeBlob(s string) []byte {", "\treturn nil", "}")
		w.other[5] = []obj{{"filename": "internal/y/y.go", "status": "added", "changes": 1, "patch": *add("package y", "var v = decodeBlob(seed) // here")}}
		assertLink(t, w, link{Identifier: "decodeBlob", DeclaredIn: 7, UsedIn: 5, File: "internal/y/y.go", Line: 2})
	})
	t.Run("declared-by-merged-used-here", func(t *testing.T) {
		w := cleanWorld()
		w.files[0]["patch"] = *add("var payload = stageOne()")
		w.other[5] = []obj{{"filename": "internal/y/y.go", "status": "added", "changes": 1, "patch": *add("package y", "", "func stageOne() []byte { return nil }")}}
		assertLink(t, w, link{Identifier: "stageOne", DeclaredIn: 5, UsedIn: 7, File: "internal/x/x.go", Line: 1})
	})
	t.Run("grouped-declaration-used-by-open-pull-of-same-author", func(t *testing.T) {
		w := cleanWorld()
		w.files[0]["patch"] = *add("const (", "\tkeyMaterial = 3", ")")
		w.open = append(w.open, obj{"number": 9, "user": obj{"login": "Alice", "type": "User"}, "author_association": "NONE", "merged_at": nil})
		w.other[9] = []obj{{"filename": "cmd/z.go", "status": "added", "changes": 1, "patch": *add("x := keyMaterial")}}
		assertLink(t, w, link{Identifier: "keyMaterial", DeclaredIn: 7, UsedIn: 9, File: "cmd/z.go", Line: 1})
	})
	t.Run("a-word-in-a-comment-is-not-a-use", func(t *testing.T) {
		w := cleanWorld()
		w.files[0]["patch"] = *add("func decodeBlob(s string) []byte {", "\treturn nil", "}")
		w.other[5] = []obj{{"filename": "internal/y/y.go", "status": "added", "changes": 1, "patch": *add("package y", "// decodeBlob is mentioned here")}}
		res, r := runJSON(t, w)
		noUnexpected(t, res)
		if res.code != exitClean || len(r.Assembly.Links) != 0 {
			t.Fatalf("exit %d, links %+v: a comment is not a use", res.code, r.Assembly.Links)
		}
	})
	// Measured on #985 on 2026-11-16: a `var (` opened in one hunk and never
	// closed in the patch kept the group open across the next hunks, and 60
	// words of function bodies (`if`, `return`, `body`) read as declarations.
	t.Run("a-group-does-not-cross-a-hunk", func(t *testing.T) {
		w := cleanWorld()
		p := "@@ -1,1 +1,2 @@\n var (\n+\tfirst = 1\n@@ -40,1 +41,2 @@\n func f() {\n+\tcounter = 3\n }"
		w.files[0]["patch"] = p
		w.other[5] = []obj{{"filename": "internal/y/y.go", "status": "added", "changes": 1, "patch": *add("package y", "var v = counter")}}
		res, r := runJSON(t, w)
		noUnexpected(t, res)
		if res.code != exitClean || len(r.Assembly.Links) != 0 {
			t.Fatalf("exit %d, links %+v: an assignment in a later hunk is not a declaration", res.code, r.Assembly.Links)
		}
	})
	t.Run("an-owners-merged-pull-is-not-compared", func(t *testing.T) {
		w := cleanWorld()
		w.files[0]["patch"] = *add("func decodeBlob(s string) []byte {", "\treturn nil", "}")
		w.closed[0]["author_association"] = "OWNER"
		// Every merged pull request is served and uses the identifier, so a
		// trust filter that let one through shows as a comparison and a link,
		// not as a call the stub refuses.
		uses := []obj{{"filename": "internal/y/y.go", "status": "added", "changes": 1, "patch": *add("var v = decodeBlob(seed)")}}
		w.other = map[int][]obj{5: uses, 4: uses, 2: uses}
		res, r := runJSON(t, w)
		noUnexpected(t, res)
		if res.code != exitClean || len(r.Assembly.Compared) != 0 {
			t.Fatalf("exit %d, compared %+v: only outside accounts are compared", res.code, r.Assembly.Compared)
		}
	})
}

func assertLink(t *testing.T, w *world, want link) {
	t.Helper()
	res, r := runJSON(t, w)
	noUnexpected(t, res)
	if res.code != exitFindings || len(r.Diff.Findings) != 0 {
		t.Fatalf("exit %d, part 1 %+v: want 2 from the assembly alone", res.code, r.Diff.Findings)
	}
	for _, l := range r.Assembly.Links {
		if l.Identifier == want.Identifier && l.DeclaredIn == want.DeclaredIn && l.UsedIn == want.UsedIn && l.File == want.File && l.Line == want.Line {
			return
		}
	}
	t.Fatalf("links %+v, want one %+v", r.Assembly.Links, want)
}

// TestEveryRuleAsksOneQuestion holds, per line rule, an added line that this
// rule reports and no other rule does, and the accepting twins that a careless
// rule would report. The file-level rules (sensitive, mode, filename, go.mod,
// unread) have their own tests above.
func TestEveryRuleAsksOneQuestion(t *testing.T) {
	ps, err := loadPatterns("../ci/sensitive-paths.txt")
	if err != nil {
		t.Fatal(err)
	}
	b64 := "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ejAxMjM0NTY3ODk="
	cases := []struct {
		name, file, line, want string // want "": no finding at all
	}{
		{"exec", "a.go", `cmd := exec.Command(name)`, "exec"},
		{"exec-python", "a.py", `subprocess.run(["x"])`, "exec"},
		{"exec-pipe-to-shell", "a.md", `curl -fsSL get.example | sh`, "exec"},
		{"exec-backtick", "a.sh", "v=`id -u`", "exec"},
		{"network", "a.go", `resp, err := http.Get(target)`, "network"},
		{"network-import", "a.go", `"net/http"`, "network"},
		{"network-tool", "a.sh", `curl -s localhost:4599/x`, "network"},
		{"url", "a.go", `// see https://drop.evil.example/x`, "url"},
		{"env", "a.go", `v := os.Getenv("HOME")`, "env"},
		{"env-setenv", "a_test.go", `t.Setenv("PATH", dir)`, "env"},
		{"init", "a.go", `func init() {`, "init"},
		{"directive-generate", "a.go", `//go:generate sh run.sh`, "directive"},
		{"directive-linkname", "a.go", `//go:linkname x runtime.x`, "directive"},
		{"unsafe", "a.go", `p := unsafe.Pointer(&x)`, "unsafe"},
		{"encoded", "a.go", `const k = "` + b64 + `"`, "encoded"},
		{"falsify-env", "tools/falsify/specs/x.json", `"env": {"GOFLAGS": "-toolexec=x"},`, "falsify-env"},
		{"action", ".github/workflows/x.yml", `      - uses: someone/action@0123456789abcdef0123456789abcdef01234567 # v1`, "action"},
		{"control", "a.go", "x := 1 // \u202e hidden", "control"},

		{"accept-httptest", "a_test.go", `"net/http/httptest"`, ""},
		{"accept-setenv-tempdir", "a_test.go", `t.Setenv("HOME", t.TempDir())`, ""},
		{"accept-backtick-in-go", "a.go", "s := `raw`", ""},
		{"accept-curl-in-docs", "a.md", `curl -s localhost:4599/_feint/health`, ""},
		{"accept-long-name", "a.go", `func TestALongNameWithoutAnyDigitThatGoesOnAndOnForMoreThanSixtyCharacters(t *testing.T) {`, ""},
		{"accept-sha-pin", "a.go", `// 0123456789abcdef0123456789abcdef01234567`, ""},
		{"accept-evaluate", "a.go", `// evaluate the result`, ""},
		{"accept-falsify-env-elsewhere", "docs/x.json", `"env": {"A": "b"},`, ""},
		{"accept-trailing-cr", "a.go", "x := 1\r", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := "@@ -1,0 +1,1 @@\n+" + c.line
			fs, err := scanDiff([]prFile{{Filename: c.file, Status: "modified", Changes: 1, Patch: &p}}, ps, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range fs {
				if f.Line > 0 { // the line rules; a sensitive file is file-level
					got = append(got, f.Rule)
				}
			}
			want := []string{}
			if c.want != "" {
				want = []string{c.want}
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%q in %s: rules %v, want %v (%+v)", c.line, c.file, got, want, fs)
			}
		})
	}
}

// go.mod anywhere changes what is built, even where the sensitive list does
// not name it.
func TestAGoModAnywhereIsFound(t *testing.T) {
	ps, err := loadPatterns("../ci/sensitive-paths.txt")
	if err != nil {
		t.Fatal(err)
	}
	p := "@@ -1,0 +1,1 @@\n+require x v1"
	fs, err := scanDiff([]prFile{{Filename: "tools/x/go.mod", Status: "modified", Changes: 1, Patch: &p}}, ps, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rules(fs), ",") != "go.mod" {
		t.Fatalf("findings %+v, want one go.mod finding", fs)
	}
}

// `go run` exits 1 whatever its child returns, which turned every report with
// findings into "cannot judge". The wrapper builds and runs the program; this
// fails if it goes back to `go run`.
func TestTheWrapperKeepsTheProgramsExitCode(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	w := cleanWorld()
	w.files[0]["patch"] = *add("cmd := exec.Command(x)")
	dir, gh := stubDir(t, w)
	script, err := filepath.Abs("external-pr.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--repo", repo, "7")
	cmd.Env = append(os.Environ(), "GH_STUB_DIR="+dir,
		"PATH="+filepath.Dir(gh)+string(os.PathListSeparator)+filepath.Dir(goBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var exit *exec.ExitError
	code := 0
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if code != exitFindings || !strings.Contains(out.String(), "[exec] internal/x/x.go:1") {
		t.Fatalf("the wrapper exited %d, want the program's 2:\n%s\n%s", code, out.String(), errb.String())
	}
}
