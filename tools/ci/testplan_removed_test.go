package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheTestplanAsksForTheSpecADeletedTestWasJudgedBy drives tools/testplan
// end to end on a throwaway repository: a spec names the fixture's one test in ./a/,
// and the file declaring it is committed, then taken out of a/. testplan's
// changed() leaves a deleted path out on purpose, so the plan asks for the
// spec only if removedTests() reads the deletion and main hands it to the
// test link (#965).
//
// Three ways out of a/, one case each: deleted in the working tree, moved and
// staged (git reports a staged move as R unless told --no-renames, so the
// uncommitted call is judged), and moved in a commit on a branch (the range
// call against main is judged). The accepting half comes first in every case:
// with nothing changed, the plan asks for nothing.
func TestTheTestplanAsksForTheSpecADeletedTestWasJudgedBy(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "tools", "testplan", "check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const spec = "mise run falsify -- tools/falsify/specs/s.json"

	cases := []struct {
		name string
		out  func(t *testing.T, repo *throwawayRepo)
	}{
		{"deleted_in_the_working_tree", func(t *testing.T, repo *throwawayRepo) {
			if err := os.Remove(filepath.Join(repo.Dir, "a", "guard_test.go")); err != nil {
				t.Fatal(err)
			}
		}},
		{"moved_and_staged", func(t *testing.T, repo *throwawayRepo) {
			if err := os.MkdirAll(filepath.Join(repo.Dir, "b"), 0o700); err != nil {
				t.Fatal(err)
			}
			repo.Git("mv", "a/guard_test.go", "b/guard_test.go")
		}},
		{"moved_in_a_commit_on_a_branch", func(t *testing.T, repo *throwawayRepo) {
			if err := os.MkdirAll(filepath.Join(repo.Dir, "b"), 0o700); err != nil {
				t.Fatal(err)
			}
			repo.Git("checkout", "-q", "-b", "feature")
			repo.Git("mv", "a/guard_test.go", "b/guard_test.go")
			repo.Git("commit", "-q", "-m", "move")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newThrowawayRepo(t, t.TempDir())
			for p, body := range map[string]string{
				"tools/falsify/specs/s.json": `{"package":"./a/","mutations":[{"file":"a/guard.go","find":"x","replace":"y","test":"TestGuardHolds"}]}`,
				"a/guard.go":                 "package a\n",
				"a/guard_test.go":            "package a\n\nimport \"testing\"\n\nfunc TestGuardHolds(t *testing.T) {}\n",
			} {
				full := filepath.Join(repo.Dir, filepath.FromSlash(p))
				if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			repo.Git("add", "-A")
			repo.Git("commit", "-q", "-m", "base")

			run := func() string {
				t.Helper()
				cmd := exec.Command("bash", "--noprofile", "--norc", script)
				cmd.Dir = repo.Dir
				cmd.Env = repo.Env()
				out, runErr := cmd.CombinedOutput()
				// 2 is `--check` naming a path no rule of this repository
				// triages (the move lands in b/, which no rule names); the plan
				// is printed above it. Anything else means no plan was made.
				var exit *exec.ExitError
				if runErr != nil && (!errors.As(runErr, &exit) || exit.ExitCode() != 2) {
					t.Fatalf("check.sh failed, so no plan was read: %v\n%s", runErr, out)
				}
				return string(out)
			}

			if out := run(); !strings.Contains(out, "Nothing to run") || strings.Contains(out, spec) {
				t.Fatalf("nothing changed, and the plan did not say nothing to run:\n%s", out)
			}
			tc.out(t, repo)
			out := run()
			if !strings.Contains(out, spec) || !strings.Contains(out, "changed or was removed") {
				t.Fatalf("the test file the spec's test lived in left a/, and the spec was not earned:\n%s", out)
			}
		})
	}
}
