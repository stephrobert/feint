// Package hermetic gives a test binary an environment that names no
// repository but its own working directory. It is imported by the TestMain of
// every test package of the repository, and by nothing else: every test binary
// links os/exec (testing/internal/testdeps imports internal/fuzz, which does),
// so every one can start a git.
//
// Why it exists (#955). Inside a git hook, git exports GIT_DIR,
// GIT_INDEX_FILE and friends, and a child git obeys them before it looks at
// its working directory. On 2026-11-14 a test under tools/ ran `git add` in a
// throwaway directory with the inherited environment, from the pre-push hook,
// and wrote into the index of the real worktree. A helper that scrubs the
// environment per command (tools/ci/throwaway_git_test.go) protects the calls
// that use it; a script the test runs, a program name held in a variable, or a
// Python subprocess does not go through it. Dropping GIT_* once, from the
// process itself, before any test runs, protects all of them at once.
//
// TestATestBinaryDropsTheGitDirAHookExports (tools/ci) fails without the body
// of DropInheritedGit, and TestEveryTestBinaryThatStartsAProcessDropsInheritedGit
// refuses a test binary whose TestMain does not call it first, and a package
// that reads the environment while it initialises, before TestMain can.
package hermetic

import (
	"os"
	"strings"
)

// DropInheritedGit removes every GIT_* variable from the environment of the
// running process. Call it as the first statement of TestMain, before m.Run:
// a test that sets one of them afterwards (t.Setenv) does so on purpose.
//
// It panics if a variable cannot be removed: a test binary that carries on
// with a hook's GIT_DIR is the defect this exists to stop, and a panic in
// TestMain fails the whole package rather than letting one test write.
func DropInheritedGit() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") {
			if err := os.Unsetenv(name); err != nil {
				panic("hermetic: cannot drop " + name + ": " + err.Error())
			}
		}
	}
}
