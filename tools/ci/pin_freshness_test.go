package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tools/ci/pin-freshness.sh answers whether a pinned tool version is still the
// current minor series upstream.
//
// It exists because a version literal in a workflow cannot know it has gone
// stale. plumber sat pinned at 0.4.22 from 2026-07-30 to 2026-11-09 while
// upstream shipped three minor series, and the damage was not the version
// number: .plumber.yaml enumerated its controls, so the 33 controls added in
// the meantime were absent, absent means disabled, and the badge read 100/100
// over a shrinking subset. 89/100 once every control was active.
//
// The logic lives in a script rather than in a `run:` block for the reason
// night-report.sh already states: a `run:` block cannot be executed off GitHub
// Actions, and this repository has audited cases of CI fixes that existed only
// as comments. Every branch below is driven with a stubbed `gh`.
func TestPinFreshness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pinned   string
		upstream string // what the stubbed gh prints; "" makes it fail
		wantCode int
		wantOut  string
	}{
		{
			// The pin is current. Nothing to say, and nothing to red.
			name: "the pin is the latest release", pinned: "0.7.23",
			upstream: "v0.7.23", wantCode: 0, wantOut: "the pin is the latest release",
		},
		{
			// A patch behind within the same minor. plumber shipped eight
			// patches in ten days, so this must NOT red or the gate is gone
			// within a month.
			name: "a patch behind, same minor", pinned: "0.7.16",
			upstream: "v0.7.23", wantCode: 0, wantOut: "same minor series",
		},
		{
			// The case that happened. Exit 2, the repository's drift code.
			name: "a whole minor behind", pinned: "0.4.22",
			upstream: "v0.7.23", wantCode: 2, wantOut: "a different minor series",
		},
		{
			// Three minors is still one verdict; the message must name both
			// versions so the red is actionable without opening the workflow.
			name: "the message names both versions", pinned: "0.4.22",
			upstream: "v0.7.23", wantCode: 2, wantOut: "pinned at 0.4.22 and upstream is on 0.7.23",
		},
		{
			// A pin AHEAD of upstream is a different minor too, and it is a
			// real state: a release yanked upstream, or a typo in the pin.
			// Reporting it as fresh would be the instrument lying.
			name: "the pin is ahead of upstream", pinned: "0.9.0",
			upstream: "v0.7.23", wantCode: 2, wantOut: "a different minor series",
		},
		{
			// "We could not look" is not "nothing moved". Exit 1, never 0 and
			// never 2: a caller that cannot tell the two apart learns nothing.
			name: "upstream unreadable", pinned: "0.7.23",
			upstream: "", wantCode: 1, wantOut: "could not read the latest release",
		},
		{
			// A tag that is not a dotted version cannot be compared, and a
			// silent fallback would compare "nightly" against "0.7.23" and
			// answer something.
			name: "upstream answers a non-version tag", pinned: "0.7.23",
			upstream: "nightly", wantCode: 1, wantOut: "not a dotted version",
		},
		{
			// A leading v on the pin is accepted on both sides, because the
			// repository writes some pins with it (OCTL_VERSION: 'v0.0.32')
			// and some without (SCW_VERSION: '2.56.3').
			name: "a v-prefixed pin compares equal", pinned: "v0.7.23",
			upstream: "v0.7.23", wantCode: 0, wantOut: "the pin is the latest release",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runPinFreshness(t, "getplumber/plumber", tc.pinned, tc.upstream)
			if code != tc.wantCode {
				t.Errorf("exit %d, want %d\n%s", code, tc.wantCode, out)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output does not contain %q\n%s", tc.wantOut, out)
			}
		})
	}
}

// An empty pin is its own case: it is what a renamed or deleted workflow
// variable produces, and comparing "" against a real version would call the
// pin stale and send somebody looking for a release that is already current.
func TestPinFreshnessRefusesAnEmptyPin(t *testing.T) {
	code, out := runPinFreshness(t, "getplumber/plumber", "", "v0.7.23")
	if code != 1 {
		t.Errorf("exit %d, want 1 (cannot conclude)\n%s", code, out)
	}
	if !strings.Contains(out, "the pinned version is empty") {
		t.Errorf("output does not name the empty pin\n%s", out)
	}
}

// Wrong arity is exit 1 as well, and it is worth a case because the script is
// called from a workflow where a dropped argument is a plausible edit.
func TestPinFreshnessRefusesWrongArity(t *testing.T) {
	for _, args := range [][]string{{}, {"getplumber/plumber"}, {"a", "b", "c"}} {
		code, out := runPinFreshnessArgs(t, "v0.7.23", args...)
		if code != 1 {
			t.Errorf("args %v: exit %d, want 1\n%s", args, code, out)
		}
		if !strings.Contains(out, "usage:") {
			t.Errorf("args %v: output does not print usage\n%s", args, out)
		}
	}
}

// ghStub replaces `gh` on PATH. It dispatches on the subcommand rather than on
// the whole argument line: matching the full line makes the stub fall through
// silently when an argument order changes, which is a harness lying before its
// subject.
//
// PIN_FRESHNESS_UPSTREAM empty means "upstream unreadable": the stub exits
// non-zero, which is what a network failure or an unknown repository looks
// like to the script.
const ghReleaseStub = `#!/usr/bin/env bash
case "$1 $2" in
  "release view")
    if [ -z "${PIN_FRESHNESS_UPSTREAM:-}" ]; then
      echo "could not resolve to a Repository" >&2
      exit 1
    fi
    printf '%s\n' "${PIN_FRESHNESS_UPSTREAM}"
    ;;
  *)
    echo "unexpected gh call: $*" >&2
    exit 64
    ;;
esac
`

func runPinFreshness(t *testing.T, repo, pinned, upstream string) (int, string) {
	t.Helper()
	return runPinFreshnessArgs(t, upstream, repo, pinned)
}

func runPinFreshnessArgs(t *testing.T, upstream string, args ...string) (int, string) {
	t.Helper()

	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(ghReleaseStub), 0o755); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}

	script := filepath.Join("..", "..", "tools", "ci", "pin-freshness.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the script this test exists for is missing: %v", err)
	}

	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PIN_FRESHNESS_UPSTREAM="+upstream,
	)
	out, err := cmd.CombinedOutput()

	// Anything that is not a non-zero exit is a harness failure, and must not
	// be read as a verdict: a stub that could not start would otherwise look
	// like the script concluding.
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run %s: %v\n%s", script, err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}
