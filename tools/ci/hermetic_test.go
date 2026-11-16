package ci

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		// The git directory is joined from t.TempDir() directly, not read
		// back from d.Dir: the shape the environment-entry guard accepts (#958).
		dir := t.TempDir()
		d := newThrowawayRepo(t, dir)
		if err := os.WriteFile(filepath.Join(d.Dir, "DECOY.md"), []byte("decoy\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		d.Git("add", "DECOY.md")
		d.Git("commit", "--quiet", "-m", "decoy")
		gitDir := filepath.Join(dir, ".git")
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
// not, keyed by base name, as the build named by tags compiles them (nil: the
// default build). Only the files that build compiles are read
// (build.Context.MatchFile): a TestMain behind `//go:build x` is not in the
// binary `go test` builds, and reading it anyway would accept a package whose
// default test binary has no TestMain at all. The converse is why the walk
// judges every tag set the repository uses (#958): `go test -tags x` can
// leave out the file that holds the TestMain the default build has.
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
func hermeticFindings(dir string, files map[string][]byte, tags []string) (judged bool, refusal string, err error) {
	ctx := buildContext(files, tags)
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
			return true, dir + ": its test binary has no TestMain in the " + buildName(tags) + " (a TestMain behind a build constraint is not in it), so every git its tests start, directly or through any function they call, obeys a hook's GIT_DIR; add a TestMain whose first statement is hermetic.DropInheritedGit()", nil
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

// buildName names the build a refusal is about, so that one reported under
// -tags conformance is not read as one about the default build.
func buildName(tags []string) string {
	if len(tags) == 0 {
		return "default build"
	}
	return "build with -tags " + strings.Join(tags, ",")
}

// buildContext is the default build of this platform with tags added, reading
// files from memory: what `go test -tags <tags>` would compile in one
// directory. A nil tags is the default build.
func buildContext(files map[string][]byte, tags []string) build.Context {
	ctx := build.Default
	ctx.BuildTags = slices.Clone(tags)
	ctx.OpenFile = func(path string) (io.ReadCloser, error) {
		src, ok := files[filepath.Base(path)]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return io.NopCloser(bytes.NewReader(src)), nil
	}
	return ctx
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
// It judges once per build-tag set the repository uses (#958): the default
// build, and every tag a `//go:build` line of the tree names, so a binary
// built with `go test -tags conformance` is judged as it is compiled. The
// same walk refuses a test that sets a GIT_* variable to a value not taken
// from t.TempDir() (gitSetenvRefusals). What the standard library does while
// it initialises is measured, not assumed:
// TestNoLinkedStandardPackageCopiesTheEnvironmentAtInit.
func TestEveryTestBinaryThatStartsAProcessDropsInheritedGit(t *testing.T) {
	dirs := repositoryGoFiles(t)
	tags, err := repositoryBuildTags(dirs)
	if err != nil {
		t.Fatalf("a //go:build line does not parse, so the tag sets are unknown and nothing was judged: %v", err)
	}
	// The witness for the tag discovery: eleven files of this tree carry
	// `//go:build conformance` on 2026-11-15, and mise.toml builds tests
	// with -tags=conformance. A discovery that finds nothing would judge the
	// default build alone and look green.
	if !slices.Contains(tags, "conformance") {
		t.Fatalf("the //go:build lines of the tree name the tags %v and not conformance, which this tree uses: the discovery cannot see the tag sets", tags)
	}
	refusals, judged, err := judgeTree(dirs, buildTagSets(tags))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refusals {
		t.Error(r)
	}
	for _, set := range buildTagSets(tags) {
		// A scan that judged nothing cannot tell a clean tree from a scan that
		// looked nowhere: this package has tests, so it is the witness.
		if got := judged[buildName(set)]; !slices.Contains(got, "tools/ci") {
			t.Fatalf("in the %s, the scan judged %d test binaries (%v) and not tools/ci, which has tests: it cannot judge this tree", buildName(set), len(got), got)
		}
		t.Logf("%s: %d directories read, %d of them build a test binary and were judged: %v", buildName(set), len(dirs), len(judged[buildName(set)]), judged[buildName(set)])
	}
	// FakeCredentials is the one source of non-constant Setenv keys that
	// gitSetenvRefusals accepts, and only because it keeps SCW_* keys alone.
	if why := fakeCredentialsKeepsScalewayKeys(dirs[conformancetestDir]); why != "" {
		t.Error(why)
	}
	for _, why := range append(stalePins(dirs, envEntryPins), stalePins(dirs, envSourcePins)...) {
		t.Error(why)
	}
}

// judgeTree asks both questions (hermeticFindings, gitSetenvRefusals) of every
// directory, once per build-tag set, and returns the refusals and, per build
// name, the directories that build a test binary.
func judgeTree(dirs map[string]map[string][]byte, sets [][]string) (refusals []string, judged map[string][]string, err error) {
	judged = map[string][]string{}
	for _, set := range sets {
		for _, dir := range slices.Sorted(maps.Keys(dirs)) {
			isTest, refusal, err := hermeticFindings(dir, dirs[dir], set)
			if err != nil {
				return nil, nil, fmt.Errorf("%s does not parse in the %s, so it was not judged: %w", dir, buildName(set), err)
			}
			if isTest {
				judged[buildName(set)] = append(judged[buildName(set)], dir)
			}
			if refusal != "" {
				refusals = append(refusals, refusal)
			}
			setenv, err := gitSetenvRefusals(dir, dirs[dir], set)
			if err != nil {
				return nil, nil, fmt.Errorf("%s does not parse in the %s, so its Setenv calls were not judged: %w", dir, buildName(set), err)
			}
			// A file every build compiles is refused once, not once per build.
			for _, r := range setenv {
				if !slices.Contains(refusals, r) {
					refusals = append(refusals, r)
				}
			}
		}
	}
	return refusals, judged, nil
}

// TestTheWalkJudgesEveryBuildTheRepositoryUses drives the walk on a fixture
// tree: a TestMain the tagged build leaves out, and a Setenv only the tagged
// build compiles, are each refused in that build and only there; the
// discovery names a tag the tree uses and leaves out platform and release
// tags, and a constraint written after the package clause, which is no
// constraint.
func TestTheWalkJudgesEveryBuildTheRepositoryUses(t *testing.T) {
	good := "package x\n\nimport (\n\t\"os\"\n\t\"testing\"\n\n\t\"" + hermeticImport + "\"\n)\n\nfunc TestMain(m *testing.M) {\n\thermetic.DropInheritedGit()\n\tos.Exit(m.Run())\n}\n"
	dirs := map[string]map[string][]byte{
		"a": {
			"a_test.go": []byte("package x\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n"),
			"m_test.go": []byte("//go:build !feintfixture\n\n" + good),
		},
		"b": {
			"m_test.go": []byte(good),
			"s_test.go": []byte("//go:build feintfixture && !plan9 && go1.22\n\npackage x\n\nimport \"testing\"\n\nfunc TestS(t *testing.T) { t.Setenv(\"GIT_DIR\", \"/home/someone/project/.git\") }\n"),
			"late.go":   []byte("package x\n\n//go:build notatag\n"),
		},
	}
	tags, err := repositoryBuildTags(dirs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tags, []string{"feintfixture"}) {
		t.Fatalf("the discovery names %v, want [feintfixture] alone: no platform tag, no release tag, nothing after the package clause", tags)
	}
	if got := buildTagSets([]string{"p", "q"}); len(got) != 4 || got[0] != nil || !slices.Equal(got[1], []string{"p"}) || !slices.Equal(got[2], []string{"q"}) || !slices.Equal(got[3], []string{"p", "q"}) {
		t.Errorf("buildTagSets([p q]) = %v, want [[] [p] [q] [p q]]", got)
	}
	refusals, judged, err := judgeTree(dirs, buildTagSets(tags))
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 2 || !strings.Contains(refusals[0], "a: its test binary has no TestMain in the build with -tags feintfixture") || !strings.Contains(refusals[1], "b/s_test.go:") || !strings.Contains(refusals[1], "Setenv(\"GIT_DIR\", ...) takes a value not derived") {
		t.Errorf("refusals = %q, want two, both in the build with -tags feintfixture: a's missing TestMain, then b's Setenv", refusals)
	}
	if got := judged["default build"]; !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("the default build judged %v, want [a b]", got)
	}
}

// stalePins names every pin of envEntryPins whose function is no longer
// declared in its file: a pin outliving its function would silence the
// question for whatever comes to bear that name.
func stalePins(dirs map[string]map[string][]byte, pins map[string]string) []string {
	var out []string
	for _, site := range slices.Sorted(maps.Keys(pins)) {
		file, fn, _ := strings.Cut(site, " ")
		fn, src, keyed := strings.Cut(fn, ": ")
		body := dirs[filepath.ToSlash(filepath.Dir(file))][filepath.Base(file)]
		declared := regexp.MustCompile(`\nfunc (\([^)]*\) )?` + regexp.QuoteMeta(fn) + `\(`)
		if !declared.Match(body) {
			out = append(out, "envEntryPins names "+site+", which no longer exists: the pin is stale, remove it")
		} else if keyed && !bytes.Contains(body, []byte(src)) {
			out = append(out, "envSourcePins names "+site+", whose source no longer appears in its file: the pin is stale, remove it")
		}
	}
	return out
}

// TestAPinWhoseFunctionIsGoneIsRefused: a pin naming a function its file still
// declares is accepted; one naming a function the file no longer declares, or
// a file that is gone, is refused with its reason.
func TestAPinWhoseFunctionIsGoneIsRefused(t *testing.T) {
	dirs := map[string]map[string][]byte{"internal/cli": {"a_test.go": []byte("package cli\n\nimport \"testing\"\n\ntype c struct{}\n\nfunc (c) Run() {}\n\nfunc TestKept(t *testing.T) { _ = y.Env() }\n")}}
	pins := map[string]string{
		"internal/cli/a_test.go TestKept":          "still there",
		"internal/cli/a_test.go Run":               "a method, still there",
		"internal/cli/a_test.go TestKept: y.Env()": "a source still written there",
		"internal/cli/a_test.go TestKept: cmd.Env": "a source no longer written there",
		"internal/cli/a_test.go TestRenamed":       "gone",
		"internal/cli/gone_test.go TestKept":       "file gone",
	}
	got := stalePins(dirs, pins)
	want := []string{
		"envSourcePins names internal/cli/a_test.go TestKept: cmd.Env, whose source no longer appears in its file: the pin is stale, remove it",
		"envEntryPins names internal/cli/a_test.go TestRenamed, which no longer exists: the pin is stale, remove it",
		"envEntryPins names internal/cli/gone_test.go TestKept, which no longer exists: the pin is stale, remove it",
	}
	if !slices.Equal(got, want) {
		t.Errorf("stalePins = %q, want %q", got, want)
	}
}

// repositoryGoFiles reads every .go file of the repository, keyed by its
// directory relative to the root (slash-separated) and then by base name.
// Hidden directories, testdata, node_modules and vendor are not packages of
// this module.
func repositoryGoFiles(t *testing.T) map[string]map[string][]byte {
	t.Helper()
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
	return dirs
}

// platformTags are the build tags `go test` sets on its own, from the
// platform and the toolchain: a //go:build line naming one of them does not
// name a build the repository chooses with -tags.
var platformTags = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "hurd": true,
	"illumos": true, "ios": true, "js": true, "linux": true, "nacl": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true, "windows": true, "zos": true,
	"386": true, "amd64": true, "arm": true, "arm64": true, "loong64": true, "mips": true,
	"mipsle": true, "mips64": true, "mips64le": true, "ppc64": true, "ppc64le": true,
	"riscv64": true, "s390x": true, "wasm": true,
	"cgo": true, "gc": true, "gccgo": true, "unix": true, "ignore": true,
}

// repositoryBuildTags names, sorted, every tag a `//go:build` line of the tree
// mentions that is not a platform or release tag: the tags a `go test -tags`
// of this repository can choose.
func repositoryBuildTags(dirs map[string]map[string][]byte) ([]string, error) {
	seen := map[string]bool{}
	for dir, files := range dirs {
		for name, src := range files {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, dir+"/"+name, src, parser.PackageClauseOnly|parser.ParseComments)
			if err != nil {
				return nil, err
			}
			for _, group := range file.Comments {
				if group.Pos() > file.Package {
					break
				}
				for _, c := range group.List {
					if !constraint.IsGoBuild(c.Text) {
						continue
					}
					expr, err := constraint.Parse(c.Text)
					if err != nil {
						return nil, fmt.Errorf("%s/%s: %w", dir, name, err)
					}
					expr.Eval(func(tag string) bool {
						if !platformTags[tag] && !strings.HasPrefix(tag, "go1.") {
							seen[tag] = true
						}
						return false
					})
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

// buildTagSets is the default build, then each tag alone, then all of them
// together when there are several: a binary can differ from the default one
// under any of them.
func buildTagSets(tags []string) [][]string {
	sets := [][]string{nil}
	for _, tag := range tags {
		sets = append(sets, []string{tag})
	}
	if len(tags) > 1 {
		sets = append(sets, slices.Clone(tags))
	}
	return sets
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
	const noTaggedTestMain = "has no TestMain in the build with -tags hermeticselftest"
	const notFirst = "as its first statement"
	const notOurs = "is not \"" + hermeticImport + "\"'s"
	const early = "while the package initialises"
	for _, tc := range []struct {
		name   string
		files  map[string][]byte
		judged bool
		reason string   // empty: no refusal expected
		tags   []string // the build judged; nil is the default build
	}{
		{"a test binary that drops GIT_* first is accepted", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+good)}, true, "", nil},
		{"the TestMain may live in a sibling file of the same binary", map[string][]byte{
			"a_test.go": file(execImport, usesExec),
			"m_test.go": file(hermeticImp, good),
		}, true, "", nil},
		{"an aliased import of the package is accepted", map[string][]byte{"a_test.go": file(execImport+"\th \""+hermeticImport+"\"\n", usesExec+strings.Replace(good, "hermetic.", "h.", 1))}, true, "", nil},
		{"a directory with no test file builds no test binary", map[string][]byte{"lib.go": library}, false, "", nil},
		{"a test that starts git only through a function outside its test files is refused", map[string][]byte{
			"lib.go":    library,
			"a_test.go": file("", noExec),
		}, true, noTestMain, nil},
		{"a test binary without a TestMain is refused", map[string][]byte{"a_test.go": file(execImport, usesExec)}, true, noTestMain, nil},
		{"a TestMain behind a build constraint is not in the default binary", map[string][]byte{
			"a_test.go": file(execImport, usesExec),
			"m_test.go": append([]byte("//go:build hermeticselftest\n\n"), file(hermeticImp, good)...),
		}, true, noTestMain, nil},
		{"a TestMain that runs the tests before dropping is refused", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+"func TestMain(m *testing.M) {\n\tcode := m.Run()\n\thermetic.DropInheritedGit()\n\tos.Exit(code)\n}\n")}, true, notFirst, nil},
		{"a TestMain that drops nothing is refused", map[string][]byte{"a_test.go": file(execImport, usesExec+"func TestMain(m *testing.M) {\n\tos.Exit(m.Run())\n}\n")}, true, notFirst, nil},
		{"a local function of the same name is not the package's", map[string][]byte{"a_test.go": file(execImport, usesExec+"func DropInheritedGit() {}\n\nfunc TestMain(m *testing.M) {\n\tDropInheritedGit()\n\tos.Exit(m.Run())\n}\n")}, true, notOurs, nil},
		{"a local function of the same name is not the package's even when the package is imported", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+"var _ = hermetic.DropInheritedGit\n\nfunc DropInheritedGit() {}\n\nfunc TestMain(m *testing.M) {\n\tDropInheritedGit()\n\tos.Exit(m.Run())\n}\n")}, true, notOurs, nil},
		{"another package's function of the same name is not the package's", map[string][]byte{"a_test.go": file(execImport+"\thermetic \"example.invalid/hermetic\"\n", usesExec+good)}, true, notOurs, nil},
		{"the environment copied at package level, before TestMain, is refused", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+"var inherited = os.Environ()\n\n"+good)}, true, early, nil},
		{"a GIT_* variable read by an init function outside the test files is refused", map[string][]byte{
			"lib.go":    []byte("package x\n\nimport \"os\"\n\nvar gitDir string\n\nfunc init() { gitDir = os.Getenv(\"GIT_DIR\") }\n"),
			"a_test.go": file(execImport+hermeticImp, usesExec+good),
		}, true, early, nil},
		{"a read that runs only when called, or of another variable, is accepted", map[string][]byte{"a_test.go": file(execImport+hermeticImp, usesExec+"var environ = func() []string { return os.Environ() }\n\nvar home = os.Getenv(\"HOME\")\n\n"+good)}, true, "", nil},
		// #958: the same directory judged in another build. The TestMain
		// lives in a file the tagged build leaves out, so that build has none.
		{"a TestMain the tagged build leaves out is refused in that build", map[string][]byte{
			"a_test.go": file(execImport, usesExec),
			"m_test.go": append([]byte("//go:build !hermeticselftest\n\n"), file(hermeticImp, good)...),
		}, true, noTaggedTestMain, []string{"hermeticselftest"}},
		{"the same directory is accepted in the default build", map[string][]byte{
			"a_test.go": file(execImport, usesExec),
			"m_test.go": append([]byte("//go:build !hermeticselftest\n\n"), file(hermeticImp, good)...),
		}, true, "", nil},
		{"a TestMain only the tagged build compiles is accepted in that build", map[string][]byte{
			"a_test.go": file(execImport, usesExec),
			"m_test.go": append([]byte("//go:build hermeticselftest\n\n"), file(hermeticImp, good)...),
		}, true, "", []string{"hermeticselftest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			judged, refusal, err := hermeticFindings("tools/x", tc.files, tc.tags)
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
