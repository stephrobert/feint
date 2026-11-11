package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gofmtHookEntry reads the `entry:` line of the gofmt hook out of
// .pre-commit-config.yaml.
//
// Out of the file rather than written here, for the reason every gate in this
// repository that quotes a document reads it instead: a copy is a second
// source, and a test holding its own copy of the command proves the copy works.
func gofmtHookEntry(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", ".pre-commit-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- a fixed path inside the repository
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "- id: gofmt" {
			continue
		}
		for _, next := range lines[i:] {
			trimmed := strings.TrimSpace(next)
			if after, found := strings.CutPrefix(trimmed, "entry: bash -c "); found {
				return strings.Trim(after, "'")
			}
		}
	}
	t.Fatal("no gofmt hook with a bash entry in .pre-commit-config.yaml")
	return ""
}

// runHook executes the hook's own command with a PATH of this test's choosing,
// and answers its exit code. A directory holding only what the test put there
// is how "gofmt cannot run" is produced without touching the station.
func runHook(t *testing.T, entry, path, dir string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", entry)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if !asExitError(err, &exit) {
			t.Fatalf("running the hook: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}

// TestTheGofmtHookFailsWhenGofmtCannotRun is cited by .pre-commit-config.yaml.
//
// ## The defect, as it was measured (#829)
//
// The entry was `test -z "$(gofmt -l .)" || { … exit 1; }`. `gofmt -l` writes
// the offending files to stdout and its own errors to stderr, and the verdict
// was `test -z` over the captured stdout alone — the exit code was never read.
// So any failure of gofmt produced an empty substitution, `test -z ""` was
// true, and the hook exited 0.
//
// Not `Skipped`, not red: **Passed, asserting it looked**. Its window is a
// fresh clone before `mise trust`, which is a new contributor's first commit,
// and it is the only gate in this file with that shape — `go vet` and
// `golangci-lint` propagate their own status, and both reddened at the same
// instant on the same cause, which is how the gofmt green was noticed at all.
//
// ## Why this test runs the command instead of reading it
//
// A test asserting that the entry *contains* `||` or `$?` would pass on any
// future rewrite that happens to keep the characters and lose the property. The
// property is behavioural — a hook that cannot measure must not report success
// — so it is measured, by running the hook's own line with a PATH that holds no
// gofmt.
//
// The accepting half is asserted in the same file, because a hook that always
// failed would satisfy the test above and refuse every commit, which this
// repository has written down as worse than no gate at all.
func TestTheGofmtHookFailsWhenGofmtCannotRun(t *testing.T) {
	entry := gofmtHookEntry(t)

	// A PATH holding bash's own dependencies and no gofmt. Not an empty PATH:
	// the entry needs `test` and `echo`, which are builtins, but a future entry
	// may not be, and an empty PATH would then fail for a reason that is not
	// the subject.
	empty := t.TempDir()
	code, out := runHook(t, entry, empty, t.TempDir())
	if code == 0 {
		t.Fatalf("the hook reported success with no gofmt on PATH: it asserts it looked "+
			"when it could not look, which is the defect #829 names\n%s", out)
	}
	if !strings.Contains(out, "could not run") {
		t.Errorf("the hook failed without saying it could not measure:\n%s", out)
	}
}

// TestTheGofmtHookPassesOnFormattedCodeAndFailsOnUnformatted is the accepting
// half, and the half that keeps the fix from being a refusal of everything.
//
// Both directions in one test on purpose: a guard satisfied only by its
// refusing case is one nobody can tell from a broken one.
func TestTheGofmtHookPassesOnFormattedCodeAndFailsOnUnformatted(t *testing.T) {
	entry := gofmtHookEntry(t)
	path := os.Getenv("PATH")
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt is not on PATH, so the accepting half cannot be measured here")
	}

	t.Run("formatted", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "a.go", "package p\n\nfunc a() {}\n")
		if code, out := runHook(t, entry, path, dir); code != 0 {
			t.Errorf("the hook refused correctly formatted Go (exit %d):\n%s", code, out)
		}
	})

	t.Run("unformatted", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "a.go", "package p\nfunc a()    {}\n")
		code, out := runHook(t, entry, path, dir)
		if code == 0 {
			t.Errorf("the hook accepted Go that gofmt rewrites:\n%s", out)
		}
		if !strings.Contains(out, "a.go") {
			t.Errorf("the refusal does not name the file to fix:\n%s", out)
		}
	})
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
