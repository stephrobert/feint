package ci

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// gitSetenvRefusals answers #958's first case: a test that sets a GIT_*
// variable itself, after TestMain dropped the inherited ones, aimed at a tree
// somebody works in. Two tests set GIT_DIR on purpose to play a hook
// (tools/testplan/tracked_hook_test.go, tools/ci/throwaway_git_test.go), and
// three hand one to a child process (tools/ci/hermetic_test.go,
// tools/ci/falsify_harness_test.go, tools/testplan/tracked_hook_test.go),
// each at a decoy under t.TempDir(); nothing stopped another from naming the
// working tree.
//
// The rule is static. It reads, in the build named by tags, every _test.go
// file, every non-test file of a package whose name ends in "test" (those only
// test binaries link: internal/conformancetest, feinttest), and, in any other
// non-test file, a Setenv whose receiver is a testing parameter. It asks
// three questions, each refusing what it does not recognise:
//
//   - setenv: a call X.Setenv(key, value) whose key is a GIT_* constant must
//     have a value derived from t.TempDir(); a key it cannot read as a
//     constant is refused, since it may be GIT_DIR. "Derived" is narrow on
//     purpose (derived): t.TempDir() on a *testing.T, B or F, filepath.Join
//     of a derived value and plain literals with no "..", or a local name
//     declared in the same declaration, assigned exactly once from a derived
//     value, whose address is never taken.
//   - literals: every string literal whose value, unquoted, starts with GIT_
//     is accepted only as the key of a Setenv (judged above), the value of a
//     constant used as a Setenv key and nowhere else, "GIT_X=" + a derived
//     value (or "GIT_CONFIG_GLOBAL=" + os.DevNull), a key in gitHarmlessKeys,
//     or a read (the string given to strings.HasPrefix and its kin,
//     the name given to Getenv or LookupEnv, an operand of == or !=). A
//     variable name has to be written somewhere, so this question closes every
//     way of building an environment that writes the name whole: a helper, a
//     map, fmt.Sprintf, a concatenation after the name, a list a caller
//     passes. A name split before GIT_ ("GI"+"T_DIR") is not seen, and that
//     is a stated limit.
//   - envEntries: an environment handed over (`X.Env = ...`, for any X since
//     no type is known; an Env field of an exec.Cmd, os.ProcAttr or
//     syscall.ProcAttr literal) is read where it is built, an append or a
//     slice literal, directly or through a local variable; taken from
//     anything else it is refused unless vouched for in envSourcePins. Every
//     entry it adds has a key this guard can read, because a key read at run
//     time may be GIT_DIR whatever the literals say.
//
// Non-test files outside a *test package are asked only about a Setenv on a
// testing parameter: they are product code. On 2026-11-15 the only GIT_*
// literals there are internal/hermetic/hermetic.go's, which reads them.
//
// Why static and not a check in internal/hermetic: the testing package offers
// no hook on t.Setenv or on a child's environment, so a runtime check needs
// every caller to go through a helper, and a static guard to refuse the
// callers that do not. That guard alone answers the question.
//
// Decided on 2026-11-15, not open: a symbolic link under t.TempDir() that
// points at a real tree is out of static reach, and a test would have to
// create it on purpose; internal/cli/up.go's os.Setenv is the product setting
// a lab's declared variables, not a test, so not this guard's subject. A list
// spread into an environment from a parameter of the function is accepted,
// and that is a limit, not a proof: the literal question judges a GIT_* name
// a caller writes, but a list built at run time and passed in (read from a
// file, split from a string) is not judged at the call site. Measured on
// 2026-11-16: run(lines), with run spreading its parameter into cmd.Env and
// lines read from a file, is accepted; so is a helper newCmd(env) that
// spreads its own parameter.
//
// A key that is not a constant is accepted in one shape: the key of a range
// over a map whose keys the guard can read, none of them GIT_* (rangedKeys).
// Functions whose keys are read at run time otherwise are pinned in
// envEntryPins, which silences the third question alone.
//
// Refusals carry the file and the line. TestTheSetenvGuardAsksEachQuestion
// and TestTheEnvironmentEntryGuardAsksEachQuestion have one case per question.
func gitSetenvRefusals(dir string, files map[string][]byte, tags []string) ([]string, error) {
	ctx := buildContext(files, tags)
	fset := token.NewFileSet()
	type parsed struct {
		name string
		file *ast.File
		full bool
	}
	var compiled []parsed
	consts := map[string]map[string]string{} // package name, then constant
	for _, name := range slices.Sorted(maps.Keys(files)) {
		ok, err := ctx.MatchFile(dir, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		// Object resolution is kept on: it ties each name to the declaration
		// it refers to, so that three loops each binding `name` in a scope
		// of their own are not counted as one name assigned three times.
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), files[name], 0)
		if err != nil {
			return nil, err
		}
		pkg := file.Name.Name
		full := strings.HasSuffix(name, "_test.go") || strings.HasSuffix(pkg, "test")
		compiled = append(compiled, parsed{name, file, full})
		if consts[pkg] == nil {
			consts[pkg] = map[string]string{}
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != len(vs.Names) {
					continue
				}
				for i, n := range vs.Names {
					if v, ok := stringLiteral(vs.Values[i]); ok {
						consts[pkg][n.Name] = v
					}
				}
			}
		}
	}
	keyConsts := map[string]map[string]bool{}
	for _, p := range compiled {
		if p.full {
			pkg := p.file.Name.Name
			if keyConsts[pkg] == nil {
				var group []*ast.File
				for _, q := range compiled {
					if q.full && q.file.Name.Name == pkg {
						group = append(group, q.file)
					}
				}
				keyConsts[pkg] = setenvOnlyConsts(group)
			}
		}
	}
	var out []string
	for _, p := range compiled {
		pkg := p.file.Name.Name
		alias := importNames(p.file)
		for _, decl := range p.file.Decls {
			site := dir + "/" + p.name
			if fn, ok := decl.(*ast.FuncDecl); ok {
				site += " " + fn.Name.Name
			}
			s := newScope(decl, dir, alias, consts[pkg])
			var found []refusal
			if p.full {
				found = append(found, s.setenv(false)...)
				found = append(found, s.literals(keyConsts[pkg])...)
				found = append(found, s.envEntries()...)
			} else {
				found = s.setenv(true)
			}
			for _, r := range found {
				if _, pinned := envEntryPins[site]; pinned && r.entry {
					continue
				}
				if r.source && sourceVouched(site, r.src) {
					continue
				}
				out = append(out, fmt.Sprintf("%s/%s:%d: %s", dir, p.name, fset.Position(r.pos).Line, r.why))
			}
		}
	}
	return out, nil
}

// refusal is one thing a question refuses, where, and whether it is the
// third question's (an entry whose key cannot be read), which a pin silences.
type refusal struct {
	pos    token.Pos
	why    string
	entry  bool
	source bool   // an environment taken from what cannot be read, which envSourcePins silences
	src    string // that source, as gotypes.ExprString prints it
}

// conformancetestImport and conformancetestDir name the package whose
// FakeCredentials is the one call rangedKeys accepts as a map of known keys.
const (
	conformancetestImport = "github.com/stephrobert/feint/internal/conformancetest"
	conformancetestDir    = "internal/conformancetest"
)

// envEntryPins are the functions whose environment entries have keys read at
// run time, each with why none can be GIT_*. A pin silences the third
// question in that function alone, never the first two: a literal "GIT_X="
// written there is still judged. TestEveryTestBinaryThatStartsAProcessDropsInheritedGit
// refuses a pin whose function no longer exists (stalePins).
var envEntryPins = map[string]string{
	"internal/cli/object_storage_conformance_test.go TestMountedS3OfficialAWSCLIBytes": "copies the scw client's own environment and the Scaleway pack's Env(endpoint).Vars, product code that publishes the pack's SCW_* variables; -tags conformance only",
	"internal/conformancetest/exo.go EXO":                                              "appends the vars its callers pass, the Exoscale pack's own environment (internal/cli/exo_endpoint_conformance_test.go), product code that publishes EXOSCALE_* variables; -tags conformance only",
}

// envSourcePins are the sources, keyed "file function: source" with the
// source as the refusal prints it (gotypes.ExprString), from which a function
// hands a child an environment built elsewhere, each with where it is built
// and why that place is judged or harmless. A pin silences that one source in
// that one function (#958, cycle 3): a source added later to the same
// function is refused, and the entries and literals written there are still
// judged. stalePins refuses a pin whose function, or whose source text, no
// longer appears in its file. A pin matches the source text, not what a name
// in it refers to: a local that shadows a pinned function's name (a variable
// called withoutInheritedGit) passes in the pinned function. That takes
// intent, and it is a stated limit.
var envSourcePins = map[string]string{
	"tools/ci/hermetic_test.go TestATestBinaryDropsTheGitDirAHookExports: witnessEnv":                                "witnessEnv is what the decoy closure returns: append(os.Environ(), \"GIT_DIR=\"+gitDir, \"GIT_INDEX_FILE=\"+...), literals the second question judges",
	"tools/ci/throwaway_git_test.go Git: r.Env()":                                                                    "r.Env() is a clone of hermeticGitEnv(): os.Environ() without GIT_* and PRE_COMMIT*, plus literal entries the second question judges",
	"tools/ci/throwaway_git_test.go TestAThrowawayRepoIgnoresTheGitDirAHookExports: repo.Env()":                      "repo.Env(), the clone of hermeticGitEnv() described for Git",
	"tools/ci/testplan_removed_test.go TestTheTestplanAsksForTheSpecADeletedTestWasJudgedBy: repo.Env()":             "repo.Env(), the clone of hermeticGitEnv() described for Git",
	"tools/ci/falsify_harness_test.go TestTheTestplanCheckHandsBackItsOwnExitCode: repo.Env()":                       "repo.Env(), the clone of hermeticGitEnv() described for Git",
	"tools/testplan/plan_test.go tracked: withoutInheritedGit(os.Environ())":                                         "withoutInheritedGit(os.Environ()) drops every GIT_* from the process's environment and adds nothing",
	"internal/cli/exo_endpoint_conformance_test.go TestExoDropsConfiguredS3EndpointPrefix: exo.Env":                  "exo.Env, a conformancetest.Client's environment, built in internal/conformancetest, whose non-test files this guard judges in full; -tags conformance only",
	"internal/cli/object_storage_conformance_test.go TestMountedS3OfficialAWSCLIBytes: aws.Env":                      "aws.Env, a conformancetest.Client's environment, see TestExoDropsConfiguredS3EndpointPrefix; -tags conformance only",
	"internal/s3/client_conformance_test.go TestOfficialAWSCLIBytes: cli.Env":                                        "cli.Env, a conformancetest.Client's environment, see TestExoDropsConfiguredS3EndpointPrefix; -tags conformance only",
	"internal/conformancetest/aws.go Run: c.Env":                                                                     "c.Env, the Client's own environment, built in this package, which this guard judges in full; -tags conformance only",
	"internal/conformancetest/aws.go Attempt: c.Env":                                                                 "c.Env, as for Run; -tags conformance only",
	"tools/ci/hermetic_test.go TestATestBinaryDropsTheGitDirAHookExports: hookEnv":                                   "hookEnv is what the decoy closure returns, like witnessEnv: the hook's pair aimed at a decoy under t.TempDir(), which the child must drop in its TestMain; that drop is what the test measures",
	"tools/ci/preflight_milestones_test.go runMilestoneGate: repo.Env()":                                             "repo.Env(), the clone of hermeticGitEnv() described for Git",
	"tools/conformance/functionalgate_test.go runStackGate: gateEnvironment()":                                       "os.Environ() less five FEINT_FUNCTIONAL_* and FEINT_VM entries; it adds nothing",
	"tools/evidence/mode_test.go runMode: cleanEnvironment()":                                                        "os.Environ() less FEINT_VM and FEINT_EVIDENCE_VM; it adds nothing",
	"tools/testplan/tracked_hook_test.go TestTrackedIgnoresTheGitDirAHookExports: withoutInheritedGit(os.Environ())": "drops every GIT_* from the process's environment; the decoy entries appended after it are literals the second question judges",
}

// gitHarmlessKeys are the GIT_* variables whose value names no repository:
// identity, prompting and the system configuration switch. They may take any
// value; GIT_CONFIG_GLOBAL may take os.DevNull alone.
// tools/ci/throwaway_git_test.go's hermeticGitEnv sets these, and every other
// GIT_* variable must come from t.TempDir().
var gitHarmlessKeys = map[string]bool{
	"GIT_CONFIG_NOSYSTEM": true, "GIT_TERMINAL_PROMPT": true,
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
	"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
}

// binding is one way a local name receives a value inside a scope: an
// assignment (value may be nil when it cannot be paired with one expression),
// a range clause, or its address being taken.
type binding struct {
	value     ast.Expr
	rangeOver *ast.RangeStmt
	addressed bool
	zero      bool // `var x T`: declared with no value
}

// fieldKey names tc.field for a local tc.
//
//nolint:staticcheck // SA1019: ast.Object, file-level name resolution, is what tells three loops binding `name` apart; a name it cannot resolve has no Obj and is refused, never accepted (#958)
type fieldKey struct {
	obj   *ast.Object
	field string
}

// uses records where each local name appears, so that a map can be refused
// when it appears anywhere but its declaration and the range clauses that read
// it: `m[k] = v`, `fill(m)` or `alias := m` can each add a key the literal
// does not show.
//
//nolint:staticcheck // SA1019: see fieldKey (#958)
type uses struct {
	allowed map[ast.Node]bool
	selX    map[*ast.Ident]bool
	idents  map[*ast.Object][]*ast.Ident
	fields  map[fieldKey][]*ast.SelectorExpr
}

// escapes reports a map name used beyond its declaration and range clauses.
//
//nolint:staticcheck // SA1019: see fieldKey (#958)
func (u uses) escapes(obj *ast.Object) bool {
	return slices.ContainsFunc(u.idents[obj], func(id *ast.Ident) bool { return !u.allowed[id] })
}

// fieldEscapes reports tc.field used beyond range clauses, or tc itself
// passed whole, which hands out the maps it holds.
//
//nolint:staticcheck // SA1019: see fieldKey (#958)
func (u uses) fieldEscapes(obj *ast.Object, field string) bool {
	if slices.ContainsFunc(u.fields[fieldKey{obj, field}], func(s *ast.SelectorExpr) bool { return !u.allowed[s] }) {
		return true
	}
	return slices.ContainsFunc(u.idents[obj], func(id *ast.Ident) bool { return !u.allowed[id] && !u.selX[id] })
}

// envSite is one environment handed over: X.Env = value (target X.Env), or
// an Env: field of a literal (no target).
type envSite struct {
	target, value ast.Expr
}

// scope is one top-level declaration, read once for the three questions.
// Bindings are counted over the whole declaration, closures included: a name
// a closure reassigns is not assigned once.
//
//nolint:staticcheck // SA1019: see fieldKey (#958)
type scope struct {
	node     ast.Node
	dir      string
	alias    map[string]string
	consts   map[string]string
	bindings map[*ast.Object][]binding
	tempDirs map[*ast.Object]bool // parameters typed *testing.T, *testing.B or *testing.F
	tests    map[*ast.Object]bool // those, and testing.TB
	params   map[*ast.Object]bool
	u        uses
	parents  map[ast.Node]ast.Node
	calls    map[*ast.SelectorExpr]*ast.CallExpr
	setenvs  []*ast.SelectorExpr
	lits     []*ast.BasicLit
	envs     []envSite
	writes   []ast.Node // X.Env[i] = ..., copy(X.Env, ...)
}

// newScope reads one declaration.
//
//nolint:staticcheck // SA1019: see fieldKey (#958)
func newScope(node ast.Node, dir string, alias, consts map[string]string) *scope {
	s := &scope{
		node: node, dir: dir, alias: alias, consts: consts,
		bindings: map[*ast.Object][]binding{},
		tempDirs: map[*ast.Object]bool{}, tests: map[*ast.Object]bool{}, params: map[*ast.Object]bool{},
		u:       uses{allowed: map[ast.Node]bool{}, selX: map[*ast.Ident]bool{}, idents: map[*ast.Object][]*ast.Ident{}, fields: map[fieldKey][]*ast.SelectorExpr{}},
		parents: map[ast.Node]ast.Node{},
		calls:   map[*ast.SelectorExpr]*ast.CallExpr{},
	}
	bind := func(lhs []ast.Expr, rhs []ast.Expr, zero bool) {
		for i, l := range lhs {
			id, ok := l.(*ast.Ident)
			if !ok || id.Obj == nil {
				continue
			}
			s.u.allowed[id] = true
			b := binding{zero: zero && len(rhs) == 0}
			if len(lhs) == len(rhs) {
				b.value = rhs[i]
			}
			s.bindings[id.Obj] = append(s.bindings[id.Obj], b)
		}
	}
	var stack []ast.Node
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			s.parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *ast.FuncType:
			if x.Params == nil {
				break
			}
			for _, field := range x.Params.List {
				kind := testingParamKind(field.Type, alias["testing"])
				for _, name := range field.Names {
					if name.Obj == nil {
						continue
					}
					s.params[name.Obj] = true
					if kind != "" {
						s.tests[name.Obj] = true
					}
					if kind == "star" {
						s.tempDirs[name.Obj] = true
					}
				}
			}
		case *ast.AssignStmt:
			bind(x.Lhs, x.Rhs, false)
			for i, l := range x.Lhs {
				if ix, ok := l.(*ast.IndexExpr); ok {
					if sel, ok := ix.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Env" {
						s.writes = append(s.writes, ix)
					}
				}
				if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "Env" && len(x.Lhs) == len(x.Rhs) {
					s.envs = append(s.envs, envSite{target: sel, value: x.Rhs[i]})
				}
			}
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, len(x.Names))
			for i, n := range x.Names {
				lhs[i] = n
			}
			bind(lhs, x.Values, true)
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if id, ok := e.(*ast.Ident); ok && id.Obj != nil {
					s.bindings[id.Obj] = append(s.bindings[id.Obj], binding{rangeOver: x})
					s.u.allowed[id] = true
				}
			}
			s.u.allowed[x.X] = true
		case *ast.UnaryExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Obj != nil && x.Op == token.AND {
				s.bindings[id.Obj] = append(s.bindings[id.Obj], binding{addressed: true})
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Setenv" {
				s.calls[sel] = x
			}
			if f, ok := x.Fun.(*ast.Ident); ok && f.Name == "copy" && f.Obj == nil && len(x.Args) == 2 {
				if sel, ok := x.Args[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "Env" {
					s.writes = append(s.writes, x)
				}
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "Setenv" {
				s.setenvs = append(s.setenvs, x)
			}
			if id, ok := x.X.(*ast.Ident); ok && id.Obj != nil {
				s.u.selX[id] = true
				k := fieldKey{id.Obj, x.Sel.Name}
				s.u.fields[k] = append(s.u.fields[k], x)
			}
		case *ast.Ident:
			if x.Obj != nil {
				s.u.idents[x.Obj] = append(s.u.idents[x.Obj], x)
			}
		case *ast.BasicLit:
			s.lits = append(s.lits, x)
		case *ast.KeyValueExpr:
			if k, ok := x.Key.(*ast.Ident); ok && k.Name == "Env" && len(stack) > 1 {
				if lit, ok := stack[len(stack)-2].(*ast.CompositeLit); ok && s.isEnvHolder(lit.Type) {
					s.envs = append(s.envs, envSite{value: x.Value})
				}
			}
		}
		return true
	})
	return s
}

// setenv is the first question. testingOnly judges only the calls whose
// receiver is a testing parameter: what a non-test file of an ordinary
// package is asked.
func (s *scope) setenv(testingOnly bool) []refusal {
	var out []refusal
	for _, sel := range s.setenvs {
		if testingOnly {
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Obj == nil || !s.tests[recv.Obj] {
				continue
			}
		}
		call, ok := s.calls[sel]
		if !ok {
			out = append(out, refusal{pos: sel.Pos(), why: "Setenv is used as a value, so what it is called with cannot be read; call it directly"})
			continue
		}
		if len(call.Args) != 2 {
			out = append(out, refusal{pos: call.Pos(), why: "a Setenv call with " + strconv.Itoa(len(call.Args)) + " arguments is a shape this guard does not know"})
			continue
		}
		key, known := s.constantKey(call.Args[0])
		switch {
		case known && !strings.HasPrefix(key, "GIT_"):
		case known && !s.derived(call.Args[1], 0):
			out = append(out, refusal{pos: call.Pos(), why: "Setenv(" + strconv.Quote(key) + ", ...) takes a value not derived from t.TempDir(), so it may name a tree somebody works in; build it from t.TempDir() (#958)"})
		case known:
		default:
			keys, readable := s.rangedKeys(call.Args[0])
			if !readable {
				out = append(out, refusal{pos: call.Pos(), why: "Setenv is called with a key that is not a constant, so it may be a GIT_* variable; name the key as a constant (#958)"})
				continue
			}
			if i := slices.IndexFunc(keys, func(k string) bool { return strings.HasPrefix(k, "GIT_") }); i >= 0 {
				out = append(out, refusal{pos: call.Pos(), why: "Setenv is called with keys ranging over a map that holds " + strconv.Quote(keys[i]) + ", whose value this guard cannot trace to t.TempDir(); set it on its own line (#958)"})
			}
		}
	}
	return out
}

// gitPrefixReads are the strings functions a GIT_* literal may be handed to:
// they read a name, they set nothing.
var gitPrefixReads = []string{"HasPrefix", "Contains", "TrimPrefix", "CutPrefix"}

// literals is the second question, see gitSetenvRefusals.
func (s *scope) literals(keyConsts map[string]bool) []refusal {
	var out []refusal
	for _, lit := range s.lits {
		v, ok := stringLiteral(lit)
		if !ok || !strings.HasPrefix(v, "GIT_") {
			continue
		}
		key, rest, isEntry := strings.Cut(v, "=")
		if gitHarmlessKeys[key] || s.isRead(lit) || s.isSetenvKey(lit) || s.isKeyConst(lit, keyConsts) {
			continue
		}
		if !isEntry {
			out = append(out, refusal{pos: lit.Pos(), why: "the literal " + strconv.Quote(v) + " names a GIT_* variable outside every shape this guard reads (a Setenv key, a constant used only as one, \"GIT_X=\" + a value from t.TempDir(), a key in gitHarmlessKeys, a read), so it may reach a child's environment unjudged (#958)"})
			continue
		}
		bin, isConcat := s.parents[lit].(*ast.BinaryExpr)
		isConcat = isConcat && bin.Op == token.ADD && bin.X == lit
		if isConcat {
			if outer, chained := s.parents[bin].(*ast.BinaryExpr); chained && outer.X == bin {
				isConcat = false
			}
		}
		switch {
		case !isConcat && rest != "":
			out = append(out, refusal{pos: lit.Pos(), why: "the environment entry " + strconv.Quote(v) + " names a value not derived from t.TempDir(), so a child git may obey it in a tree somebody works in (#958)"})
		case !isConcat || rest != "":
			out = append(out, refusal{pos: lit.Pos(), why: "a literal opening the environment entry " + key + "= sits in a shape this guard cannot read; write \"" + key + "=\" + <a value from t.TempDir()> (#958)"})
		case key == "GIT_CONFIG_GLOBAL" && s.isPkg(bin.Y, "os", "DevNull"):
		case !s.derived(bin.Y, 0):
			out = append(out, refusal{pos: lit.Pos(), why: "the environment entry " + key + "= takes a value not derived from t.TempDir(), so a child git may obey it in a tree somebody works in (#958)"})
		}
	}
	return out
}

// isRead reports a GIT_* literal that is read, not set: the string given to
// strings.HasPrefix and its kin, the name given to os/syscall Getenv or
// LookupEnv, or an operand of == or !=.
func (s *scope) isRead(lit *ast.BasicLit) bool {
	switch p := s.parents[lit].(type) {
	case *ast.BinaryExpr:
		return p.Op == token.EQL || p.Op == token.NEQ
	case *ast.CallExpr:
		for _, name := range gitPrefixReads {
			if s.isPkg(p.Fun, "strings", name) && len(p.Args) == 2 && p.Args[1] == lit {
				return true
			}
		}
		for _, pkg := range []string{"os", "syscall"} {
			if (s.isPkg(p.Fun, pkg, "Getenv") || s.isPkg(p.Fun, pkg, "LookupEnv")) && len(p.Args) == 1 && p.Args[0] == lit {
				return true
			}
		}
	}
	return false
}

// isSetenvKey reports a literal that is the key of a Setenv call, which the
// first question judges.
func (s *scope) isSetenvKey(lit *ast.BasicLit) bool {
	call, ok := s.parents[lit].(*ast.CallExpr)
	if !ok || len(call.Args) != 2 || call.Args[0] != lit {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Setenv"
}

// isKeyConst reports a literal that is the value of a constant used as a
// Setenv key and nowhere else (setenvOnlyConsts).
func (s *scope) isKeyConst(lit *ast.BasicLit, keyConsts map[string]bool) bool {
	spec, ok := s.parents[lit].(*ast.ValueSpec)
	if !ok {
		return false
	}
	gen, ok := s.parents[spec].(*ast.GenDecl)
	if !ok || gen.Tok != token.CONST || len(spec.Values) != len(spec.Names) {
		return false
	}
	for i, v := range spec.Values {
		if v == lit {
			return keyConsts[spec.Names[i].Name]
		}
	}
	return false
}

// setenvOnlyConsts names the string constants of one package whose value
// starts with GIT_ and that are used as the key of a Setenv call and nowhere
// else, read by name over the package's judged files.
func setenvOnlyConsts(files []*ast.File) map[string]bool {
	gitConsts := map[string]bool{}
	declared := map[*ast.Ident]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			gen, ok := n.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				return true
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != len(vs.Names) {
					continue
				}
				for i, name := range vs.Names {
					if v, ok := stringLiteral(vs.Values[i]); ok && strings.HasPrefix(v, "GIT_") {
						gitConsts[name.Name] = true
						declared[name] = true
					}
				}
			}
			return true
		})
	}
	keyUse, otherUse := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return false
			}
			if id, ok := n.(*ast.Ident); ok && gitConsts[id.Name] && !declared[id] {
				isKey := false
				if call, ok := stack[len(stack)-1].(*ast.CallExpr); ok && len(call.Args) == 2 && call.Args[0] == id {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					isKey = ok && sel.Sel.Name == "Setenv"
				}
				if isKey {
					keyUse[id.Name] = true
				} else {
					otherUse[id.Name] = true
				}
			}
			stack = append(stack, n)
			return true
		})
	}
	out := map[string]bool{}
	for name := range keyUse {
		if !otherUse[name] {
			out[name] = true
		}
	}
	return out
}

// envEntries is the third question, see gitSetenvRefusals. An environment is
// read where it is built: an append or a slice literal written here, or a
// local variable whose every binding in this declaration is one and which is
// used only to build it and to hand it over. Anything else (a call, a field,
// a parameter, a variable assigned from a call) cannot be read and is
// refused, unless its site is vouched for in envSourcePins.
func (s *scope) envEntries() []refusal {
	var out []refusal
	for _, w := range s.writes {
		out = append(out, refusal{pos: w.Pos(), why: gotypes.ExprString(w.(ast.Expr)) + " changes the entries of an environment after it was handed over, so one may become a GIT_* variable; build the environment whole before assigning it (#958)"})
	}
	for _, env := range s.envs {
		entries, refusals := s.envBuiltBy(env.value, true, env.target)
		out = append(out, refusals...)
		for _, e := range entries {
			if why := s.entryKeyProblem(e); why != "" {
				out = append(out, refusal{pos: e.Pos(), why: why, entry: true})
			}
		}
	}
	return out
}

// envBuiltBy returns the entries an environment expression adds, and what
// cannot be read. local allows a local variable, read through its bindings,
// one level deep: each binding must itself be an append or a slice literal.
// The list an append extends (os.Environ(), the variable itself) is not an
// entry; its GIT_* literals are judged where they are written.
//
// self is what the environment is assigned to (X.Env, or the local variable
// being read): `env = append(env, ...)` extends itself. os.Environ() is the
// process's environment, from which TestMain dropped every GIT_*; nil is
// empty.
func (s *scope) envBuiltBy(env ast.Expr, local bool, self ast.Expr) ([]ast.Expr, []refusal) {
	if self != nil && sameExpr(env, self) {
		return nil, nil
	}
	switch x := env.(type) {
	case *ast.CallExpr:
		if f, ok := x.Fun.(*ast.Ident); ok && f.Name == "append" && f.Obj == nil && len(x.Args) > 0 {
			return s.appended(x, local, self)
		}
		if s.isPkg(x.Fun, "os", "Environ") && len(x.Args) == 0 {
			return nil, nil
		}
	case *ast.CompositeLit:
		return x.Elts, nil
	case *ast.Ident:
		if x.Name == "nil" && x.Obj == nil {
			return nil, nil
		}
		if local && x.Obj != nil && s.declaredIn(x.Obj) && !s.params[x.Obj] {
			return s.localEnv(x)
		}
	}
	src := gotypes.ExprString(env)
	return nil, []refusal{{pos: env.Pos(), why: "an environment is taken from " + src + ", which this guard cannot read, so an entry may be a GIT_* variable; build it here with append or a slice literal, or vouch for the site in envSourcePins (#958)", source: true, src: src}}
}

// sameExpr reports two expressions naming the same thing: the same local
// object, or the same text.
func sameExpr(a, b ast.Expr) bool {
	ai, aok := a.(*ast.Ident)
	bi, bok := b.(*ast.Ident)
	if aok && bok && ai.Obj != nil && bi.Obj != nil {
		return ai.Obj == bi.Obj
	}
	return gotypes.ExprString(a) == gotypes.ExprString(b)
}

// sourceVouched reports a pin of envSourcePins for this function and this
// very source text: a source added later to a pinned function is refused.
func sourceVouched(site, src string) bool {
	for key := range envSourcePins {
		fn, text, _ := strings.Cut(key, ": ")
		if fn == site && text == src {
			return true
		}
	}
	return false
}

// appended returns the entries an append adds. A list spread in is read
// entry by entry when it is a slice literal, accepted when it is a parameter
// (its callers write the entries), and refused otherwise.
//
// The list it extends is judged by the same rules as an environment (#958,
// cycle 3): a file's lines extended by one entry are still a file's lines.
func (s *scope) appended(x *ast.CallExpr, local bool, self ast.Expr) ([]ast.Expr, []refusal) {
	entries, refusals := s.envBuiltBy(x.Args[0], local, self)
	entries = append(slices.Clone(entries), x.Args[1:]...)
	if !x.Ellipsis.IsValid() || len(x.Args) < 2 {
		return entries, refusals
	}
	last := entries[len(entries)-1]
	entries = entries[:len(entries)-1]
	id, isIdent := last.(*ast.Ident)
	if lit, ok := last.(*ast.CompositeLit); ok {
		entries = append(entries, lit.Elts...)
	} else if !isIdent || id.Obj == nil || !s.params[id.Obj] {
		refusals = append(refusals, refusal{pos: last.Pos(), why: "entries spread into an environment from a list this guard cannot read, so one may be a GIT_* variable; append them one by one (#958)", entry: true})
	}
	return entries, refusals
}

// localEnv reads an environment held in a local variable: the entries of
// every binding, and a refusal for a binding or a use it cannot follow.
func (s *scope) localEnv(id *ast.Ident) ([]ast.Expr, []refusal) {
	var entries []ast.Expr
	var out []refusal
	cannot := func(why string) {
		out = append(out, refusal{pos: id.Pos(), why: "the environment " + id.Name + " " + why + ", so an entry may be a GIT_* variable; build it with append or a slice literal in this function (#958)", source: true, src: id.Name})
	}
	for _, b := range s.bindings[id.Obj] {
		switch {
		case b.addressed:
			cannot("has its address taken")
		case b.rangeOver != nil:
			cannot("is bound by a range clause")
		case b.value == nil && !b.zero:
			cannot("is assigned from a call that returns several values")
		case b.value == nil:
		default:
			e, r := s.envBuiltBy(b.value, false, id)
			entries = append(entries, e...)
			out = append(out, r...)
		}
	}
	for _, use := range s.u.idents[id.Obj] {
		if !s.envUseAllowed(use) {
			cannot("is used where its entries may change")
			break
		}
	}
	return entries, out
}

// envUseAllowed reports a use of an environment variable that neither changes
// its entries nor hands it to code that might: its declaration or binding,
// the list an append extends, the right-hand side of X.Env =, an Env: field,
// or what a range reads.
func (s *scope) envUseAllowed(use *ast.Ident) bool {
	switch p := s.parents[use].(type) {
	case *ast.ValueSpec:
		return slices.Contains(p.Names, use)
	case *ast.AssignStmt:
		if slices.Contains(p.Lhs, ast.Expr(use)) {
			return true
		}
		for i, r := range p.Rhs {
			if r == use && len(p.Lhs) == len(p.Rhs) {
				sel, ok := p.Lhs[i].(*ast.SelectorExpr)
				return ok && sel.Sel.Name == "Env"
			}
		}
	case *ast.CallExpr:
		f, ok := p.Fun.(*ast.Ident)
		return ok && f.Name == "append" && f.Obj == nil && len(p.Args) > 0 && p.Args[0] == use
	case *ast.KeyValueExpr:
		k, ok := p.Key.(*ast.Ident)
		return ok && k.Name == "Env" && p.Value == use
	case *ast.RangeStmt:
		return p.X == use || p.Key == use || p.Value == use
	}
	return false
}

// entryKeyProblem says why the key of one entry added to an environment
// cannot be read, or nothing when it can.
func (s *scope) entryKeyProblem(e ast.Expr) string {
	head := e
	for {
		bin, ok := head.(*ast.BinaryExpr)
		if !ok || bin.Op != token.ADD {
			break
		}
		head = bin.X
	}
	if v, ok := s.constantKey(head); ok && strings.Contains(v, "=") {
		return ""
	}
	// KEY + "=" + value or KEY + "=...", KEY a constant: readable, and a
	// GIT_* KEY must be written as the literal "GIT_X=" so that the second
	// question judges its value.
	if v, ok := s.constantKey(head); ok {
		if bin, isBin := e.(*ast.BinaryExpr); isBin {
			if inner, isInner := bin.X.(*ast.BinaryExpr); isInner && inner.X == head {
				if sep, isSep := stringLiteral(inner.Y); isSep && sep == "=" && !strings.HasPrefix(v, "GIT_") {
					return ""
				}
			}
		}
		if _, isSep := entrySeparator(e, head); isSep && !strings.HasPrefix(v, "GIT_") {
			return ""
		}
	}
	if id, ok := head.(*ast.Ident); ok {
		if bin, ok := e.(*ast.BinaryExpr); ok {
			if inner, ok := bin.X.(*ast.BinaryExpr); ok && inner.X == id {
				if sep, ok := stringLiteral(inner.Y); ok && sep == "=" {
					keys, readable := s.rangedKeys(id)
					if readable && !slices.ContainsFunc(keys, func(k string) bool { return strings.HasPrefix(k, "GIT_") }) {
						return ""
					}
				}
			}
		}
	}
	return "an entry is added to an environment with a key this guard cannot read, so it may be a GIT_* variable; lead it with the key as a literal (#958)"
}

// entrySeparator reads KEY + "=..." where KEY is head and the right operand is
// a literal opening with "=": readAfterClose+"=0".
func entrySeparator(e, head ast.Expr) (string, bool) {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD || bin.X != head {
		return "", false
	}
	v, ok := stringLiteral(bin.Y)
	return v, ok && strings.HasPrefix(v, "=")
}

// isPkg reports pkg.name, pkg being the import path's name in this file and
// not a local name that shadows it (an import is never resolved to an
// object, a local is).
func (s *scope) isPkg(e ast.Expr, path, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Obj == nil && s.alias[path] != "" && x.Name == s.alias[path]
}

// isEnvHolder reports a literal type whose Env field is a child's
// environment: exec.Cmd, os.ProcAttr, syscall.ProcAttr. A machine.Spec{Env:}
// is a container's, not a child process's of the test.
func (s *scope) isEnvHolder(typ ast.Expr) bool {
	return s.isPkg(typ, "os/exec", "Cmd") || s.isPkg(typ, "os", "ProcAttr") || s.isPkg(typ, "syscall", "ProcAttr")
}

// testingParamKind is "star" for *testing.T, *testing.B and *testing.F, "tb"
// for testing.TB, and empty otherwise. Only the first may call TempDir: a
// testing.TB is an interface any type can implement, with a TempDir that
// returns any path.
func testingParamKind(typ ast.Expr, testingName string) string {
	if testingName == "" {
		return ""
	}
	if star, ok := typ.(*ast.StarExpr); ok {
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		x, ok := sel.X.(*ast.Ident)
		if ok && x.Name == testingName && (sel.Sel.Name == "T" || sel.Sel.Name == "B" || sel.Sel.Name == "F") {
			return "star"
		}
		return ""
	}
	sel, ok := typ.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	x, ok := sel.X.(*ast.Ident)
	if ok && x.Name == testingName && sel.Sel.Name == "TB" {
		return "tb"
	}
	return ""
}

// stringLiteral unquotes a string literal.
func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}

// constantKey reads a key: a string literal, or the name of a string
// constant. A name the file resolves is a constant only if its declaration is
// a const spec; a name it leaves unresolved is looked up among the
// package-level constants of the same package's other files.
func (s *scope) constantKey(e ast.Expr) (string, bool) {
	if v, ok := stringLiteral(e); ok {
		return v, true
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return "", false
	}
	if id.Obj == nil {
		v, ok := s.consts[id.Name]
		return v, ok
	}
	spec, ok := id.Obj.Decl.(*ast.ValueSpec)
	if id.Obj.Kind != ast.Con || !ok || len(spec.Values) != len(spec.Names) {
		return "", false
	}
	for i, n := range spec.Names {
		if n.Name == id.Name {
			return stringLiteral(spec.Values[i])
		}
	}
	return "", false
}

// declaredIn reports a name declared inside this declaration: a package-level
// variable can be reassigned by any other function, so one assignment seen
// here proves nothing.
//
//nolint:staticcheck // SA1019: see fieldKey (#958)
func (s *scope) declaredIn(obj *ast.Object) bool {
	d, ok := obj.Decl.(ast.Node)
	return ok && d.Pos() >= s.node.Pos() && d.End() <= s.node.End()
}

// onlyBinding is the one binding of a local name declared in this
// declaration, or false.
func (s *scope) onlyBinding(id *ast.Ident) (binding, bool) {
	if id.Obj == nil || !s.declaredIn(id.Obj) {
		return binding{}, false
	}
	bs := s.bindings[id.Obj]
	if len(bs) != 1 {
		return binding{}, false
	}
	return bs[0], true
}

// derived reports a value that can only name a directory under t.TempDir(),
// see gitSetenvRefusals.
func (s *scope) derived(e ast.Expr, depth int) bool {
	if depth > 16 {
		return false
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.derived(x.X, depth+1)
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return false
		}
		if sel.Sel.Name == "TempDir" && len(x.Args) == 0 {
			return recv.Obj != nil && s.tempDirs[recv.Obj]
		}
		if s.isPkg(x.Fun, "path/filepath", "Join") && len(x.Args) > 0 {
			for _, arg := range x.Args[1:] {
				v, ok := stringLiteral(arg)
				if !ok || slices.Contains(strings.Split(filepath.ToSlash(v), "/"), "..") {
					return false
				}
			}
			return s.derived(x.Args[0], depth+1)
		}
	case *ast.Ident:
		b, ok := s.onlyBinding(x)
		if !ok || b.value == nil {
			return false
		}
		return s.derived(b.value, depth+1)
	}
	return false
}

// rangedKeys answers for a key that is not a constant: when it is the key of
// a range clause, bound nowhere else, over a map whose keys this guard can
// read, it returns those keys. A map is readable when it is written in the
// test as a map literal with constant keys, reached directly, through a name
// assigned once, or as the field of a table of struct literals the test
// ranges over (`for _, tc := range []struct{...}{...}` then
// `range tc.field`); or when it is FakeCredentials(t) of
// internal/conformancetest, whose keys are SCW_* by construction
// (fakeCredentialsKeepsScalewayKeys).
func (s *scope) rangedKeys(e ast.Expr) ([]string, bool) {
	id, ok := e.(*ast.Ident)
	if !ok {
		return nil, false
	}
	b, ok := s.onlyBinding(id)
	if !ok || b.rangeOver == nil {
		return nil, false
	}
	if k, ok := b.rangeOver.Key.(*ast.Ident); !ok || k.Name != id.Name {
		return nil, false
	}
	return s.mapKeys(b.rangeOver.X, 0)
}

// mapKeys reads the keys of a map expression, see rangedKeys.
func (s *scope) mapKeys(e ast.Expr, depth int) ([]string, bool) {
	if depth > 16 {
		return nil, false
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.mapKeys(x.X, depth+1)
	case *ast.CompositeLit:
		if _, ok := x.Type.(*ast.MapType); !ok {
			return nil, false
		}
		var keys []string
		for _, elt := range x.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				return nil, false
			}
			k, ok := s.constantKey(kv.Key)
			if !ok {
				return nil, false
			}
			keys = append(keys, k)
		}
		return keys, true
	case *ast.Ident:
		b, ok := s.onlyBinding(x)
		if !ok || b.value == nil || s.u.escapes(x.Obj) {
			return nil, false
		}
		return s.mapKeys(b.value, depth+1)
	case *ast.CallExpr:
		if s.isPkg(x.Fun, conformancetestImport, "FakeCredentials") {
			return nil, true
		}
		if f, ok := x.Fun.(*ast.Ident); ok && f.Name == "FakeCredentials" && f.Obj == nil && s.dir == conformancetestDir {
			return nil, true
		}
	case *ast.SelectorExpr:
		return s.tableFieldKeys(x, depth)
	}
	return nil, false
}

// tableFieldKeys reads tc.field where tc ranges over a slice literal of
// struct literals whose struct type is written inline: the keys of every
// row's field, keyed or positional.
func (s *scope) tableFieldKeys(sel *ast.SelectorExpr, depth int) ([]string, bool) {
	row, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil, false
	}
	b, ok := s.onlyBinding(row)
	if !ok || b.rangeOver == nil || s.u.fieldEscapes(row.Obj, sel.Sel.Name) {
		return nil, false
	}
	if v, ok := b.rangeOver.Value.(*ast.Ident); !ok || v.Name != row.Name {
		return nil, false
	}
	table, ok := b.rangeOver.X.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	arr, ok := table.Type.(*ast.ArrayType)
	if !ok {
		return nil, false
	}
	st, ok := arr.Elt.(*ast.StructType)
	if !ok {
		return nil, false
	}
	index := -1
	var i int
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == sel.Sel.Name {
				index = i
			}
			i++
		}
	}
	if index < 0 {
		return nil, false
	}
	var keys []string
	for _, elt := range table.Elts {
		lit, ok := elt.(*ast.CompositeLit)
		if !ok {
			return nil, false
		}
		var field ast.Expr
		for j, v := range lit.Elts {
			if kv, ok := v.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == sel.Sel.Name {
					field = kv.Value
				}
			} else if j == index {
				field = v
			}
		}
		if field == nil {
			continue
		}
		k, ok := s.mapKeys(field, depth+1)
		if !ok {
			return nil, false
		}
		keys = append(keys, k...)
	}
	return keys, true
}

// fakeCredentialsKeepsScalewayKeys is the condition under which rangedKeys
// may accept a key: FakeCredentials, in the build that compiles it (-tags
// conformance), writes its map only under an `if` whose condition is
// strings.HasPrefix(K, "SCW_") and only at index K. A HasPrefix elsewhere in
// the body filters nothing. Empty when it holds; otherwise the reason.
func fakeCredentialsKeepsScalewayKeys(files map[string][]byte) string {
	ctx := buildContext(files, []string{"conformance"})
	for _, name := range slices.Sorted(maps.Keys(files)) {
		ok, err := ctx.MatchFile(conformancetestDir, name)
		if err != nil || !ok {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, files[name], parser.SkipObjectResolution)
		if err != nil {
			return conformancetestDir + "/" + name + " does not parse: " + err.Error()
		}
		alias := importNames(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "FakeCredentials" || fn.Body == nil {
				continue
			}
			writes, guarded := 0, 0
			var stack []ast.Node
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return false
				}
				if as, ok := n.(*ast.AssignStmt); ok {
					for _, l := range as.Lhs {
						ix, ok := l.(*ast.IndexExpr)
						if !ok {
							continue
						}
						writes++
						if k, ok := ix.Index.(*ast.Ident); ok && guardedBy(stack, k.Name, alias["strings"]) {
							guarded++
						}
					}
				}
				stack = append(stack, n)
				return true
			})
			if writes > 0 && guarded == writes {
				return ""
			}
			return conformancetestDir + "/" + name + ": FakeCredentials no longer keeps SCW_* keys alone (every map write under if strings.HasPrefix(key, \"SCW_\")), so the Setenv guard accepts, for a key ranging over it, a GIT_* variable read from a file"
		}
	}
	return conformancetestDir + " has no FakeCredentials in the build with -tags conformance, so the Setenv guard's one accepted non-constant key has no source to vouch for it"
}

// guardedBy reports an enclosing `if` whose condition holds, joined by &&,
// strings.HasPrefix(key, "SCW_").
func guardedBy(stack []ast.Node, key, stringsName string) bool {
	for _, n := range stack {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			continue
		}
		var conds []ast.Expr
		var flatten func(e ast.Expr)
		flatten = func(e ast.Expr) {
			if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.LAND {
				flatten(b.X)
				flatten(b.Y)
				return
			}
			conds = append(conds, e)
		}
		flatten(ifs.Cond)
		for _, c := range conds {
			call, ok := c.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HasPrefix" {
				continue
			}
			x, ok := sel.X.(*ast.Ident)
			arg, isIdent := call.Args[0].(*ast.Ident)
			if v, isLit := stringLiteral(call.Args[1]); ok && x.Name == stringsName && isIdent && arg.Name == key && isLit && v == "SCW_" {
				return true
			}
		}
	}
	return false
}
