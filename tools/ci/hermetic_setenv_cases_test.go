package ci

import (
	"strings"
	"testing"
)

// fixture is a test file of package pkg importing os, os/exec, path/filepath,
// strings and testing (each used once so the file compiles in spirit), plus
// extra imports, then body.
func fixture(pkg, extra, body string) []byte {
	return []byte("package " + pkg + "\n\nimport (\n\t\"os\"\n\t\"os/exec\"\n\t\"path/filepath\"\n\t\"strings\"\n\t\"testing\"\n" + extra + ")\n\nvar _ = os.Exit\nvar _ = exec.Command\nvar _ = filepath.Join\nvar _ = strings.Fields\nvar _ testing.T\n\n" + body)
}

// inTest wraps body in func TestX(t *testing.T) of package x.
func inTest(body string) []byte {
	return fixture("x", "", "func TestX(t *testing.T) {\n"+body+"\n}\n")
}

// refusalsMatch pairs every expected reason with one refusal containing it,
// and requires no refusal left over: a mutant that silences one question
// changes the set even when another question refuses the same case.
func refusalsMatch(refusals, reasons []string) bool {
	if len(refusals) != len(reasons) {
		return false
	}
	used := make([]bool, len(refusals))
	for _, r := range reasons {
		found := false
		for i, x := range refusals {
			if !used[i] && strings.Contains(x, r) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// The reasons the three questions give, by a substring each owns.
const (
	setenvNotDerived = ", ...) takes a value not derived from t.TempDir()"
	notConstant      = "a key that is not a constant"
	asValue          = "Setenv is used as a value"
	mapHoldsGit      = "ranging over a map that holds \"GIT_DIR\""
	literalOutside   = "outside every shape this guard reads"
	namesValue       = "names a value not derived from t.TempDir()"
	entryNotDerived  = "= takes a value not derived from t.TempDir()"
	unreadableShape  = "sits in a shape this guard cannot read"
	unreadableKey    = "with a key this guard cannot read"
	spread           = "entries spread into an environment"
	source           = "which this guard cannot read, so an entry may be"
	addressTaken     = "has its address taken"
	rangeBound       = "is bound by a range clause"
	severalValues    = "is assigned from a call that returns several values"
	entriesMayChange = "is used where its entries may change"
	changesEntries   = "changes the entries of an environment after it was handed over"
)

type guardCase struct {
	name    string
	dir     string // empty: tools/x
	files   map[string][]byte
	tags    []string
	reasons []string // nil: accepted
}

func runGuardCases(t *testing.T, cases []guardCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir
			if dir == "" {
				dir = "tools/x"
			}
			refusals, err := gitSetenvRefusals(dir, tc.files, tc.tags)
			if err != nil {
				t.Fatalf("the case does not parse, so it measures nothing: %v", err)
			}
			if !refusalsMatch(refusals, tc.reasons) {
				t.Errorf("refused %q, want exactly one refusal for each of %q", refusals, tc.reasons)
			}
		})
	}
}

func one(src []byte) map[string][]byte { return map[string][]byte{"a_test.go": src} }

// TestTheSetenvGuardAsksEachQuestion: one case per question of the first two
// questions on Setenv, each with the reasons every question gives, and the
// accepting half beside it: the decoy uses of the repository as they are
// written, and the tables of non-GIT keys.
func TestTheSetenvGuardAsksEachQuestion(t *testing.T) {
	const real = `"/home/someone/project/.git"`
	runGuardCases(t, []guardCase{
		{name: "tracked_hook_test.go's decoy, a t.TempDir() and a join under it, is accepted", files: one(inTest(`
	decoy := t.TempDir()
	index := filepath.Join(decoy, "index")
	t.Setenv("GIT_DIR", decoy)
	t.Setenv("GIT_INDEX_FILE", index)`))},
		{name: "throwaway_git_test.go's decoy, a join of the directory it created, is accepted", files: one(inTest(`
	decoyDir := t.TempDir()
	decoy := newRepo(t, decoyDir)
	_ = decoy
	t.Setenv("GIT_DIR", filepath.Join(decoyDir, ".git"))`))},
		{name: "os.Setenv of a GIT_* key under t.TempDir() is accepted", files: one(inTest(`
	_ = os.Setenv("GIT_WORK_TREE", t.TempDir())`))},
		{name: "a key that is not GIT_* takes any value", files: one(inTest(`
	t.Setenv("HOME", "/root")`))},
		{name: "a constant key from another file that is not GIT_* takes any value", files: map[string][]byte{
			"k_test.go": fixture("x", "", `const childKey = "FEINT_CHILD"`+"\n"),
			"a_test.go": inTest(`	t.Setenv(childKey, os.Getenv("HOME"))`),
		}},
		{name: "a GIT_* constant used only as a Setenv key, with a value from t.TempDir(), is accepted", files: map[string][]byte{
			"k_test.go": fixture("x", "", `const gitKey = "GIT_DIR"`+"\n"),
			"a_test.go": inTest(`	t.Setenv(gitKey, t.TempDir())`),
		}},
		{name: "a table of literal keys none of which is GIT_* is accepted (up_env_precedence_test.go)", files: one(inTest(`
	for _, tc := range []struct {
		p       string
		planted map[string]string
	}{
		{"a", map[string]string{"SCW_ACCESS_KEY": "x"}},
		{p: "b", planted: map[string]string{"OSC_ACCESS_KEY": "y"}},
	} {
		for name, value := range tc.planted {
			t.Setenv(name, value)
		}
		for name := range tc.planted {
			_ = name
		}
	}`))},
		{name: "a key ranging over FakeCredentials is accepted (run_terraform_conformance_test.go)", files: one(fixture("x", "\t\""+conformancetestImport+"\"\n", `func TestX(t *testing.T) {
	fake := conformancetest.FakeCredentials(t)
	for name, value := range fake {
		t.Setenv(name, value)
	}
}
`))},
		{name: "a GIT_* key set to a path anybody works in is refused", files: one(inTest(`	t.Setenv("GIT_DIR", ` + real + `)`)), reasons: []string{setenvNotDerived}},
		{name: "a GIT_* key named by a constant of another file is read through the constant", files: map[string][]byte{
			"k_test.go": fixture("x", "", `const gitKey = "GIT_DIR"`+"\n"),
			"a_test.go": inTest(`	t.Setenv(gitKey, ` + real + `)`),
		}, reasons: []string{setenvNotDerived}},
		{name: "a GIT_* key named by a local constant is read through the constant", files: one(inTest(`
	const k = "GIT_INDEX_FILE"
	t.Setenv(k, ` + real + `)`)), reasons: []string{setenvNotDerived}},
		{name: "constants are read per package: x_test's own is not shadowed by x's", files: map[string][]byte{
			"a_test.go": fixture("x_test", "", `func TestX(t *testing.T) { t.Setenv(key, `+real+`) }`+"\n"),
			"c_test.go": fixture("x_test", "", `const key = "GIT_DIR"`+"\n"),
			"z_test.go": fixture("x", "", `const key = "FEINT_HARMLESS"`+"\n"),
		}, reasons: []string{setenvNotDerived}},
		{name: "a name assigned from t.TempDir() and then reassigned is refused", files: one(inTest(`
	decoy := t.TempDir()
	decoy = ` + real + `
	t.Setenv("GIT_DIR", decoy)`)), reasons: []string{setenvNotDerived}},
		{name: "a name whose address is taken is refused", files: one(inTest(`
	decoy := t.TempDir()
	p := &decoy
	_ = p
	t.Setenv("GIT_DIR", decoy)`)), reasons: []string{setenvNotDerived}},
		{name: "a package-level variable another function reassigns is refused", files: one(fixture("x", "", `var decoy string

func init() { decoy = `+real+` }

func TestX(t *testing.T) {
	decoy = t.TempDir()
	t.Setenv("GIT_DIR", decoy)
}
`)), reasons: []string{setenvNotDerived}},
		{name: "a join that climbs out of t.TempDir() is refused", files: one(inTest(`
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "..", "..", "home"))`)), reasons: []string{setenvNotDerived}},
		{name: "a join with a part that is not a literal is refused", files: one(inTest(`
	rel := os.Getenv("REL")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), rel))`)), reasons: []string{setenvNotDerived}},
		{name: "a Join method of a local type is not filepath.Join", files: one(fixture("x", "", `type j struct{}

func (j) Join(parts ...string) string { return "/home/someone/project/.git" }

func TestX(t *testing.T) {
	r := j{}
	t.Setenv("GIT_DIR", r.Join(t.TempDir()))
}
`)), reasons: []string{setenvNotDerived}},
		{name: "a package-level value of another file with a Join method is not filepath", files: map[string][]byte{
			"j_test.go": fixture("x", "", `type j struct{}

func (j) Join(parts ...string) string { return "/home/someone/project/.git" }

var r = j{}
`),
			"a_test.go": inTest(`	t.Setenv("GIT_DIR", r.Join(t.TempDir()))`),
		}, reasons: []string{setenvNotDerived}},
		{name: "a FakeCredentials outside internal/conformancetest is not the package's", files: map[string][]byte{
			"f_test.go": fixture("x", "", `func FakeCredentials(t *testing.T) map[string]string { return nil }`+"\n"),
			"a_test.go": inTest(`	for k, v := range FakeCredentials(t) {
		t.Setenv(k, v)
	}`),
		}, reasons: []string{notConstant}},
		{name: "a local variable named filepath is not the package", files: one(fixture("x", "", `type j struct{}

func (j) Join(parts ...string) string { return "/home/someone/project/.git" }

func TestX(t *testing.T) {
	filepath := j{}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir()))
}
`)), reasons: []string{setenvNotDerived}},
		{name: "TempDir called on something that is not the test is refused", files: one(fixture("x", "", `type fake struct{}

func (fake) TempDir() string { return "/home/someone" }

func TestX(t *testing.T) {
	f := fake{}
	t.Setenv("GIT_DIR", f.TempDir())
}
`)), reasons: []string{setenvNotDerived}},
		{name: "TempDir on a testing.TB, which any type can implement, is refused", files: one(fixture("x", "", `func plant(tb testing.TB) {
	tb.Setenv("GIT_DIR", tb.TempDir())
}
`)), reasons: []string{setenvNotDerived}},
		{name: "a key that is not a constant is refused", files: one(inTest(`
	key := os.Getenv("WHICH")
	t.Setenv(key, "x")`)), reasons: []string{notConstant}},
		{name: "a local variable named like a constant of another file is not that constant", files: map[string][]byte{
			"k_test.go": fixture("x", "", `const childKey = "FEINT_CHILD"`+"\n"),
			"a_test.go": inTest(`	childKey := os.Getenv("WHICH")
	t.Setenv(childKey, "x")`),
		}, reasons: []string{notConstant}},
		{name: "a package-level variable is not a constant, whatever its first value", files: map[string][]byte{
			"k_test.go": fixture("x", "", `var childKey = "FEINT_CHILD"

func init() { childKey = "GIT_DIR" }
`),
			"a_test.go": inTest(`	t.Setenv(childKey, ` + real + `)`),
		}, reasons: []string{notConstant, literalOutside}},
		{name: "a key ranging over a map built at run time is refused", files: one(inTest(`
	m := map[string]string{}
	m[os.Getenv("K")] = "v"
	for k, v := range m {
		t.Setenv(k, v)
	}`)), reasons: []string{notConstant}},
		{name: "a key ranging over a literal map that holds a GIT_* key is refused", files: one(inTest(`
	for k, v := range map[string]string{"HOME": "/root", "GIT_DIR": ` + real + `} {
		t.Setenv(k, v)
	}`)), reasons: []string{mapHoldsGit, literalOutside}},
		{name: "a map's value used as the key is not one of its keys", files: one(inTest(`
	for _, v := range map[string]string{"HOME": "GIT_DIR"} {
		t.Setenv(v, ` + real + `)
	}`)), reasons: []string{notConstant, literalOutside}},
		{name: "a table field handed to a function, which may add a key, is refused", files: one(fixture("x", "", `func fill(m map[string]string) {}

func TestX(t *testing.T) {
	for _, tc := range []struct{ planted map[string]string }{{map[string]string{"HOME": "/root"}}} {
		fill(tc.planted)
		for k, v := range tc.planted {
			t.Setenv(k, v)
		}
	}
}
`)), reasons: []string{notConstant}},
		{name: "a table row handed whole to a function is refused", files: one(fixture("x", "", `func fill(r any) {}

func TestX(t *testing.T) {
	for _, tc := range []struct{ planted map[string]string }{{map[string]string{"HOME": "/root"}}} {
		fill(tc)
		for k, v := range tc.planted {
			t.Setenv(k, v)
		}
	}
}
`)), reasons: []string{notConstant}},
		{name: "a table whose struct type is declared elsewhere is a shape the guard does not read", files: one(fixture("x", "", `type row struct{ planted map[string]string }

func TestX(t *testing.T) {
	for _, tc := range []row{{map[string]string{"HOME": "/root"}}} {
		for k, v := range tc.planted {
			t.Setenv(k, v)
		}
	}
}
`)), reasons: []string{notConstant}},
		{name: "a key ranging over the map a method returns is refused", files: one(fixture("x", "", `type registry struct{}

func (registry) load() map[string]string { return nil }

func TestX(t *testing.T) {
	r := registry{}
	for k, v := range r.load() {
		t.Setenv(k, v)
	}
}
`)), reasons: []string{notConstant}},
		{name: "a key ranging over the map a plain function returns is refused", files: one(fixture("x", "", `func load() map[string]string { return map[string]string{"GIT_DIR": `+real+`} }

func TestX(t *testing.T) {
	for k, v := range load() {
		t.Setenv(k, v)
	}
}
`)), reasons: []string{notConstant, literalOutside}},
		{name: "a local variable named conformancetest is not the package", files: one(fixture("x", "\t\""+conformancetestImport+"\"\n", `type reg struct{}

func (reg) FakeCredentials(t *testing.T) map[string]string { return nil }

func TestX(t *testing.T) {
	conformancetest := reg{}
	for k, v := range conformancetest.FakeCredentials(t) {
		t.Setenv(k, v)
	}
}
`)), reasons: []string{notConstant}},
		{name: "Setenv taken as a value is refused", files: one(inTest(`
	set := t.Setenv
	set("GIT_DIR", t.TempDir())`)), reasons: []string{asValue, literalOutside}},
		{name: "a Setenv in a package-level function literal is judged", files: one(fixture("x", "", `var plant = func(t *testing.T) { t.Setenv("GIT_DIR", `+real+`) }`+"\n")), reasons: []string{setenvNotDerived}},
		{name: "a Setenv in a file only the tagged build compiles is judged in that build", files: one(append([]byte("//go:build hermeticselftest\n\n"), inTest(`	t.Setenv("GIT_DIR", `+real+`)`)...)), tags: []string{"hermeticselftest"}, reasons: []string{setenvNotDerived}},
		{name: "the same file is not in the default build", files: one(append([]byte("//go:build hermeticselftest\n\n"), inTest(`	t.Setenv("GIT_DIR", `+real+`)`)...))},
		{name: "an os.Setenv in a non-test file of an ordinary package is the product's, not judged", files: map[string][]byte{"lib.go": []byte("package x\n\nimport \"os\"\n\nfunc plant() { _ = os.Setenv(\"GIT_DIR\", " + real + ") }\n")}},
		{name: "a t.Setenv in a non-test file, on a testing parameter, is judged", files: map[string][]byte{"lib.go": []byte("package x\n\nimport \"testing\"\n\nfunc Plant(t *testing.T) { t.Setenv(\"GIT_DIR\", " + real + ") }\n")}, reasons: []string{setenvNotDerived}},
		{name: "a GIT_* constant used elsewhere than as a Setenv key is refused", files: one(fixture("x", "", `const k = "GIT_DIR"

func TestX(t *testing.T) {
	t.Setenv(k, t.TempDir())
	_ = os.Getenv(k)
}
`)), reasons: []string{literalOutside}},
	})
}

// TestTheEnvironmentEntryGuardAsksEachQuestion: one case per question of the
// literal and environment-entry questions, the three decoy uses of the
// repository beside them, and one case for each shape a falsifier and a
// reviewer measured as accepted on 2026-11-15 before the literal question.
func TestTheEnvironmentEntryGuardAsksEachQuestion(t *testing.T) {
	const real = `"/home/someone/project/.git"`
	runGuardCases(t, []guardCase{
		{name: "falsify_harness_test.go's decoy, a join under t.TempDir(), is accepted", files: one(inTest(`
	cmd := exec.Command("git")
	decoy := filepath.Join(t.TempDir(), "decoy.git")
	cmd.Env = append(os.Environ(), "GIT_DIR="+decoy, "GIT_INDEX_FILE="+filepath.Join(decoy, "index"))`))},
		{name: "hermeticGitEnv's entries, which name no repository, are accepted", files: one(fixture("x", "", `func env() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t")
}
`))},
		{name: "reads of a GIT_* name are accepted", files: one(fixture("x", "", `func dropped(kv, key string) bool {
	_ = os.Getenv("GIT_DIR")
	return strings.HasPrefix(kv, "GIT_") || key == "GIT_CONFIG_GLOBAL"
}
`))},
		{name: "a constant key joined with \"=\" is a key the guard reads", files: one(fixture("x", "", `const lockRoot = "FEINT_LOCK_ROOT"

func TestX(t *testing.T) {
	cmd := exec.Command("x")
	cmd.Env = append(os.Environ(), lockRoot+"="+os.Getenv("P"), lockRoot+"=0")
}
`))},
		{name: "an entry built from a range over literal keys none of which is GIT_* is accepted", files: one(inTest(`
	cmd := exec.Command("x")
	for k, v := range map[string]string{"HOME": "/root"} {
		cmd.Env = append(cmd.Env, k+"="+v)
	}`))},
		{name: "a list the callers pass, spread in, is accepted", files: one(fixture("x", "", `func run(t *testing.T, env []string) {
	cmd := exec.Command("x")
	cmd.Env = append(os.Environ(), env...)
}
`))},
		{name: "an Env field of a literal that is not a child's environment is not judged", files: one(fixture("x", "", `type spec struct{ Env []string }

var _ = spec{Env: []string{os.Getenv("E")}}
`))},
		{name: "conformancetest's own FakeCredentials, called in its package, is accepted", dir: conformancetestDir, files: map[string][]byte{"scw.go": []byte("package conformancetest\n\nimport \"testing\"\n\ntype Client struct{ Env []string }\n\nfunc SCW(t *testing.T, c Client) {\n\tfor key, value := range FakeCredentials(t) {\n\t\tc.Env = append(c.Env, key+\"=\"+value)\n\t}\n}\n")}},
		{name: "a whole literal GIT_* entry naming a path is refused", files: one(inTest(`
	cmd := exec.Command("git")
	cmd.Env = append(os.Environ(), "GIT_DIR=/home/someone/project/.git")`)), reasons: []string{namesValue}},
		{name: "a GIT_* entry joined with a value not from t.TempDir() is refused", files: one(inTest(`
	cmd := exec.Command("git")
	cmd.Env = append(os.Environ(), "GIT_WORK_TREE="+os.Getenv("HOME"))`)), reasons: []string{entryNotDerived}},
		{name: "the key GIT_CONFIG_GLOBAL takes os.DevNull and nothing else", files: one(inTest(`
	cmd := exec.Command("git")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.Getenv("CFG"))`)), reasons: []string{entryNotDerived}},
		{name: "a GIT_* entry in a longer concatenation is refused", files: one(inTest(`
	cmd := exec.Command("git")
	decoy := t.TempDir()
	cmd.Env = append(os.Environ(), "GIT_DIR="+decoy+os.Getenv("REL"))`)), reasons: []string{unreadableShape}},
		{name: "a GIT_* literal written outside any Env is still judged", files: one(fixture("x", "", `var hook = []string{"GIT_INDEX_FILE=/home/someone/project/.git/index"}`+"\n")), reasons: []string{namesValue}},
		{name: "an entry whose key is not written is refused", files: one(inTest(`
	cmd := exec.Command("git")
	entry := os.Getenv("E")
	cmd.Env = append(os.Environ(), entry)`)), reasons: []string{unreadableKey}},
		{name: "a GIT_* key named by a constant and joined with \"=\" must be written as a literal", files: one(fixture("x", "", `const gitDir = "GIT_DIR"

func TestX(t *testing.T) {
	cmd := exec.Command("x")
	cmd.Env = append(os.Environ(), gitDir+"="+t.TempDir())
}
`)), reasons: []string{unreadableKey, literalOutside}},
		{name: "a GIT_* key named by a constant with its value in one literal must be written as a literal", files: one(fixture("x", "", `const gitDir = "GIT_DIR"

func TestX(t *testing.T) {
	cmd := exec.Command("x")
	cmd.Env = append(os.Environ(), gitDir+"=/home/someone/project/.git")
}
`)), reasons: []string{unreadableKey, literalOutside}},
		{name: "an entry built from a range over keys that hold GIT_* is refused", files: one(inTest(`
	cmd := exec.Command("x")
	for k, v := range map[string]string{"GIT_DIR": ` + real + `} {
		cmd.Env = append(cmd.Env, k+"="+v)
	}`)), reasons: []string{unreadableKey, literalOutside}},
		{name: "a local list spread in is refused", files: one(inTest(`
	cmd := exec.Command("x")
	extra := strings.Fields(os.Getenv("E"))
	cmd.Env = append(os.Environ(), extra...)`)), reasons: []string{spread}},
		{name: "a slice literal spread in is read entry by entry", files: one(inTest(`
	cmd := exec.Command("x")
	cmd.Env = append(os.Environ(), []string{os.Getenv("E")}...)`)), reasons: []string{unreadableKey}},
		{name: "an exec.Cmd literal's Env field is judged", files: one(inTest(`
	_ = &exec.Cmd{Path: "git", Env: []string{os.Getenv("E")}}`)), reasons: []string{unreadableKey}},
		{name: "an os.ProcAttr literal's Env field is judged", files: one(inTest(`
	_ = &os.ProcAttr{Env: []string{os.Getenv("E")}}`)), reasons: []string{unreadableKey}},
		{name: "a package whose name ends in test is judged in its non-test files", dir: conformancetestDir, files: map[string][]byte{"exo.go": []byte("package conformancetest\n\nimport \"testing\"\n\ntype Client struct{ Env []string }\n\nfunc EXOFixture(t *testing.T, c Client, vars map[string]string) {\n\tfor key, value := range vars {\n\t\tc.Env = append(c.Env, key+\"=\"+value)\n\t}\n}\n")}, reasons: []string{unreadableKey}},
		{name: "measured: a map handed to a helper that builds the environment", files: one(fixture("x", "", `func envOf(m map[string]string) []string { return nil }

func TestX(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git")
	cmd.Env = envOf(map[string]string{"GIT_DIR": root + "/../../.git"})
}
`)), reasons: []string{literalOutside, source}},
		{name: "measured: fmt.Sprintf building the entry, through a variable", files: one(fixture("x", "\t\"fmt\"\n", `func TestX(t *testing.T) {
	x := t.TempDir()
	env := append(os.Environ(), fmt.Sprintf("%s=%s", "GIT_DIR", x))
	cmd := exec.Command("git")
	cmd.Env = env
}
`)), reasons: []string{literalOutside, unreadableKey}},
		{name: "measured: a helper returning a concatenated name", files: one(fixture("x", "", `func env(dir string) []string { return append(os.Environ(), "GIT_"+"DIR="+dir) }`+"\n")), reasons: []string{literalOutside}},
		{name: "measured: a caller writing a concatenated name into a spread parameter", files: one(fixture("x", "", `func run(env []string) {
	cmd := exec.Command("git")
	cmd.Env = append(os.Environ(), env...)
}

func TestX(t *testing.T) { run([]string{"GIT_" + "DIR=/home/someone/project/.git"}) }
`)), reasons: []string{literalOutside}},
		{name: "measured: an os.ProcAttr literal with a GIT_* entry", files: one(inTest(`
	_ = &os.ProcAttr{Env: []string{"GIT_DIR=/home/someone/project/.git"}}`)), reasons: []string{namesValue}},
		{name: "measured: the key, \"=\" and the value as three literals", files: one(inTest(`
	cmd := exec.Command("git")
	cmd.Env = append(os.Environ(), "GIT_DIR"+"="+"/home/someone/project/.git")`)), reasons: []string{literalOutside, unreadableKey}},
		{name: "a GIT_* entry given first to strings.TrimPrefix is not a read (cycle 2, falsifier N2)", files: one(inTest(`
	env := []string{strings.TrimPrefix("GIT_DIR=/home/someone/project/.git", "")}
	cmd := exec.Command("git")
	cmd.Env = env`)), reasons: []string{namesValue, unreadableKey}},
		{name: "measured: an environment read from a file is refused", files: one(inTest(`
	data, _ := os.ReadFile("testdata/child.env")
	cmd := exec.Command("git")
	cmd.Env = strings.Split(string(data), "\n")`)), reasons: []string{source}},
		{name: "measured: an environment built in a variable with a key read at run time is refused", files: one(inTest(`
	env := append(os.Environ(), os.Getenv("E"))
	cmd := exec.Command("git")
	cmd.Env = env`)), reasons: []string{unreadableKey}},
		{name: "an environment built in a variable by appends of written keys is accepted", files: one(inTest(`
	env := append(os.Environ(), "HOME=/root")
	env = append(env, "LANG=C")
	cmd := exec.Command("git")
	cmd.Env = env`))},
		{name: "a variable declared empty and then appended to is accepted", files: one(inTest(`
	var env []string
	env = append(env, "HOME=/root")
	cmd := exec.Command("git")
	cmd.Env = env`))},
		{name: "a variable assigned from a call is refused", files: one(inTest(`
	env := strings.Fields(os.Getenv("E"))
	cmd := exec.Command("git")
	cmd.Env = env`)), reasons: []string{source}},
		{name: "a variable assigned from another variable is refused", files: one(inTest(`
	other := append(os.Environ(), "HOME=/root")
	env := other
	cmd := exec.Command("git")
	cmd.Env = env`)), reasons: []string{source}},
		{name: "a variable assigned from a call that returns several values is refused", files: one(fixture("x", "", `func load() ([]string, error) { return nil, nil }

func TestX(t *testing.T) {
	env, _ := load()
	cmd := exec.Command("git")
	cmd.Env = env
}
`)), reasons: []string{severalValues}},
		{name: "a variable whose entry is overwritten by index is refused", files: one(inTest(`
	env := []string{"HOME=/root"}
	env[0] = os.Getenv("E")
	cmd := exec.Command("git")
	cmd.Env = env`)), reasons: []string{entriesMayChange}},
		{name: "a variable whose address is taken is refused", files: one(inTest(`
	env := []string{"HOME=/root"}
	p := &env
	_ = p
	cmd := exec.Command("git")
	cmd.Env = env`)), reasons: []string{addressTaken, entriesMayChange}},
		{name: "a variable bound by a range clause is refused", files: one(inTest(`
	cmd := exec.Command("git")
	for _, env := range [][]string{{"HOME=/root"}} {
		cmd.Env = env
	}`)), reasons: []string{rangeBound}},
		{name: "a parameter handed over whole is refused", files: one(fixture("x", "", `func run(env []string) {
	cmd := exec.Command("git")
	cmd.Env = env
}
`)), reasons: []string{source}},
		{name: "a field handed over is refused", files: one(fixture("x", "", `type repo struct{ env []string }

func (r repo) run() {
	cmd := exec.Command("git")
	cmd.Env = r.env
}
`)), reasons: []string{source}},
		{name: "measured: a file's lines extended by one entry are refused (cycle 3)", files: one(inTest(`
	data, _ := os.ReadFile("testdata/child.env")
	cmd := exec.Command("git")
	cmd.Env = append(strings.Split(string(data), "\n"), "HOME=/root")`)), reasons: []string{source}},
		{name: "measured: a variable extending a list read at run time is refused (cycle 3)", files: one(inTest(`
	lines := strings.Fields(os.Getenv("E"))
	env := append(lines, "HOME=/root")
	cmd := exec.Command("git")
	cmd.Env = env`)), reasons: []string{source}},
		{name: "the process's environment, nil, an environment extending itself and nested appends are accepted (cycle 3)", files: one(inTest(`
	cmd := exec.Command("git")
	cmd.Env = os.Environ()
	cmd.Env = append(nil, "HOME=/root")
	cmd.Env = append(cmd.Env, "LANG=C")
	cmd.Env = append(append(os.Environ(), "A=1"), "B=2")`))},
		{name: "a nested append over a list read at run time is refused (cycle 3)", files: one(inTest(`
	cmd := exec.Command("git")
	cmd.Env = append(append(strings.Fields(os.Getenv("E")), "A=1"), "B=2")`)), reasons: []string{source}},
		{name: "measured: an entry of X.Env overwritten by index is refused (cycle 3)", files: one(inTest(`
	lines := strings.Fields(os.Getenv("E"))
	cmd := exec.Command("git")
	cmd.Env = []string{"HOME=/root"}
	cmd.Env[0] = lines[0]`)), reasons: []string{changesEntries}},
		{name: "measured: X.Env given to copy is refused (cycle 3)", files: one(inTest(`
	lines := strings.Fields(os.Getenv("E"))
	cmd := exec.Command("git")
	cmd.Env = []string{"HOME=/root"}
	copy(cmd.Env, lines)`)), reasons: []string{changesEntries}},
		{name: "a non-test file of an ordinary package is not asked about environments", files: map[string][]byte{"lib.go": []byte("package x\n\nimport \"os/exec\"\n\nfunc run(entry string) { c := exec.Command(\"x\"); c.Env = []string{entry, \"GIT_DIR=/home\"} }\n")}},
	})
}

// TestFakeCredentialsIsVouchedForOnlyWhileItFilters: FakeCredentials is
// vouched for only when every map write sits under the SCW_ filter on the
// key it writes.
func TestFakeCredentialsIsVouchedForOnlyWhileItFilters(t *testing.T) {
	src := func(body string) map[string][]byte {
		return map[string][]byte{"aws.go": []byte("//go:build conformance\n\npackage conformancetest\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\nfunc FakeCredentials(t *testing.T) map[string]string {\n\tfake := map[string]string{}\n\tfor _, line := range []string{} {\n" + body + "\t}\n\treturn fake\n}\n")}
	}
	const filtered = "\t\tif key, value, ok := strings.Cut(line, \"=\"); ok && strings.HasPrefix(key, \"SCW_\") {\n\t\t\tfake[key] = value\n\t\t}\n"
	if why := fakeCredentialsKeepsScalewayKeys(src(filtered)); why != "" {
		t.Errorf("FakeCredentials filtering on SCW_ was refused: %s", why)
	}
	for name, body := range map[string]string{
		"filtering on nothing":               strings.Replace(filtered, `"SCW_"`, `""`, 1),
		"a HasPrefix that guards nothing":    "\t\tkey, value, _ := strings.Cut(line, \"=\")\n\t\t_ = strings.HasPrefix(key, \"SCW_\")\n\t\tfake[key] = value\n",
		"filtering one key, writing another": "\t\tif key, value, ok := strings.Cut(line, \"=\"); ok && strings.HasPrefix(key, \"SCW_\") {\n\t\t\tfake[value] = key\n\t\t}\n",
	} {
		if why := fakeCredentialsKeepsScalewayKeys(src(body)); !strings.Contains(why, "no longer keeps SCW_* keys alone") {
			t.Errorf("FakeCredentials %s was accepted, or refused for another reason: %q", name, why)
		}
	}
	if why := fakeCredentialsKeepsScalewayKeys(map[string][]byte{}); !strings.Contains(why, "has no FakeCredentials") {
		t.Errorf("a missing FakeCredentials was accepted, or refused for another reason: %q", why)
	}
}

// TestAnEnvironmentPinSilencesOneQuestionInOneFunction: the pinned function
// may add entries whose keys are read at run time, but a literal GIT_* entry
// written there is still refused, and the same entries elsewhere are refused.
func TestAnEnvironmentPinSilencesOneQuestionInOneFunction(t *testing.T) {
	src := func(fn, extra string) map[string][]byte {
		return map[string][]byte{"object_storage_conformance_test.go": []byte("package cli\n\nimport (\n\t\"os\"\n\t\"os/exec\"\n\t\"testing\"\n)\n\nfunc " + fn + "(t *testing.T) {\n\tcmd := exec.Command(\"x\")\n\tfor key, value := range readVars() {\n\t\tcmd.Env = append(cmd.Env, key+\"=\"+value)\n\t}\n" + extra + "\t_ = os.Exit\n}\n\nfunc readVars() map[string]string { return nil }\n")}
	}
	runGuardCases(t, []guardCase{
		{name: "the pinned function is accepted", dir: "internal/cli", files: src("TestMountedS3OfficialAWSCLIBytes", "")},
		{name: "a literal GIT_* entry in the pinned function is still refused", dir: "internal/cli", files: src("TestMountedS3OfficialAWSCLIBytes", "\tcmd.Env = append(cmd.Env, \"GIT_DIR=/home/someone/project/.git\")\n"), reasons: []string{namesValue}},
		{name: "the same entries in another function are refused", dir: "internal/cli", files: src("TestSomethingElse", ""), reasons: []string{unreadableKey}},
	})
}

// TestASourcePinVouchesForOneFunction: a function pinned in envSourcePins may
// hand a child an environment built elsewhere, the same in another function
// is refused, and a literal GIT_* entry written in the pinned function is
// still refused.
func TestASourcePinVouchesForOneFunction(t *testing.T) {
	src := func(fn, extra string) map[string][]byte {
		return map[string][]byte{"plan_test.go": []byte("package main\n\nimport (\n\t\"os\"\n\t\"os/exec\"\n\t\"testing\"\n)\n\nfunc " + fn + "(t *testing.T) []string {\n\tcmd := exec.Command(\"git\")\n\tcmd.Env = withoutInheritedGit(os.Environ())\n" + extra + "\treturn nil\n}\n\nfunc withoutInheritedGit(env []string) []string { return env }\n")}
	}
	runGuardCases(t, []guardCase{
		{name: "the vouched function is accepted", dir: "tools/testplan", files: src("tracked", "")},
		{name: "a literal GIT_* entry in the vouched function is still refused", dir: "tools/testplan", files: src("tracked", "\tcmd.Env = append(cmd.Env, \"GIT_DIR=/home/someone/project/.git\")\n"), reasons: []string{namesValue}},
		{name: "the same in another function is refused", dir: "tools/testplan", files: src("listed", ""), reasons: []string{source}},
		{name: "measured: a source added later to the vouched function is refused (cycle 3)", dir: "tools/testplan", files: src("tracked", "\tcmd2 := exec.Command(\"git\")\n\tcmd2.Env = withoutInheritedGit(cmd.Env)\n"), reasons: []string{source}},
	})
}

// TestARefusalNamesItsLine:// TestARefusalNamesItsLine: two refusals with the same reason on two lines
// are both reported, each with its own line.
func TestARefusalNamesItsLine(t *testing.T) {
	got, err := gitSetenvRefusals("tools/x", one(inTest("\tt.Setenv(\"GIT_DIR\", \"/a\")\n\tt.Setenv(\"GIT_DIR\", \"/b\")")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "tools/x/a_test.go:18: ") || !strings.HasPrefix(got[1], "tools/x/a_test.go:19: ") {
		t.Errorf("refusals = %q, want two, at tools/x/a_test.go:18 and :19", got)
	}
}
