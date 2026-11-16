package ci

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// throwawayRepo is the one way a test under tools/ writes with git: it owns a
// repository in a directory the test chose, and every git it runs, and every
// script the test runs against it, sees an environment that names nothing but
// that repository.
//
// Why it exists, measured twice. Inside a git hook, git exports GIT_DIR,
// GIT_INDEX_FILE and friends, and a child git obeys them before it looks at its
// working directory. A test that sets only cmd.Dir is then writing into the
// repository being pushed:
//
//   - preflight_milestones_test.go passed standalone and failed under the
//     pre-push hook, its throwaway repository pointing at the real one;
//   - TestTheTestplanCheckHandsBackItsOwnExitCode re-wrote the same harness
//     without the fix, and on 2026-11-15 its `git add README.md` landed in the
//     index of the real worktree from inside the pre-push hook. Only the
//     project's pre-commit hook stopped the `git commit` that followed.
//     Exported towards a throwaway decoy instead, the same test exited 0 and
//     left a second commit in the decoy: green, and wrong.
//
// TestAThrowawayRepoIgnoresTheGitDirAHookExports fails without the scrubbing
// below, and TestNoTestWritesThroughARawGitCommand refuses a third copy of the
// harness written without it.
//
// Since #955 every test binary that starts a process also drops GIT_* in its
// TestMain (hermetic_test.go), so a hook's variables are gone before this runs.
// The scrubbing here stays: a test may set GIT_DIR itself after TestMain
// (TestAThrowawayRepoIgnoresTheGitDirAHookExports does, to play the hook), and
// the global configuration below is not a GIT_* a hook exports.
type throwawayRepo struct {
	t   *testing.T
	Dir string
	env []string
}

// hermeticGitEnv is the parent environment without any GIT_* or PRE_COMMIT*
// variable, and without the operator's global and system git configuration:
// a global core.hooksPath would run the operator's hooks in a repository whose
// template was emptied for exactly that reason, and a global commit.gpgsign
// would ask for a key. The identity is fixed so that a commit never depends on
// who runs the test.
func hermeticGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "PRE_COMMIT") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
}

// newThrowawayRepo creates dir if needed and initialises an empty repository
// there, on branch main, from an empty template so that no hook is copied in.
func newThrowawayRepo(t *testing.T, dir string) *throwawayRepo {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &throwawayRepo{t: t, Dir: dir, env: hermeticGitEnv()}
	r.Git("-c", "init.templateDir=", "init", "--quiet", "-b", "main")
	return r
}

// Git runs git in the repository and returns its combined output; a failure
// stops the test, since nothing after it would be measuring what it says.
func (r *throwawayRepo) Git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = r.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v in %s: %v\n%s", args, r.Dir, err, out)
	}
	return string(out)
}

// Env is the environment a script run against this repository must get. A
// script that calls git itself (tools/testplan/check.sh does) obeys an
// inherited GIT_DIR exactly like the test would.
func (r *throwawayRepo) Env() []string {
	return slices.Clone(r.env)
}

// TestAThrowawayRepoIgnoresTheGitDirAHookExports reproduces a hook: GIT_DIR
// and GIT_INDEX_FILE point at a decoy repository, the very pair measured on
// 2026-11-15, and a throwaway repository is then written to. The decoy's index
// must come out byte-identical and its history unchanged, and a script run
// with Env() must find the throwaway repository, not the decoy.
//
// The decoy is itself a throwaway repository under t.TempDir(): this test
// never points those variables at a tree anybody works in.
func TestAThrowawayRepoIgnoresTheGitDirAHookExports(t *testing.T) {
	// The decoy's directory is joined from t.TempDir() directly, not read back
	// from decoy.Dir: that is the shape the Setenv guard accepts (#958).
	decoyDir := t.TempDir()
	decoy := newThrowawayRepo(t, decoyDir)
	if err := os.WriteFile(filepath.Join(decoy.Dir, "DECOY.md"), []byte("decoy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	decoy.Git("add", "DECOY.md")
	decoy.Git("commit", "--quiet", "-m", "decoy")
	index := filepath.Join(decoyDir, ".git", "index")
	indexBefore, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := strings.TrimSpace(decoy.Git("rev-parse", "HEAD"))

	t.Setenv("GIT_DIR", filepath.Join(decoyDir, ".git"))
	t.Setenv("GIT_INDEX_FILE", index)

	repo := newThrowawayRepo(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(repo.Dir, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo.Git("add", "README.md")
	repo.Git("commit", "--quiet", "-m", "base")

	indexAfter, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexBefore, indexAfter) {
		t.Errorf("the decoy's index changed: the throwaway repository wrote into the repository GIT_INDEX_FILE named")
	}
	if headAfter := strings.TrimSpace(decoy.Git("rev-parse", "HEAD")); headAfter != headBefore {
		t.Errorf("the decoy's HEAD moved from %s to %s: the throwaway commit landed in the repository GIT_DIR named", headBefore, headAfter)
	}
	if got := strings.TrimSpace(repo.Git("ls-files")); got != "README.md" {
		t.Errorf("the throwaway repository tracks %q, want README.md alone", got)
	}

	script := exec.Command("bash", "--noprofile", "--norc", "-c", "git rev-parse --show-toplevel")
	script.Dir = repo.Dir
	script.Env = repo.Env()
	out, err := script.CombinedOutput()
	if err != nil {
		t.Fatalf("a script run with Env() could not read its repository: %v\n%s", err, out)
	}
	want, err := filepath.EvalSymlinks(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("a script run with Env() found %q, want the throwaway repository %q", got, want)
	}
}

// The shared helper, as the static guard below recognises it.
const (
	throwawayHelperFile = "tools/ci/throwaway_git_test.go"
	throwawayHelperFunc = "(*throwawayRepo).Git"
)

// gitReads are the subcommands a test may run through a bare exec.Command,
// because none of them writes to a repository. `status` is deliberately
// absent: it refreshes the index when it can. A subcommand outside this list
// is refused, not guessed at.
var gitReads = []string{"cat-file", "log", "ls-files", "ls-tree", "rev-list", "rev-parse", "show"}

// rawGitFindings parses one Go file and returns a refusal for every call to
// os/exec's Command or CommandContext whose program is the literal "git",
// unless it is the shared helper's own call, or a read whose every argument is
// a string literal and whose first argument is one of gitReads. accepted
// counts the calls it let through, so that a caller can prove the scan saw
// something.
//
// Which repository a read lists is no longer this guard's question (#955): it
// asked whether the read's Env came from a function named
// withoutInheritedGit, matched by name and blind to what the function did.
// The hermetic default answers it at the source for every call of the binary,
// and TestEveryTestBinaryThatStartsAProcessDropsInheritedGit holds that
// default. What a hermetic environment cannot stop is a write aimed at the
// tree the test runs in, by a cmd.Dir left unset or pointed at it: that is
// what is left here.
func rawGitFindings(path string, src []byte) (refused []string, accepted int, err error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, 0, err
	}
	execNames := map[string]bool{}
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
			name := "exec"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			execNames[name] = true
		}
	}
	if len(execNames) == 0 {
		return nil, 0, nil
	}
	for _, decl := range file.Decls {
		fn, _ := decl.(*ast.FuncDecl)
		enclosing := ""
		if fn != nil {
			enclosing = funcName(fn)
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || !execNames[pkg.Name] {
				return true
			}
			var args []ast.Expr
			switch sel.Sel.Name {
			case "Command":
				args = call.Args
			case "CommandContext":
				if len(call.Args) > 0 {
					args = call.Args[1:]
				}
			default:
				return true
			}
			if len(args) == 0 || stringLit(args[0]) != "git" {
				return true
			}
			where := fmt.Sprintf("%s:%d", path, fset.Position(call.Pos()).Line)
			if path == throwawayHelperFile && enclosing == throwawayHelperFunc {
				accepted++
				return true
			}
			rest := args[1:]
			if call.Ellipsis.IsValid() {
				refused = append(refused, where+": git arguments spread from a slice cannot be read, so whether it writes cannot be said; use newThrowawayRepo")
				return true
			}
			literal := make([]string, 0, len(rest))
			for _, a := range rest {
				s, isLit := stringLitOK(a)
				if !isLit {
					refused = append(refused, where+": git arguments are not all string literals, so whether it writes cannot be said; use newThrowawayRepo")
					return true
				}
				literal = append(literal, s)
			}
			switch {
			case len(literal) == 0:
				refused = append(refused, where+": git with no subcommand is not a known read; use newThrowawayRepo")
			case strings.HasPrefix(literal[0], "-"):
				refused = append(refused, where+": git takes the option "+strconv.Quote(literal[0])+" before its subcommand, which can redirect a read to another repository; use newThrowawayRepo")
			case !slices.Contains(gitReads, literal[0]):
				refused = append(refused, where+": git "+strconv.Quote(literal[0])+" is not a known read; use newThrowawayRepo")
			default:
				accepted++
			}
			return true
		})
	}
	return refused, accepted, nil
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return "(*" + id.Name + ")." + fn.Name.Name
		}
	case *ast.Ident:
		return "(" + t.Name + ")." + fn.Name.Name
	}
	return "?." + fn.Name.Name
}

func stringLitOK(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

func stringLit(e ast.Expr) string {
	s, _ := stringLitOK(e)
	return s
}

// TestNoTestWritesThroughARawGitCommand walks every _test.go of the
// repository and refuses a git that does not go through newThrowawayRepo,
// unless it is a literal read (gitReads). The third copy of the harness,
// written without the scrubbing, is what this exists to stop.
//
// Before #955 this guard read tools/ only, under a narrower name. A
// read used to be accepted only when its Env came from a function named
// withoutInheritedGit; that question moved to the source (#955): every test
// binary that starts a process drops GIT_* in its TestMain, proved by
// TestATestBinaryDropsTheGitDirAHookExports and held by
// TestEveryTestBinaryThatStartsAProcessDropsInheritedGit. What the hermetic
// default does not stop is a write into the tree the test runs in, which is
// this guard's remaining question.
//
// What this does not see: a program name held in a variable, a git run by a
// shell script or `bash -c`, and Python. Against a hook's GIT_DIR those are
// covered by the hermetic default (falsify.py drops GIT_* when it starts, its
// selftest proves it); against a write aimed at the working tree itself, they
// are not covered by anything, and this guard says so rather than pretending.
func TestNoTestWritesThroughARawGitCommand(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	files, accepted := 0, 0
	var refused []string
	sawHelper := false
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (name == "testdata" || name == "node_modules" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		r, a, err := rawGitFindings(rel, src)
		if err != nil {
			return err
		}
		files++
		accepted += a
		refused = append(refused, r...)
		if rel == throwawayHelperFile && a > 0 {
			sawHelper = true
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("the walk of the repository failed, so nothing was judged: %v", walkErr)
	}
	// A scan that found nothing cannot tell a clean tree from a scan that
	// looked nowhere: the helper's own call is the witness it must find.
	if files == 0 || !sawHelper {
		t.Fatalf("the scan read %d test files and did not find the helper's own git call in %s: it cannot judge this tree", files, throwawayHelperFile)
	}
	for _, r := range refused {
		t.Error(r)
	}
	t.Logf("%d test files, %d git calls accepted, %d refused", files, accepted, len(refused))
}

// TestTheRawGitGuardAsksEachQuestion: one case per question the guard asks,
// each passing every other question and failing only its own, with the reason
// asserted, since one refusal answers several questions.
func TestTheRawGitGuardAsksEachQuestion(t *testing.T) {
	const imports = "package x\n\nimport (\n\t\"context\"\n\t\"os/exec\"\n)\n\nvar _ = context.Background\n\n"
	// scrubbed wraps one call in a function, so that a case differs from the
	// next by the call alone.
	scrubbed := func(call string) string {
		return "func f(sub string) { cmd := " + call + "; _, _ = cmd, sub }\n"
	}
	helperBody := "type throwawayRepo struct{}\n\nfunc (r *throwawayRepo) Git(args ...string) { _ = exec.Command(\"git\", args...) }\n"
	for _, tc := range []struct {
		name, path, src string
		accepted        int
		reason          string // empty: no refusal expected
	}{
		{"the helper's own call is accepted", throwawayHelperFile, imports + helperBody, 1, ""},
		{"the same call in another file is refused", "tools/x/y_test.go", imports + helperBody, 0, "spread from a slice"},
		{"the same call under another method name is refused", throwawayHelperFile, imports + strings.Replace(helperBody, ") Git(", ") Run(", 1), 0, "spread from a slice"},
		{"a scrubbed literal read is accepted", "tools/x/y_test.go", imports + scrubbed(`exec.Command("git", "ls-files")`), 1, ""},
		{"a read bound to no variable is accepted, its environment is the binary's", "internal/x/y_test.go", imports + "func f() { _, _ = exec.Command(\"git\", \"ls-files\").Output() }\n", 1, ""},
		{"a literal write is refused", "tools/x/y_test.go", imports + scrubbed(`exec.Command("git", "add", "README.md")`), 0, "\"add\" is not a known read"},
		{"an unknown subcommand is refused, not guessed at", "tools/x/y_test.go", imports + scrubbed(`exec.Command("git", "frobnicate")`), 0, "\"frobnicate\" is not a known read"},
		{"a read whose subcommand is a variable is refused", "tools/x/y_test.go", imports + scrubbed(`exec.Command("git", sub)`), 0, "not all string literals"},
		{"an option before a read is refused", "tools/x/y_test.go", imports + scrubbed(`exec.Command("git", "-C", "/elsewhere", "ls-files")`), 0, "option \"-C\" before its subcommand"},
		{"git with no argument is refused", "tools/x/y_test.go", imports + scrubbed(`exec.Command("git")`), 0, "no subcommand"},
		{"CommandContext is judged like Command", "tools/x/y_test.go", imports + scrubbed(`exec.CommandContext(context.Background(), "git", "commit")`), 0, "\"commit\" is not a known read"},
		{"an aliased os/exec is judged like exec", "tools/x/y_test.go", "package x\n\nimport osexec \"os/exec\"\n\nfunc f() { cmd := osexec.Command(\"git\", \"init\"); _ = cmd }\n", 0, "\"init\" is not a known read"},
		{"a write outside tools/ is refused too", "internal/x/y_test.go", imports + scrubbed(`exec.Command("git", "commit", "-m", "x")`), 0, "\"commit\" is not a known read"},
		{"another program is not the guard's business", "tools/x/y_test.go", imports + "func f() { _ = exec.Command(\"bash\", \"-c\", \"true\") }\n", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refused, accepted, err := rawGitFindings(tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("the case does not parse, so it measures nothing: %v", err)
			}
			if accepted != tc.accepted {
				t.Errorf("accepted %d calls, want %d", accepted, tc.accepted)
			}
			switch {
			case tc.reason == "" && len(refused) != 0:
				t.Errorf("refused %q, want no refusal", refused)
			case tc.reason != "" && (len(refused) != 1 || !strings.Contains(refused[0], tc.reason)):
				t.Errorf("refused %q, want exactly one refusal saying %q", refused, tc.reason)
			}
		})
	}
}
