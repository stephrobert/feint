package ci

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// stdPackage is what `go list -json` says of one standard package a test
// binary of this module links, in one build.
type stdPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	Standard   bool
}

// linkedStandardPackages lists the standard packages linked by every test
// binary of the module, in every build-tag set the repository uses, with the
// files each compiles on this platform.
func linkedStandardPackages(t *testing.T, sets [][]string) map[string]stdPackage {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go on PATH, so the standard packages this module links cannot be listed and nothing was measured: %v", err)
	}
	// The go on PATH must be the one that built this test: another release
	// lists another standard library, and the verdict would be about it.
	version, err := exec.Command(goTool, "env", "GOVERSION").Output()
	if err != nil {
		t.Fatalf("go env GOVERSION failed, so which standard library would be measured is unknown: %v", err)
	}
	if err := sameToolchain(goTool, string(version), runtime.Version()); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]stdPackage{}
	for _, set := range sets {
		args := []string{"list", "-deps", "-test", "-json=ImportPath,Dir,GoFiles,CgoFiles,Standard"}
		if len(set) > 0 {
			args = append(args, "-tags", strings.Join(set, ","))
		}
		args = append(args, "./...")
		cmd := exec.Command(goTool, args...)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		listing, err := cmd.Output()
		if err != nil {
			t.Fatalf("go %s failed, so nothing was measured: %v\n%s", strings.Join(args, " "), err, stderr.String())
		}
		dec := json.NewDecoder(bytes.NewReader(listing))
		for {
			var p stdPackage
			if err := dec.Decode(&p); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatalf("the output of go list does not decode, so nothing was measured: %v", err)
			}
			if p.Standard {
				out[p.ImportPath] = p
			}
		}
	}
	return out
}

// sameToolchain refuses a listing made by another release than the one that
// built this test: `go env GOVERSION` as listed prints it, built is
// runtime.Version(). Another release lists another standard library, and the
// verdict would be about it.
func sameToolchain(goTool, listed, built string) error {
	if got := strings.TrimSpace(listed); got != built {
		return fmt.Errorf("the go on PATH (%s) is %s and this test was built by %s: its listing would measure another standard library, so this run cannot be judged", goTool, got, built)
	}
	return nil
}

// TestTheToolchainCheckRefusesAnotherRelease: the same release, as go env
// prints it with its newline, is accepted; another release, older or newer,
// is refused with the reason.
func TestTheToolchainCheckRefusesAnotherRelease(t *testing.T) {
	for _, tc := range []struct {
		listed, built string
		refused       bool
	}{
		{"go1.26.11\n", "go1.26.11", false},
		{"go1.26.10\n", "go1.26.11", true},
		{"go1.27.0\n", "go1.26.11", true},
	} {
		err := sameToolchain("/x/go", tc.listed, tc.built)
		switch {
		case !tc.refused && err != nil:
			t.Errorf("sameToolchain(%q, %q) refused: %v", tc.listed, tc.built, err)
		case tc.refused && (err == nil || !strings.Contains(err.Error(), "this run cannot be judged")):
			t.Errorf("sameToolchain(%q, %q) = %v, want a refusal saying this run cannot be judged", tc.listed, tc.built, err)
		}
	}
}

// environmentCopy is one place a linked standard package reads the whole
// environment: the package, and the top-level function it sits in.
type environmentCopy struct {
	pkg, fn string
}

// TestNoLinkedStandardPackageCopiesTheEnvironmentAtInit answers #958's second
// case with a measurement rather than a belief: a standard package that copies
// the environment while it initialises, before any TestMain, would keep a
// hook's GIT_DIR whatever TestMain drops afterwards, and
// TestEveryTestBinaryThatStartsAProcessDropsInheritedGit reads this module's
// code, not the standard library's.
//
// Population: every standard package `go list -deps -test ./...` gives, in
// every build-tag set the repository uses, with the files this platform
// compiles. Two questions:
//
//   - no string literal of those packages contains "GIT_" once unquoted (so
//     "GI\x54_DIR" counts), so none reads a GIT_* variable by name, at
//     initialisation or later;
//   - every reference to a function that reads the whole environment
//     (Environ, runtime_envs, or a name a //go:linkname ties to one), called
//     or not, sits in a function, never in a package-level value or an init
//     function, and the set of those functions is exactly
//     stdEnvironmentCopies, each read and found to run only when called.
//
// The listing is made by the go on PATH only when it is the release that
// built this test; otherwise the run cannot be judged and fails.
//
// A new Go release that adds such a reference changes the set, and this test
// goes red until somebody reads the new function. What it does not see: a
// variable name built at run time from pieces ("GI"+"T_DIR").
func TestNoLinkedStandardPackageCopiesTheEnvironmentAtInit(t *testing.T) {
	tags, err := repositoryBuildTags(repositoryGoFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	pkgs := linkedStandardPackages(t, buildTagSets(tags))
	// The witness: every test binary links os/exec (see hermeticFindings),
	// and syscall holds the copy of the environment os reads and changes.
	for _, want := range []string{"os", "os/exec", "syscall", "testing"} {
		if _, ok := pkgs[want]; !ok {
			t.Fatalf("the listing of linked standard packages has %d entries and not %s, which every test binary links: it measured another population", len(pkgs), want)
		}
	}
	files := 0
	scan := newStdScan()
	type parsedStd struct {
		path string
		fset *token.FileSet
		file *ast.File
	}
	var all []parsedStd
	for _, path := range slices.Sorted(maps.Keys(pkgs)) {
		p := pkgs[path]
		for _, name := range append(slices.Clone(p.GoFiles), p.CgoFiles...) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution|parser.ParseComments)
			if err != nil {
				t.Fatalf("%s/%s does not parse, so it was not measured: %v", path, name, err)
			}
			files++
			all = append(all, parsedStd{path, fset, file})
			scan.linknames(file)
		}
	}
	for _, p := range all {
		scan.file(p.path, p.fset, p.file)
	}
	t.Logf("measured %d linked standard packages, %d files, in the build-tag sets %v", len(pkgs), files, buildTagSets(tags))
	problems, reasons := scan.judge(stdEnvironmentCopies)
	for _, r := range reasons {
		t.Log(r)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// stdScan accumulates, over the files of the linked standard packages, the
// three things TestNoLinkedStandardPackageCopiesTheEnvironmentAtInit judges.
type stdScan struct {
	gitLiterals, earlyCalls []string
	found                   map[environmentCopy]bool
	envNames                map[string]bool // the names the environment is read through
}

func newStdScan() *stdScan {
	return &stdScan{found: map[environmentCopy]bool{}, envNames: map[string]bool{"Environ": true, "runtime_envs": true}}
}

// linknames adds the local names a //go:linkname ties to a function the
// environment is read through (syscall.runtime_envs, os.Environ, ...): a
// package can read it under a name of its own. Every file is read for this
// before any reference is judged.
func (s *stdScan) linknames(file *ast.File) {
	for _, group := range file.Comments {
		for _, c := range group.List {
			fields := strings.Fields(c.Text)
			if len(fields) < 3 || fields[0] != "//go:linkname" {
				continue
			}
			target := fields[2]
			if last := target[strings.LastIndex(target, ".")+1:]; last == "runtime_envs" || last == "Environ" {
				s.envNames[fields[1]] = true
			}
		}
	}
}

// calledName is the name a call is made through: f() or x.f().
func calledName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// file scans one parsed file of the standard package path.
func (s *stdScan) file(path string, fset *token.FileSet, file *ast.File) {
	ast.Inspect(file, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			// Unquoted, so that "GI\x54_DIR" is read as what it spells.
			if v, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(v, "GIT_") {
				s.gitLiterals = append(s.gitLiterals, fset.Position(lit.Pos()).String()+" "+lit.Value)
			}
		}
		return true
	})
	for _, decl := range file.Decls {
		where := "a package-level value"
		early := true
		switch d := decl.(type) {
		case *ast.FuncDecl:
			where = d.Name.Name
			if d.Recv != nil && len(d.Recv.List) == 1 {
				where = "(" + types(d.Recv.List[0].Type) + ")." + where
			}
			early = d.Recv == nil && d.Name.Name == "init"
			if early {
				where = "an init function"
			}
		case *ast.GenDecl:
			early = d.Tok == token.VAR
		}
		// Every reference counts, called or not: `var envFn = os.Environ`
		// copies nothing yet, but whoever calls envFn reads the environment.
		// The name a function is declared under is not a reference.
		var declName *ast.Ident
		if fn, ok := decl.(*ast.FuncDecl); ok {
			declName = fn.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id != declName && s.envNames[id.Name] {
				s.found[environmentCopy{path, where}] = true
			}
			return true
		})
		if !early {
			continue
		}
		for _, call := range callsAtInit(decl) {
			if name := calledName(call); slices.Contains(stdEntryPoints, name) {
				s.earlyCalls = append(s.earlyCalls, fset.Position(call.Pos()).String()+" calls "+name+" in "+where)
			}
		}
	}
}

// judge returns what the scan refuses, and the reason of every pinned read.
func (s *stdScan) judge(pins map[environmentCopy]string) (problems, reasons []string) {
	if len(s.gitLiterals) > 0 {
		problems = append(problems, "linked standard packages name a GIT_* variable, so one may read it before TestMain drops it; read each and pin it here:\n"+strings.Join(s.gitLiterals, "\n"))
	}
	for _, c := range slices.SortedFunc(maps.Keys(s.found), func(a, b environmentCopy) int { return strings.Compare(a.pkg+" "+a.fn, b.pkg+" "+b.fn) }) {
		switch reason, pinned := pins[c]; {
		case (c.fn == "a package-level value" || c.fn == "an init function") && c.pkg != "syscall":
			problems = append(problems, c.pkg+" reads the whole environment in "+c.fn+", while it initialises, before any TestMain: a hook's GIT_DIR survives in that copy")
		case !pinned:
			problems = append(problems, c.pkg+" reads the whole environment in "+c.fn+", which nobody has read: check it runs only when called, then pin it in stdEnvironmentCopies with the reason")
		default:
			reasons = append(reasons, c.pkg+" "+c.fn+": "+reason)
		}
	}
	if len(s.earlyCalls) > 0 {
		problems = append(problems, "linked standard packages call, while they initialise, a name through which the environment is read; read each:\n"+strings.Join(s.earlyCalls, "\n"))
	}
	for _, c := range slices.SortedFunc(maps.Keys(pins), func(a, b environmentCopy) int { return strings.Compare(a.pkg+" "+a.fn, b.pkg+" "+b.fn) }) {
		if !s.found[c] {
			problems = append(problems, "stdEnvironmentCopies pins "+c.pkg+" "+c.fn+", which no linked standard package calls any more: the pin is stale, remove it")
		}
	}
	return problems, reasons
}

// TestTheStandardLibraryScanAsksEachQuestion: one case per question of
// stdScan, on fixtures, each failing only its own, with the accepting half:
// the shapes the real standard library has on go1.26.11.
func TestTheStandardLibraryScanAsksEachQuestion(t *testing.T) {
	pins := map[environmentCopy]string{
		{"syscall", "a package-level value"}: "the store",
		{"os", "Environ"}:                    "the accessor",
	}
	const syscallSrc = "package syscall\n\nvar envs []string = runtime_envs()\n\nfunc runtime_envs() []string\n"
	const osSrc = "package os\n\nimport \"syscall\"\n\nfunc Environ() []string { return syscall.Environ() }\n"
	for _, tc := range []struct {
		name   string
		extra  map[string]string // package path -> one file, beside syscall and os
		pins   map[environmentCopy]string
		reason string // empty: no problem expected
	}{
		{"the real shapes are accepted: the store, the accessor, a usage literal that runs when called", map[string]string{
			"flag": "package flag\n\nimport \"os\"\n\nvar Usage = func() { _ = CommandLine.Output(); _ = os.Args }\n",
		}, pins, ""},
		{"a standard package naming a GIT_* variable is refused", map[string]string{
			"x": "package x\n\nimport \"os\"\n\nfunc dir() string { return os.Getenv(\"GIT_DIR\") }\n",
		}, pins, "name a GIT_* variable"},
		{"the environment copied in a package-level value is refused", map[string]string{
			"x": "package x\n\nimport \"os\"\n\nvar inherited = os.Environ()\n",
		}, pins, "while it initialises, before any TestMain"},
		{"the environment copied in an init function is refused", map[string]string{
			"x": "package x\n\nimport \"os\"\n\nvar inherited []string\n\nfunc init() { inherited = os.Environ() }\n",
		}, pins, "in an init function, while it initialises"},
		{"the environment read through a reference, not a call, at package level is refused", map[string]string{
			"x": "package x\n\nimport \"os\"\n\nvar envFn = os.Environ\n",
		}, pins, "while it initialises, before any TestMain"},
		{"the environment read under a //go:linkname name of its own is refused", map[string]string{
			"x": "package x\n\nimport _ \"unsafe\"\n\n//go:linkname myEnvs syscall.runtime_envs\nfunc myEnvs() []string\n\nvar inherited = myEnvs()\n",
		}, pins, "while it initialises, before any TestMain"},
		{"a GIT_* name spelled with an escape is read as what it spells", map[string]string{
			"x": "package x\n\nimport \"os\"\n\nfunc dir() string { return os.Getenv(\"GI\\x54_DIR\") }\n",
		}, pins, "name a GIT_* variable"},
		{"a function nobody has read is refused", map[string]string{
			"x": "package x\n\nimport \"os\"\n\nfunc snapshot() []string { return os.Environ() }\n",
		}, pins, "which nobody has read"},
		{"a process started while a package initialises is refused", map[string]string{
			"x": "package x\n\nimport \"os/exec\"\n\nvar _ = exec.Command(\"true\").Run()\n",
		}, pins, "calls Run in a package-level value"},
		{"a pin nothing calls any more is refused", nil, map[environmentCopy]string{
			{"syscall", "a package-level value"}: "the store",
			{"os", "Environ"}:                    "the accessor",
			{"os/exec", "(*Cmd).environ"}:        "gone",
		}, "the pin is stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan := newStdScan()
			srcs := map[string]string{"syscall": syscallSrc, "os": osSrc}
			maps.Copy(srcs, tc.extra)
			fsets, parsed := map[string]*token.FileSet{}, map[string]*ast.File{}
			for _, path := range slices.Sorted(maps.Keys(srcs)) {
				fsets[path] = token.NewFileSet()
				file, err := parser.ParseFile(fsets[path], path+"/a.go", srcs[path], parser.SkipObjectResolution|parser.ParseComments)
				if err != nil {
					t.Fatalf("the fixture does not parse, so it measures nothing: %v", err)
				}
				parsed[path] = file
				scan.linknames(file)
			}
			for _, path := range slices.Sorted(maps.Keys(srcs)) {
				scan.file(path, fsets[path], parsed[path])
			}
			problems, _ := scan.judge(tc.pins)
			switch {
			case tc.reason == "" && len(problems) != 0:
				t.Errorf("refused %q, want nothing refused", problems)
			case tc.reason != "" && (len(problems) != 1 || !strings.Contains(problems[0], tc.reason)):
				t.Errorf("refused %q, want exactly one problem saying %q", problems, tc.reason)
			}
		})
	}
}

// callsAtInit lists the calls a package-level value or an init function makes
// while the package initialises. A function literal runs only when it is
// called, so its body counts only when it is called on the spot.
func callsAtInit(decl ast.Decl) []*ast.CallExpr {
	var out []*ast.CallExpr
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			out = append(out, x)
			if lit, ok := x.Fun.(*ast.FuncLit); ok {
				ast.Inspect(lit.Body, visit)
				for _, arg := range x.Args {
					ast.Inspect(arg, visit)
				}
				return false
			}
		}
		return true
	}
	if fn, ok := decl.(*ast.FuncDecl); ok {
		ast.Inspect(fn.Body, visit)
	} else {
		ast.Inspect(decl, visit)
	}
	return out
}

// types renders a receiver type: T or *T.
func types(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return "*" + types(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return types(x.X)
	case *ast.IndexListExpr:
		return types(x.X)
	}
	return strconv.Quote("?")
}

// stdEnvironmentCopies is every function of a linked standard package that
// reads the whole environment, measured on go1.26.11 linux/amd64 on
// 2026-11-15, with why none of them runs before TestMain.
//
// One entry sits in a package-level value, and it is pinned on purpose:
// syscall's `envs = runtime_envs()` is not a copy beside the environment, it
// is the environment. syscall.Unsetenv, which os.Unsetenv and so
// hermetic.DropInheritedGit call, edits it, and syscall.Environ, which a
// child process receives through internal/syscall/execenv, reads it.
// TestATestBinaryDropsTheGitDirAHookExports measures the outcome end to end:
// a child started after TestMain no longer sees the hook's GIT_DIR.
var stdEnvironmentCopies = map[environmentCopy]string{
	{"syscall", "a package-level value"}:    "envs = runtime_envs() is the environment store itself; Unsetenv edits it and Environ reads it",
	{"os", "Environ"}:                       "the accessor: returns syscall.Environ() when called",
	{"os/exec", "(*Cmd).environ"}:           "called by (*Cmd).Start, when a test starts a process, after TestMain",
	{"internal/syscall/execenv", "Default"}: "called by os/exec when a process starts, returns syscall.Environ()",
	{"internal/fuzz", "CoordinateFuzzing"}:  "runs under -test.fuzz, from m.Run, after TestMain",
	{"go/build", "(*Context).importGo"}:     "runs `go list` when Import resolves a module path, when called",
}

// stdEntryPoints are the names through which the functions above run: their
// own (but Environ, whose direct call at initialisation is the first question
// already), and the calls that lead to them (os/exec's Start and the methods that
// call it, go/build's Import and ImportDir). A linked standard package whose
// package-level values or init functions call one of these names may run a
// pinned function before TestMain, so the "only when called" of each reason
// is checked one level up rather than believed.
var stdEntryPoints = []string{"environ", "Default", "CoordinateFuzzing", "importGo", "Start", "Run", "Output", "CombinedOutput", "Import", "ImportDir"}
