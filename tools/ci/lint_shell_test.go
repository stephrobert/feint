package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// `mise run lint-shell` listed its scripts with `git ls-files`, which answers
// only what git already tracks: a script being written, not yet added, was
// never read, and the task answered green on a fault it had not looked at.
// Measured on 2026-11-16 with an untracked script holding SC2086: the old
// command exited 0, the new one 123 (#988).
//
// The test runs the task's own `run` line from mise.toml in a throwaway
// repository, with shellcheck replaced by a stub that prints the files it was
// handed, so it asks which files the task selects and needs no shellcheck.
// tools/falsify/specs/the-runner-reaches-only-what-was-measured.json puts the
// old command back and requires the test to go red.

// lintShellRun matches the task's `run` line inside its own table.
var lintShellRun = regexp.MustCompile(`(?m)^\[tasks\.lint-shell\]\n(?:[^\[\n].*\n|\n)*?run = ("(?:[^"\\]|\\.)*")`)

func TestLintShellReadsAScriptGitDoesNotTrackYet(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	m := lintShellRun.FindSubmatch(raw)
	if m == nil {
		t.Fatal("no run line under [tasks.lint-shell]: a shape this test does not read")
	}
	run, err := strconv.Unquote(string(m[1]))
	if err != nil {
		t.Fatalf("the run line is not a plain string: %v", err)
	}

	dir := t.TempDir()
	repo := newThrowawayRepo(t, filepath.Join(dir, "repo"))
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(repo.Dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.sh", "#!/usr/bin/env bash\ntrue\n")
	write(".gitignore", "ignored.sh\n")
	repo.Git("add", "tracked.sh", ".gitignore")
	repo.Git("commit", "--quiet", "-m", "tracked")
	write("tools/untracked.sh", "#!/usr/bin/env bash\necho $1\n")
	write("ignored.sh", "#!/usr/bin/env bash\necho $1\n")

	stubs := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubs, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nfor f in \"$@\"; do case \"$f\" in -*) ;; *) echo \"$f\" ;; esac; done\n"
	if err := os.WriteFile(filepath.Join(stubs, "shellcheck"), []byte(stub), 0o700); err != nil { //nolint:gosec // an executable stub, in a temp dir
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", run)
	cmd.Dir = repo.Dir
	// Built whole, so no GIT_* a hook exported can point git elsewhere.
	cmd.Env = []string{
		"PATH=" + stubs + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the task's run line failed: %v\n%s", err, out)
	}
	got := strings.Fields(string(out))
	slices.Sort(got)
	want := []string{"tools/untracked.sh", "tracked.sh"}
	if !slices.Equal(got, want) {
		t.Fatalf("lint-shell handed shellcheck %q, want exactly %q: the tracked script, the one "+
			"not yet added, and never the ignored one", got, want)
	}
}
