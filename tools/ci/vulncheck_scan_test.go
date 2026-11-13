package ci

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tools/ci/vulncheck-scan.sh answers whether an advisory touches the pinned
// Go, by asking govulncheck against vuln.go.dev.
//
// It exists because the verdict of that question moves without a single line
// of this repository changing — GO-2026-6859 reddened the pull-request gate
// on 2026-11-11 for a toolchain that had been green the day before, and
// nothing in the repository had moved. A gate that fails on the network
// rather than on the code teaches --no-verify, so the question is asked on a
// schedule instead, and this script is the schedule's judgement. The scan
// job is what decides whether the night is red, and a wrong verdict here is
// a pull request nobody needed, or an advisory nobody was told about.
//
// The logic lives in a script rather than in a `run:` block for the reason
// night-report.sh already states: a `run:` block cannot be executed off
// GitHub Actions, and this repository has audited cases of CI fixes that
// existed only as comments. Every branch below is driven with a stubbed
// govulncheck replaying a canned -json stream — including the measured shape
// of the real GO-2026-6859 run under go1.26.10 — and every verdict is asserted
// on both the exit code and the outputs, because an advisory verdict that
// nobody can act on is a red night that says nothing.
//
// The exit-code contract: 0 is clean, 2 is an advisory touching the pin, 1
// is "the scan could not conclude". The pin the script reads is the pin in
// mise.toml, and a finding whose trace names any other version is a scan of
// the wrong toolchain, never a verdict.
func TestVulncheckScan(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pin        string // the pin the fake mise.toml carries
		stub       string // which canned -json the stubbed govulncheck replays
		wantCode   int
		wantVerd   string
		wantOut    []string // substrings of the outputs file, in order
		wantNoOut  []string // substrings that must NOT appear in the outputs file
		wantStdout string
	}{
		{
			// The pin is clean. Nothing to move, nothing to say — and the
			// conclusion proves the night happened, with the database
			// timestamp the scan read, because a silent schedule is
			// indistinguishable from a dead one.
			name: "clean night", pin: "1.26.11", stub: "clean", wantCode: 0,
			wantVerd:   "clean",
			wantOut:    []string{"verdict=clean", "advisory=", "found=1.26.11", "fixed=", "db=2026-11-11"},
			wantStdout: "found no advisory touching the pinned Go 1.26.11",
		},
		{
			// The case that happened: GO-2026-6859, found in net/http@go1.26.10,
			// fixed in go1.26.11 — the exact payload the real run produced
			// under go1.26.10, replayed here with its two finding traces.
			// The fixed version is the newest fixed_version across findings,
			// and the advisory list is the unique set, sorted.
			name: "advisory touches the pin", pin: "1.26.10", stub: "advisory", wantCode: 2,
			wantVerd:   "advisory",
			wantOut:    []string{"verdict=advisory", "advisory=GO-2026-6859", "found=1.26.10", "fixed=1.26.11"},
			wantStdout: "GO-2026-6859 touches the pinned Go 1.26.10, fixed in 1.26.11",
		},
		{
			// Two advisories at once, fixed in different versions: the bump
			// must move to the NEWEST fixed version, and the advisory list
			// must name both, sorted, so the pull request can cite them.
			name: "two advisories, fixed in different versions", pin: "1.26.9", stub: "two", wantCode: 2,
			wantVerd:   "advisory",
			wantOut:    []string{"verdict=advisory", "advisory=GO-2026-6809,GO-2026-6859", "found=1.26.9", "fixed=1.26.11"},
			wantStdout: "GO-2026-6809,GO-2026-6859 touches the pinned Go 1.26.9, fixed in 1.26.11",
		},
		{
			// A finding about a module the repository requires is NOT an
			// advisory touching the pin: it is osv-scanner.yml's weekly watch,
			// and a pin-bump pull request would not fix it. It is counted in
			// the conclusion, never dropped, and never opens a pin PR.
			name: "a finding in a required module is not a pin advisory", pin: "1.26.11", stub: "module", wantCode: 0,
			wantVerd:   "clean",
			wantOut:    []string{"verdict=clean", "advisory=", "found=1.26.11", "fixed="},
			wantStdout: "1 finding(s) in required modules",
		},
		{
			// The scan measured a different toolchain than the pin: the
			// finding is real, but the verdict would be about the wrong Go.
			// Inconclusive, never advisory.
			name: "the trace names a different toolchain than the pin", pin: "1.26.11", stub: "advisory", wantCode: 1,
			wantVerd:   "inconclusive",
			wantOut:    []string{"verdict=inconclusive", "advisory=", "fixed="},
			wantStdout: "the scan ran under Go 1.26.10, not the pinned 1.26.11",
		},
		{
			// A finding whose fixed version is not newer than the pin is a
			// contradiction in the database: it cannot be the reason to move.
			name: "the fixed version is not newer than the pin", pin: "1.26.11", stub: "not-newer", wantCode: 1,
			wantVerd:   "inconclusive",
			wantOut:    []string{"verdict=inconclusive", "advisory=", "fixed="},
			wantStdout: "a finding is fixed in 1.26.11, which is not newer than the pinned 1.26.11",
		},
		{
			// govulncheck failed: the database could not be read, or the
			// network could not be reached. "We could not look" is not "clean"
			// — exit 1, and the night job reports it through night-report.yml.
			name: "govulncheck fails", pin: "1.26.11", stub: "fails", wantCode: 1,
			wantVerd:   "inconclusive",
			wantOut:    []string{"verdict=inconclusive"},
			wantStdout: "govulncheck exited 1",
		},
		{
			// An empty -json stream is a scan that produced nothing to judge:
			// -json always prints at least its config object, so silence is a
			// broken tool, not a clean tree.
			name: "empty output", pin: "1.26.11", stub: "empty", wantCode: 1,
			wantVerd:   "inconclusive",
			wantOut:    []string{"verdict=inconclusive"},
			wantStdout: "govulncheck -json printed nothing",
		},
		{
			// A -json stream that does not parse cannot be judged either.
			name: "unparseable output", pin: "1.26.11", stub: "garbage", wantCode: 1,
			wantVerd:   "inconclusive",
			wantOut:    []string{"verdict=inconclusive"},
			wantStdout: "govulncheck -json output did not parse",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, outputs := runVulncheckScan(t, tc.pin, tc.stub)
			if code != tc.wantCode {
				t.Errorf("exit %d, want %d\n%s", code, tc.wantCode, stdout)
			}
			if !strings.Contains(outputs, "verdict="+tc.wantVerd+"\n") && !strings.Contains(outputs, "verdict="+tc.wantVerd) {
				t.Errorf("outputs do not contain verdict=%q\n%s", tc.wantVerd, outputs)
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(outputs, want) {
					t.Errorf("outputs do not contain %q\n%s", want, outputs)
				}
			}
			for _, no := range tc.wantNoOut {
				if strings.Contains(outputs, no) {
					t.Errorf("outputs must not contain %q\n%s", no, outputs)
				}
			}
			if tc.wantStdout != "" && !strings.Contains(stdout, tc.wantStdout) {
				t.Errorf("output does not contain %q\n%s", tc.wantStdout, stdout)
			}
		})
	}
}

// A missing pin is its own case: it is what a renamed mise.toml key or a
// moved task produces, and scanning without a pin to compare against would
// call any finding an advisory.
func TestVulncheckScanRefusesAMissingPin(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mise.toml"), []byte("[tools]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, outputs := runScanIn(t, root, "clean")
	if code != 1 {
		t.Errorf("exit %d, want 1\n%s", code, stdout)
	}
	if !strings.Contains(outputs, "verdict=inconclusive") {
		t.Errorf("outputs do not contain verdict=inconclusive\n%s", outputs)
	}
	if !strings.Contains(stdout, "mise.toml names no Go pin") {
		t.Errorf("output does not contain %q\n%s", "mise.toml names no Go pin", stdout)
	}
}

// runVulncheckScan builds a fake repository root carrying the given pin and a
// stubbed govulncheck replaying the named canned -json, then runs the real
// script in it and returns its exit code, its stdout, and the contents of the
// GITHUB_OUTPUT file it wrote.
func runVulncheckScan(t *testing.T, pin, stub string) (int, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mise.toml"), []byte(fmt.Sprintf("[tools]\ngo = %q\n", pin)), 0o644); err != nil {
		t.Fatal(err)
	}
	return runScanIn(t, root, stub)
}

func runScanIn(t *testing.T, root, stub string) (int, string, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubPath := filepath.Join(bin, "govulncheck")
	if err := os.WriteFile(stubPath, []byte(govulncheckStub), 0o755); err != nil {
		t.Fatal(err)
	}
	outputs := filepath.Join(t.TempDir(), "outputs")
	script, err := filepath.Abs("vulncheck-scan.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"GOVULNCHECK_BIN=govulncheck",
		"GOVULNCHECK_CASE="+stub,
		"GITHUB_OUTPUT="+outputs,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	data, readErr := os.ReadFile(outputs)
	if readErr != nil && code != 1 {
		// A clean or advisory night must write its outputs; an inconclusive
		// one writes them too, but the assertion is on the verdict line.
		t.Fatalf("reading the outputs file: %v\n%s", readErr, out)
	}
	return code, string(out), string(data)
}

// The stub replays canned -json streams. The advisory case is the measured
// shape of the real GO-2026-6859 run under go1.26.10: a config object
// carrying the database timestamp, an SBOM object, and two finding entries
// for the same advisory (two call sites, one verdict). The two-advisory case
// adds GO-2026-6809 fixed in 1.26.10, so the newest fixed version is
// 1.26.11 and the advisory list must name both.
const govulncheckStub = `#!/usr/bin/env bash
config() { printf '%s\n' "{\"config\":{\"protocol_version\":\"v1.0.0\",\"scanner_name\":\"govulncheck\",\"scanner_version\":\"v1.3.0\",\"db\":\"https://vuln.go.dev\",\"db_last_modified\":\"2026-11-11T02:51:11Z\",\"go_version\":\"go1.26.11\",\"scan_level\":\"symbol\",\"scan_mode\":\"source\"}}"; }
sbom() { printf '%s\n' "{\"SBOM\":{\"go_version\":\"go1.26.11\",\"modules\":[{\"path\":\"repro\"},{\"path\":\"stdlib\",\"version\":\"v1.26.11\"}]}}"; }
finding() { printf '%s\n' "{\"finding\":{\"osv\":\"$1\",\"fixed_version\":\"$2\",\"trace\":[{\"module\":\"stdlib\",\"version\":\"v$3\",\"package\":\"net/http\",\"symbols\":[\"bodyEOFSignal.Read\"]},{\"module\":\"stdlib\",\"version\":\"v$3\",\"package\":\"net/http\"}]}}"; }
module_finding() { printf '%s\n' "{\"finding\":{\"osv\":\"GO-2026-1234\",\"fixed_version\":\"v1.2.4\",\"trace\":[{\"module\":\"github.com/foo/bar\",\"version\":\"v1.2.3\",\"package\":\"bar\"}]}}"; }
case "${GOVULNCHECK_CASE}" in
  clean)   config; sbom ;;
  advisory) config; sbom; finding GO-2026-6859 v1.26.11 1.26.10; finding GO-2026-6859 v1.26.11 1.26.10 ;;
  two)     config; sbom; finding GO-2026-6809 v1.26.10 1.26.9; finding GO-2026-6859 v1.26.11 1.26.9 ;;
  module)  config; sbom; module_finding ;;
  not-newer) config; sbom; finding GO-2026-6859 v1.26.11 1.26.11 ;;
  fails)   echo "network unreachable" >&2; exit 1 ;;
  empty)   exit 0 ;;
  garbage) echo "this is not json" ;;
esac
exit 0
`
