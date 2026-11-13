package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tools/ci/vulncheck-report.sh says what the nightly scan found, and closes
// when the pin has moved past it.
//
// It exists because the pull request carries the mechanical part — a pin
// moved to a newer patch — and the issue carries the signal a human has to
// see, because a pull request that cannot be opened by Actions (this
// repository withholds that permission on purpose) is otherwise a pushed
// branch nobody opens. When the fixed version is in the pin, a later clean
// night closes the issue: the mechanism that opens on a finding must close
// on its absence, or the issue outlives its cause and reads as a standing
// problem.
//
// The logic lives in a script rather than in a `run:` block for the reason
// night-report.sh already states: a `run:` block cannot be executed off
// GitHub Actions, and this repository has paid for CI fixes described in
// comments and never executed. Every branch below is driven with a stubbed
// `gh` replaying a canned issue list, and the assertion is on what the stub
// was asked to write — because an issue the script meant to open and did not
// is a signal nobody received.
func TestVulncheckReport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		apply   bool
		args    []string // verdict advisory found fixed branch pr-url
		issues  string   // the canned `gh issue list` JSON
		wantOut []string
		wantLog []string // substrings of the stub's write log
		noLog   []string // substrings that must NOT appear in the log
	}{
		{
			// The case that happens: an advisory touches the pin, no issue
			// open yet, and the run is a dry run — the body is printed, and
			// nothing is written anywhere.
			name:  "advisory, dry run, prints the body and writes nothing",
			apply: false, args: []string{"advisory", "GO-2026-6859", "1.26.10", "1.26.11", "chore/govulncheck-go-2026-6859", ""},
			issues: "[]",
			wantOut: []string{
				"verdict: GO-2026-6859 touches the pinned Go",
				"govulncheck: GO-2026-6859 touches the pinned Go 1.26.10",
				"The nightly scan found GO-2026-6859 in the pinned Go 1.26.10; it is fixed in 1.26.11.",
				"chore/govulncheck-go-2026-6859",
				"https://github.com/stephrobert/feint/compare/main...chore/govulncheck-go-2026-6859?expand=1",
			},
			noLog: []string{"issue create", "issue comment", "issue close", "label create"},
		},
		{
			// The same night, applied: the label is created if missing and
			// the issue is opened with the advisory in its title.
			name:  "advisory, applied, opens the issue",
			apply: true, args: []string{"advisory", "GO-2026-6859", "1.26.10", "1.26.11", "chore/govulncheck-go-2026-6859", ""},
			issues:  "[]",
			wantLog: []string{"label create govulncheck", "--title govulncheck: GO-2026-6859 touches the pinned Go 1.26.10"},
			noLog:   []string{"issue comment", "issue close"},
		},
		{
			// The advisory is still open from last night: one issue, updated,
			// never a new one per night.
			name:  "advisory, already open, comments instead of opening",
			apply: true, args: []string{"advisory", "GO-2026-6859", "1.26.10", "1.26.11", "chore/govulncheck-go-2026-6859", ""},
			issues:  `[{"number": 913, "title": "govulncheck: GO-2026-6859 touches the pinned Go 1.26.10"}]`,
			wantLog: []string{"issue comment 913"},
			noLog:   []string{"issue create", "issue close"},
		},
		{
			// A pull request was opened after all: the body carries its URL,
			// not the compare link, because a link to a pull request that
			// exists is what a maintainer opens.
			name:  "advisory with a pull request, names it",
			apply: false, args: []string{"advisory", "GO-2026-6859", "1.26.10", "1.26.11", "chore/govulncheck-go-2026-6859", "https://github.com/stephrobert/feint/pull/914"},
			issues:  "[]",
			wantOut: []string{"The pull request is https://github.com/stephrobert/feint/pull/914."},
			noLog:   []string{"issue create", "issue comment", "issue close"},
		},
		{
			// The pin has moved past the advisory: a clean night closes the
			// issue the advisory opened, with a comment that says why.
			name:  "clean night, closes the issue",
			apply: true, args: []string{"clean", "", "", "", "", ""},
			issues:  `[{"number": 913, "title": "govulncheck: GO-2026-6859 touches the pinned Go 1.26.10"}]`,
			wantOut: []string{"verdict: nothing touches the pin, closing #913"},
			wantLog: []string{"issue close 913"},
			noLog:   []string{"issue create", "issue comment"},
		},
		{
			// A clean night with nothing open: nothing to say, nothing to
			// write. The night is already proven by the scan job's log.
			name:  "clean night, nothing open, writes nothing",
			apply: true, args: []string{"clean", "", "", "", "", ""},
			issues:  "[]",
			wantOut: []string{"nothing touches the pin, no issue open, nothing to say"},
			noLog:   []string{"issue create", "issue comment", "issue close"},
		},
		{
			// The label is not an identifier: a human issue carrying it is
			// not this mechanism's, and a clean night must not close it.
			// The title prefix is what the mechanism matches on.
			name:  "clean night, leaves a human issue with the label alone",
			apply: true, args: []string{"clean", "", "", "", "", ""},
			issues:  `[{"number": 900, "title": "govulncheck should run on a schedule"}]`,
			wantOut: []string{"nothing touches the pin, no issue open, nothing to say"},
			noLog:   []string{"issue close 900"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, log := runVulncheckReport(t, tc.apply, tc.args, tc.issues)
			if code != 0 {
				t.Errorf("exit %d, want 0\n%s", code, out)
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("output does not contain %q\n%s", want, out)
				}
			}
			for _, want := range tc.wantLog {
				if !strings.Contains(log, want) {
					t.Errorf("the stub was never asked to %q\n%s", want, log)
				}
			}
			for _, no := range tc.noLog {
				if strings.Contains(log, no) {
					t.Errorf("the stub must not have been asked to %q\n%s", no, log)
				}
			}
		})
	}
}

// THE SCAN COULD NOT SAY. Neither branch applies: there is nothing to
// report, and nothing has been measured that would justify closing anything.
// Exits quiet rather than green, because the night job is what reports a
// scan that could not conclude (#705) and it reads the run, not this script.
func TestVulncheckReportInconclusiveSaysNothing(t *testing.T) {
	code, out, log := runVulncheckReport(t, true,
		[]string{"inconclusive", "", "", "", "", ""}, "[]")
	if code != 0 {
		t.Errorf("exit %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "the scan did not conclude") {
		t.Errorf("output does not contain %q\n%s", "the scan did not conclude", out)
	}
	if strings.TrimSpace(log) != "" {
		t.Errorf("the stub must not have been asked to write anything\n%s", log)
	}
}

// runVulncheckReport runs the real script with a stubbed `gh` replaying the
// canned issue list, and returns its exit code, its stdout, and the stub's
// log of every write it was asked to perform.
func runVulncheckReport(t *testing.T, apply bool, args []string, issues string) (int, string, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "gh.log")
	issuesFile := filepath.Join(t.TempDir(), "issues.json")
	if err := os.WriteFile(issuesFile, []byte(issues), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := strings.ReplaceAll(vulncheckGhStub, "__ISSUES__", issuesFile)
	stub = strings.ReplaceAll(stub, "__LOG__", log)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("vulncheck-report.sh")
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{}
	if apply {
		argv = append(argv, "--apply")
	}
	argv = append(argv, args...)
	cmd := exec.Command(script, argv...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	data, readErr := os.ReadFile(log)
	if readErr != nil {
		// The log is created on first write; a run that writes nothing
		// leaves no log, which is itself the assertion.
		data = []byte{}
	}
	return code, string(out), string(data)
}

// The stub answers `gh issue list` with the canned JSON and logs every write
// — create, comment, close, label create — with its arguments and the body
// file's contents, so the test can assert on what was asked, not on what a
// real repository would have done.
const vulncheckGhStub = `#!/usr/bin/env bash
if [ "$1" = "issue" ] && [ "$2" = "list" ]; then
  cat "__ISSUES__"
  exit 0
fi
echo "$*" >> "__LOG__"
# Log the body of a create or comment, so the test can see what was said.
prev=""
for arg in "$@"; do
  if [ "$prev" = "--body-file" ]; then cat "$arg" >> "__LOG__"; fi
  prev="$arg"
done
if [ "$1" = "issue" ] && [ "$2" = "close" ]; then
  # close takes the comment inline, after --comment.
  prev=""
  for arg in "$@"; do
    if [ "$prev" = "--comment" ]; then printf '%s\n' "$arg" >> "__LOG__"; fi
    prev="$arg"
  done
fi
exit 0
`
