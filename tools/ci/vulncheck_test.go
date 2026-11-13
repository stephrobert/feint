package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tools/ci/vulncheck.py drives govulncheck on a schedule (#865) and moves every
// Go pin together when an advisory reaches the pinned version.
//
// These tests run the real script with a stubbed `govulncheck` on PATH, the way
// night_report_test.go stubs `gh`: the subject is the script's decisions, and a
// test that needed the network would measure the network.

// An advisory whose fix is newer than the pin, in govulncheck's own JSON shape.
const advisoryJSON = `{"osv":{"id":"GO-2026-6859","affected":[{"package":{"name":"stdlib"},` +
	`"ranges":[{"type":"SEMVER","events":[{"introduced":"1.26.0"},{"fixed":"go1.26.12"}]}]}]}}`

// An advisory already fixed in the pinned version: nothing to do.
const oldAdvisoryJSON = `{"osv":{"id":"GO-2026-0001","affected":[{"package":{"name":"stdlib"},` +
	`"ranges":[{"type":"SEMVER","events":[{"introduced":"1.25.0"},{"fixed":"go1.26.4"}]}]}]}}`

// stubVulncheck puts a fake govulncheck on PATH. It prints what the caller
// chose and exits with code, because the real one exits non-zero when it finds
// something — that is its normal answer, not a failure.
func stubVulncheck(t *testing.T, stdout string, code int) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/usr/bin/env bash\ncat <<'JSON'\n" + stdout + "\nJSON\nexit " +
		map[bool]string{true: "0", false: "3"}[code == 0] + "\n"
	path := filepath.Join(dir, "govulncheck")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // a test stub
		t.Fatal(err)
	}
	return dir
}

// repoCopy copies the files that carry a pin into a temporary tree, so a test
// that rewrites them cannot touch the working copy.
func repoCopy(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dst, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range listPinFiles(t, root) {
		body, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // committed files
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, rel), body, 0o644); err != nil { //nolint:gosec // a temp copy
			t.Fatal(err)
		}
	}
	return dst
}

func listPinFiles(t *testing.T, root string) []string {
	t.Helper()
	out := []string{"mise.toml"}
	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yml") {
			out = append(out, filepath.Join(".github", "workflows", e.Name()))
		}
	}
	return out
}

func runVulncheck(t *testing.T, root, stubDir string, apply bool) (string, int) {
	t.Helper()
	args := []string{filepath.Join("..", "..", "tools", "ci", "vulncheck.py"), "--root", root}
	if apply {
		args = append(args, "--apply")
	}
	cmd := exec.Command("python3", args...)
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatalf("run the script: %v\n%s", err, out)
	}
	return string(out), code
}

// A night with nothing to report says so. The criterion #865 writes is exactly
// this: "a night with no advisory produces neither a pull request nor an issue,
// and it is proven other than by absence — the job's log says it concluded".
func TestAQuietNightSaysItConcluded(t *testing.T) {
	root := repoCopy(t)
	out, code := runVulncheck(t, root, stubVulncheck(t, oldAdvisoryJSON, 0), false)
	if code != 0 {
		t.Errorf("exit %d, want 0: an advisory already fixed in the pin moves nothing\n%s", code, out)
	}
	if !strings.Contains(out, "no advisory moves the pin") {
		t.Errorf("a quiet night printed nothing a reader can tell from a dead schedule:\n%s", out)
	}
}

// An advisory past the pin moves every pin, and says which advisory and which
// versions — the three facts #865 asks the pull request to carry.
func TestAnAdvisoryPastThePinMovesEveryPin(t *testing.T) {
	root := repoCopy(t)
	// Counted before the rewrite rather than written here. A number in this
	// file would be a second source of truth, and the first thing it did was
	// go stale: adding vulncheck.yml made it 18.
	before := countPins(t, root, "1.26.11")
	if before < 10 {
		t.Fatalf("only %d pin(s) found before the rewrite; the copy is wrong and this test would "+
			"pass on an empty tree", before)
	}
	out, code := runVulncheck(t, root, stubVulncheck(t, advisoryJSON, 3), true)
	if code != 2 {
		t.Fatalf("exit %d, want 2 (the repository's drift code)\n%s", code, out)
	}
	for _, want := range []string{"GO-2026-6859", "found in go1.26.11", "fixed in go1.26.12"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not name %q:\n%s", want, out)
		}
	}

	var left int
	for _, rel := range listPinFiles(t, root) {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "go-version: '1.26.11'") ||
				strings.HasPrefix(trimmed, `go = "1.26.11"`) {
				left++
				t.Errorf("%s still pins 1.26.11: %s", rel, trimmed)
			}
		}
	}
	if left == 0 {
		if moved := countPins(t, root, "1.26.12"); moved != before {
			t.Errorf("%d pins carry the new version and %d carried the old one: every pin moves "+
				"together, or the toolchain here and the one in CI drift apart", moved, before)
		}
	}
}

// countPins counts the pins, which is not the same as counting the occurrences
// of a version string: go.yml names past versions in its comments, and those
// are history, not pins.
func countPins(t *testing.T, root, version string) int {
	t.Helper()
	var n int
	for _, rel := range listPinFiles(t, root) {
		body, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // a temp copy
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "go-version: '"+version+"'" || strings.HasPrefix(trimmed, `go = "`+version+`"`) {
				n++
			}
		}
	}
	return n
}

// The one that matters, and the reason a global search-and-replace is wrong.
//
// .github/workflows/go.yml carries the history of the four previous moves in
// its comments: "MOVED TO 1.26.9 on 2026-10-19 ... GO-2026-6617 ... found in
// go1.26.6 and fixed in go1.26.9". Rewriting those sentences would turn a
// record of what happened into a record of what never happened — the exact
// defect this repository calls generated prose that nobody edits.
func TestOnlyPinsMoveAndNotTheHistory(t *testing.T) {
	root := repoCopy(t)
	before, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "go.yml"))
	if err != nil {
		t.Fatal(err)
	}
	history := historySentences(string(before))
	if len(history) < 3 {
		t.Fatalf("go.yml carries %d history sentence(s) naming a version; this test needs them to "+
			"have something to protect", len(history))
	}

	if _, code := runVulncheck(t, root, stubVulncheck(t, advisoryJSON, 3), true); code != 2 {
		t.Fatalf("the scan did not move the pin, so this test proves nothing")
	}

	after, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "go.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range history {
		if !strings.Contains(string(after), line) {
			t.Errorf("a sentence recording a past move was rewritten:\n  %s", line)
		}
	}
}

// historySentences returns the comment lines that name a Go patch version.
func historySentences(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "1.26.") || strings.Contains(trimmed, "go1.26.") {
			out = append(out, line)
		}
	}
	return out
}

// govulncheck failing is not govulncheck finding nothing. A scan that cannot
// conclude must say so: #705 is the story of a schedule whose silence was read
// as health.
func TestAScanThatCannotConcludeIsNotAQuietNight(t *testing.T) {
	root := repoCopy(t)
	out, code := runVulncheck(t, root, stubVulncheck(t, "", 3), false)
	if code == 0 {
		t.Errorf("a govulncheck that produced no JSON exited 0: the night would be read as green\n%s", out)
	}
	if !strings.Contains(out, "no JSON") {
		t.Errorf("the failure does not say what went wrong:\n%s", out)
	}
}
