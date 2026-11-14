package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTrackedIgnoresTheGitDirAHookExports reproduces a hook: GIT_DIR and
// GIT_INDEX_FILE point at a decoy, and tracked() must still list this tree.
//
// The decoy is a git directory written by hand under t.TempDir() — HEAD,
// objects/ and refs/, and no index — so building it runs no git command that
// writes, and it names no tree anybody works in. Git accepts it as a
// repository whose index is empty: obeyed, `git ls-files` answers nothing and
// exits 0, which tracked() would hand back as "this tree tracks no file". The
// witness below proves the decoy does mislead before the verdict is read; a
// decoy git refused would leave tracked() on its walk fallback and this test
// green for the wrong reason.
func TestTrackedIgnoresTheGitDirAHookExports(t *testing.T) {
	decoy := t.TempDir()
	for _, dir := range []string{"objects", filepath.Join("refs", "heads")} {
		if err := os.MkdirAll(filepath.Join(decoy, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(decoy, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(decoy, "index")
	t.Setenv("GIT_DIR", decoy)
	t.Setenv("GIT_INDEX_FILE", index)

	witness := exec.Command("git", "ls-files")
	witness.Dir = root(t)
	witness.Env = append(withoutInheritedGit(os.Environ()), "GIT_DIR="+decoy, "GIT_INDEX_FILE="+index)
	out, err := witness.Output()
	if err != nil {
		t.Fatalf("git refuses the decoy (%v), so it cannot mislead tracked() and this test measures nothing", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("the decoy lists files, so an answer misled by it cannot be told from this tree's:\n%s", out)
	}

	if got := tracked(t); !slices.Contains(got, "tools/testplan/plan_test.go") {
		t.Errorf("tracked() lists %d paths without tools/testplan/plan_test.go: it listed the repository GIT_DIR named, not this tree", len(got))
	}
}
