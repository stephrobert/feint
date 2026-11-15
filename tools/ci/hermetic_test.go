package ci

import (
	"bytes"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/hermetic"
)

// TestMain drops every GIT_* variable before any test of this package runs
// (#955). newThrowawayRepo scrubs the environment of the git it runs itself;
// this protects what it cannot see: a script a test calls, a program name in a
// variable, python3 running falsify.py. TestATestBinaryDropsTheGitDirAHookExports
// fails without the call, and TestEveryTestBinaryThatStartsAProcessDropsInheritedGit
// refuses this file without it.
func TestMain(m *testing.M) {
	hermetic.DropInheritedGit()
	os.Exit(m.Run())
}

// hermeticChild is set by TestATestBinaryDropsTheGitDirAHookExports on the run
// of this very binary it starts; it names the directory the child writes in.
const hermeticChild = "FEINT_HERMETIC_CHILD_DIR"

// hermeticScript is the forgotten helper: a shell script that runs git with
// whatever environment it inherits, which no static guard of this repository
// can read. The configuration it needs is set inside the script, so that it
// survives the scrubbing it is there to measure, and none of it names a
// repository.
const hermeticScript = `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0
git -c init.templateDir= init --quiet -b main
printf 'child\n' > CHILD.md
git add CHILD.md
git -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false -c core.hooksPath=/dev/null commit --quiet -m child
`

// TestHelperHermeticChild is not a test on its own: it is what
// TestATestBinaryDropsTheGitDirAHookExports runs inside a second copy of this
// binary, started with a hook's environment. It runs hermeticScript with the
// environment the child process holds after its TestMain.
func TestHelperHermeticChild(t *testing.T) {
	dir := os.Getenv(hermeticChild)
	if dir == "" {
		t.Skip("run only as the child of TestATestBinaryDropsTheGitDirAHookExports")
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-c", hermeticScript)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the script failed in %s: %v\n%s", dir, err, out)
	}
}

// TestATestBinaryDropsTheGitDirAHookExports reproduces the 2026-11-14 defect
// end to end. A decoy repository is named by GIT_DIR and GIT_INDEX_FILE, the
// pair a hook exports, and this binary is started again with that
// environment; inside it, a script runs `git init`, `git add` and `git commit`
// in a directory of its own and inherits everything. The decoy must come out
// byte-identical, and the child's directory must hold the commit.
//
// The witness comes first: the same script, run outside the test binary with
// the same environment, must land in a second decoy. A decoy git ignored would
// leave the verdict below green for the wrong reason.
//
// Both decoys are throwaway repositories under t.TempDir(): this test never
// points those variables at a tree anybody works in.
func TestATestBinaryDropsTheGitDirAHookExports(t *testing.T) {
	if os.Getenv(hermeticChild) != "" {
		t.Skip("this is the child run")
	}
	decoy := func() (*throwawayRepo, []string, func() ([]byte, string)) {
		d := newThrowawayRepo(t, t.TempDir())
		if err := os.WriteFile(filepath.Join(d.Dir, "DECOY.md"), []byte("decoy\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		d.Git("add", "DECOY.md")
		d.Git("commit", "--quiet", "-m", "decoy")
		gitDir := filepath.Join(d.Dir, ".git")
		env := append(os.Environ(), "GIT_DIR="+gitDir, "GIT_INDEX_FILE="+filepath.Join(gitDir, "index"))
		state := func() ([]byte, string) {
			index, err := os.ReadFile(filepath.Join(gitDir, "index"))
			if err != nil {
				t.Fatal(err)
			}
			return index, strings.TrimSpace(d.Git("rev-parse", "HEAD"))
		}
		return d, env, state
	}

	_, witnessEnv, witnessState := decoy()
	indexBefore, headBefore := witnessState()
	script := exec.Command("bash", "--noprofile", "--norc", "-c", hermeticScript)
	script.Dir = t.TempDir()
	script.Env = witnessEnv
	if out, err := script.CombinedOutput(); err != nil {
		t.Fatalf("the witness script failed, so the decoy was never shown to mislead: %v\n%s", err, out)
	}
	if indexAfter, headAfter := witnessState(); bytes.Equal(indexBefore, indexAfter) && headAfter == headBefore {
		t.Fatal("the script run with a hook's GIT_DIR left the witness decoy untouched, so the decoy misleads nothing and this test cannot measure the scrubbing")
	}

	_, hookEnv, state := decoy()
	indexBefore, headBefore = state()
	target := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(self, "-test.run=^TestHelperHermeticChild$", "-test.count=1", "-test.v")
	child.Env = append(hookEnv, hermeticChild+"="+target)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("the child run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestHelperHermeticChild") {
		t.Fatalf("the child run did not pass its helper (skipped, or never entered), so nothing was measured:\n%s", out)
	}
	indexAfter, headAfter := state()
	if !bytes.Equal(indexBefore, indexAfter) {
		t.Error("the decoy's index changed: the test binary kept the GIT_INDEX_FILE a hook exported, and a script it ran wrote there")
	}
	if headAfter != headBefore {
		t.Errorf("the decoy's HEAD moved from %s to %s: the test binary kept the GIT_DIR a hook exported", headBefore, headAfter)
	}
	// The accepting half: the script did write, in the directory it was run in.
	own := &throwawayRepo{t: t, Dir: target, env: hermeticGitEnv()}
	if got := strings.TrimSpace(own.Git("ls-files")); got != "CHILD.md" {
		t.Errorf("the child's own repository tracks %q, want CHILD.md: the script wrote somewhere else", got)
	}
}

// hermeticImport is the package every TestMain of the repository must call
// first. It is matched by import path, not by name: a local function called
// DropInheritedGit is not this one.
const hermeticImport = "github.com/stephrobert/feint/internal/hermetic"

// hermeticFindings judges one directory from every .go file in it, test or
// not, keyed by base name. Only the files the default build compiles are read
// (build.Context.MatchFile): a TestMain behind `//go:build x` is not in the
// binary `go test` builds, and reading it anyway would accept a package whose
// default test binary has no TestMain at all.
//
// judged is false when no compiled file is a test file: the directory builds no
// test binary. Its other files are still read for the last question, because
// another package's test binary may link them.
//
// Every test binary is judged, not only one whose test files import os/exec
// (the rule until a falsifier measured the hole on 2026-11-15): a test that
// calls a function outside its _test.go files, and that function runs git,
// imports nothing the old rule could see, and the decoy received the commit.
// "Every test binary that links os/exec" was the proposed replacement, and it
// is the same set: measured on 2026-11-15, `go list -e -test -deps ./...`
// gives os/exec in the dependencies of 36 test binaries out of 36, because
// testing/internal/testdeps links internal/fuzz, which imports it. Judging
// every binary needs no `go list` and also covers os.StartProcess and
// syscall.Exec, which the import rule could not see.
//
// refusal says which question failed.
func hermeticFindings(dir string, files map[string][]byte) (judged bool, refusal string, err error) {
	ctx := build.Default
	ctx.OpenFile = func(path string) (io.ReadCloser, error) {
		src, ok := files[filepath.Base(path)]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return io.NopCloser(bytes.NewReader(src)), nil
	}
	var main *ast.FuncDecl
	mainAlias, early := "", ""
	for _, name := range slices.Sorted(maps.Keys(files)) {
		ok, merr := ctx.MatchFile(dir, name)
		if merr != nil {
			return false, "", merr
		}
		if !ok {
			continue
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), files[name], parser.SkipObjectResolution)
		if perr != nil {
			return false, "", perr
		}
		alias := importNames(file)
		if strings.HasSuffix(name, "_test.go") {
			judged = true
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "TestMain" {
					main, mainAlias = fn, alias[hermeticImport]
				}
			}
		}
		if what := readsEnvironmentEarly(file, alias); what != "" && early == "" {
			early = name + " calls " + what
		}
	}
	if judged {
		if main == nil {
			return true, dir + ": its test binary has no TestMain in the default build (a TestMain behind a build constraint is not in it), so every git its tests start, directly or through any function they call, obeys a hook's GIT_DIR; add a TestMain whose first statement is hermetic.DropInheritedGit()", nil
		}
		var call *ast.CallExpr
		if main.Body != nil && len(main.Body.List) > 0 {
			if stmt, ok := main.Body.List[0].(*ast.ExprStmt); ok {
				call, _ = stmt.X.(*ast.CallExpr)
			}
		}
		callee := ""
		if call != nil && len(call.Args) == 0 {
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := f.X.(*ast.Ident); ok && f.Sel.Name == "DropInheritedGit" {
					callee = x.Name
				}
			case *ast.Ident:
				if f.Name == "DropInheritedGit" {
					callee = "."
				}
			}
		}
		switch {
		case callee == "":
			return true, dir + ": TestMain does not call hermetic.DropInheritedGit() as its first statement, so whatever runs before it, m.Run included, sees a hook's GIT_DIR", nil
		case mainAlias == "" || callee != mainAlias:
			return true, dir + ": TestMain's first statement calls a DropInheritedGit that is not " + strconv.Quote(hermeticImport) + "'s, so what it drops is nobody's promise", nil
		}
	}
	if early != "" {
		return judged, dir + ": " + early + " while the package initialises, before any TestMain runs, so the copy keeps a hook's GIT_DIR whatever TestMain drops afterwards; read the environment inside a function", nil
	}
	return judged, "", nil
}

// importNames maps each import path of file to the name it is used under,
// leaving out blank and dot imports. The default name is the last element of
// the path, which is right for every package this guard asks about.
func importNames(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		switch {
		case imp.Name == nil:
			out[p] = p[strings.LastIndex(p, "/")+1:]
		case imp.Name.Name != "_" && imp.Name.Name != ".":
			out[p] = imp.Name.Name
		}
	}
	return out
}

// readsEnvironmentEarly names a read of the environment that runs while the
// package initialises, which is before any TestMain: in the value of a
// package-level var, or in an init function. A function literal only runs when
// it is called, so its body is read only when it is called on the spot.
// Empty when there is none.
func readsEnvironmentEarly(file *ast.File, alias map[string]string) string {
	found := ""
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		if found != "" {
			return false
		}
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if lit, ok := x.Fun.(*ast.FuncLit); ok {
				ast.Inspect(lit.Body, visit)
				for _, arg := range x.Args {
					ast.Inspect(arg, visit)
				}
				return false
			}
			found = environmentRead(x, alias)
		}
		return found == ""
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.VAR {
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, v := range vs.Values {
							ast.Inspect(v, visit)
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == "init" && d.Body != nil {
				ast.Inspect(d.Body, visit)
			}
		}
	}
	return found
}

// environmentRead names call when it reads what a hook exports: the whole
// environment (os.Environ, syscall.Environ), or a GIT_* variable by name.
func environmentRead(call *ast.CallExpr, alias map[string]string) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	for _, pkg := range []string{"os", "syscall"} {
		if alias[pkg] == "" || x.Name != alias[pkg] {
			continue
		}
		switch sel.Sel.Name {
		case "Environ":
			return pkg + ".Environ()"
		case "Getenv", "LookupEnv":
			if len(call.Args) == 1 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(v, "GIT_") {
						return pkg + "." + sel.Sel.Name + "(" + lit.Value + ")"
					}
				}
			}
		}
	}
	return ""
}

// TestEveryTestBinaryThatStartsAProcessDropsInheritedGit walks every package
// of the repository and refuses a test binary whose TestMain does not drop
// GIT_* first, or a package that reads the environment before any TestMain
// can. Every test binary can start a process (see hermeticFindings), so every
// one is judged: that is what makes the hermetic default hold for a test
// written tomorrow, in a package that runs no git today.
//
// What this does not see, said rather than implied:
//   - a test that sets GIT_DIR itself after TestMain. Two do, on purpose:
//     tools/testplan/tracked_hook_test.go and tools/ci/throwaway_git_test.go
//     play a hook with t.Setenv. What protects them is not this guard: the
//     values name a decoy repository each test creates under t.TempDir(),
//     t.Setenv refuses a parallel test so no other test of the binary runs
//     while they are set, the testing package restores them when the test
//     ends, and each test asserts the decoy came out unchanged. Nothing
//     static refuses a t.Setenv("GIT_DIR", ...) aimed at a real tree.
//   - a read of the environment while a standard-library package initialises;
//   - a test binary built with a build tag (`go test -tags x`), which can
//     hold a different TestMain: the default build is the one judged.
func TestEveryTestBinaryThatStartsAProcessDropsInheritedGit(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]map[string][]byte{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if dirs[rel] == nil {
			dirs[rel] = map[string][]byte{}
		}
		dirs[rel][filepath.Base(path)] = src
		return nil
	})
	if walkErr != nil {
		t.Fatalf("the walk of the repository failed, so nothing was judged: %v", walkErr)
	}
	var judged []string
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		isTest, refusal, err := hermeticFindings(dir, dirs[dir])
		if err != nil {
			t.Fatalf("%s does not parse, so it was not judged: %v", dir, err)
		}
		if isTest {
			judged = append(judged, dir)
		}
		if refusal != "" {
			t.Error(refusal)
		}
	}
	// A scan that judged nothing cannot tell a clean tree from a scan that
	// looked nowhere: this package has tests, so it is the witness.
	if !slices.Contains(judged, "tools/ci") {
		t.Fatalf("the scan judged %d test binaries (%v) and not tools/ci, which has tests: it cannot judge this tree", len(judged), judged)
	}
	t.Logf("%d directories read, %d of them build a test binary and were judged: %v", len(dirs), len(judged), judged)
}

// TestTheHermeticGuardAsksEachQuestion: one case per question, each passing
// every other question and failing only its own, with the reason asserted.
// The three holes a falsifier measured on 2026-11-15 have a case each: a test
// that starts git only through a function outside its test files, a TestMain
// behind a build constraint, and the environment copied at package level.
func TestTheHermeticGuardAsksEachQuestion(t *testing.T) {
	const execImport = "\t\"os/exec\"\n"
	const hermeticImp = "\t\"" + hermeticImport + "\"\n"
	file := func(imports, body string) []byte {
		return []byte("package x\n\nimport (\n\t\"os\"\n\t\"testing\"\n" + imports + ")\n\nvar _ = os.Exit\n\n" + body)
	}
	const good = "func TestMain(m *testing.M) {\n\thermetic.DropInheritedGit()\n\tos.Exit(m.Run())\n}\n"
	const usesExec = "var _ = exec.Command\n\nfunc TestX(t *testing.T) {}\n"
	const noExec = "func TestX(t *testing.T) { runGit() }\n"
	library := []byte("package x\n\nimport \"os/exec\"\n\nfunc runGit() { _ = exec.Command(\"git\", \"commit\").Run() }\n")
	const noTestMain = "has no TestMain in the default build"
	const notFirst = "as its first statement"
	const notOurs = "is not \"" + hermeticImport + "\"'s"
	const early = "while the package initialises"
	for _, tc := range []struct {
		name   string
		files  map[string][]byte
		judged bool
		reason string // empty: no refusal expected
	}{
		{"a test binary that drops GIT_* first is accepted", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+good)}, true, ""},
		{"the TestMain may live in a sibling file of the same binary", map[string][]byte{
			"a_test.go": file(execImport, usesExec),
			"m_test.go": file(hermeticImp, good),
		}, true, ""},
		{"an aliased import of the package is accepted", map[string][]byte{"a_test.go": file(execImport+"\th \""+hermeticImport+"\"\n", usesExec+strings.Replace(good, "hermetic.", "h.", 1))}, true, ""},
		{"a directory with no test file builds no test binary", map[string][]byte{"lib.go": library}, false, ""},
		{"a test that starts git only through a function outside its test files is refused", map[string][]byte{
			"lib.go":    library,
			"a_test.go": file("", noExec),
		}, true, noTestMain},
		{"a test binary without a TestMain is refused", map[string][]byte{"a_test.go": file(execImport, usesExec)}, true, noTestMain},
		{"a TestMain behind a build constraint is not in the default binary", map[string][]byte{
			"a_test.go": file(execImport, usesExec),
			"m_test.go": append([]byte("//go:build hermeticselftest\n\n"), file(hermeticImp, good)...),
		}, true, noTestMain},
		{"a TestMain that runs the tests before dropping is refused", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+"func TestMain(m *testing.M) {\n\tcode := m.Run()\n\thermetic.DropInheritedGit()\n\tos.Exit(code)\n}\n")}, true, notFirst},
		{"a TestMain that drops nothing is refused", map[string][]byte{"a_test.go": file(execImport, usesExec+"func TestMain(m *testing.M) {\n\tos.Exit(m.Run())\n}\n")}, true, notFirst},
		{"a local function of the same name is not the package's", map[string][]byte{"a_test.go": file(execImport, usesExec+"func DropInheritedGit() {}\n\nfunc TestMain(m *testing.M) {\n\tDropInheritedGit()\n\tos.Exit(m.Run())\n}\n")}, true, notOurs},
		{"a local function of the same name is not the package's even when the package is imported", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+"var _ = hermetic.DropInheritedGit\n\nfunc DropInheritedGit() {}\n\nfunc TestMain(m *testing.M) {\n\tDropInheritedGit()\n\tos.Exit(m.Run())\n}\n")}, true, notOurs},
		{"another package's function of the same name is not the package's", map[string][]byte{"a_test.go": file(execImport+"\thermetic \"example.invalid/hermetic\"\n", usesExec+good)}, true, notOurs},
		{"the environment copied at package level, before TestMain, is refused", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+"var inherited = os.Environ()\n\n"+good)}, true, early},
		{"a GIT_* variable read by an init function outside the test files is refused", map[string][]byte{
			"lib.go":    []byte("package x\n\nimport \"os\"\n\nvar gitDir string\n\nfunc init() { gitDir = os.Getenv(\"GIT_DIR\") }\n"),
			"a_test.go": file(execImport+hermeticImp, usesExec+good),
		}, true, early},
		{"a read that runs only when called, or of another variable, is accepted", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+"var environ = func() []string { return os.Environ() }\n\nvar home = os.Getenv(\"HOME\")\n\n"+good)}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			judged, refusal, err := hermeticFindings("tools/x", tc.files)
			if err != nil {
				t.Fatalf("the case does not parse, so it measures nothing: %v", err)
			}
			if judged != tc.judged {
				t.Errorf("judged = %v, want %v", judged, tc.judged)
			}
			switch {
			case tc.reason == "" && refusal != "":
				t.Errorf("refused %q, want no refusal", refusal)
			case tc.reason != "" && !strings.Contains(refusal, tc.reason):
				t.Errorf("refused %q, want a refusal saying %q", refusal, tc.reason)
			}
		})
	}
}
