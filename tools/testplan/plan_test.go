package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// What keeps a routing table from becoming a lie.
//
// A table that says which runs a change earns is the most inviting place in this
// repository to write a comment instead of a control. It is read once, believed
// afterwards, and it goes wrong in two directions that look nothing alike:
//
//   - a new directory nobody triaged, whose changes then earn *nothing* because
//     no line matched — the failure mode of every fall-through default;
//   - a line naming a directory that was deleted or renamed, which keeps reading
//     as coverage while covering nothing — the same rot as a limit that no
//     longer holds.
//
// Both are cheap to detect and neither is detectable by reading. So the table is
// held against `git ls-files` in both directions, its commands are held against
// the tasks that actually exist, and its one measured claim — that a file
// reaching the machine layer earns the runtime leg — is exercised with a file
// that reaches it and a control file that does not.

func root(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func tracked(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = root(t)
	if out, err := cmd.Output(); err == nil {
		return lines(t, string(out))
	}
	// No git here, and skipping would be the wrong answer twice over.
	//
	// The falsification harness copies this repository **without** `.git`
	// (tools/falsify/falsify.py, EXCLUDE), so the first shape of this helper
	// skipped inside the copy — and a skipped test is not a red one. The two
	// strongest guards in this file, the ones that walk the whole tree in both
	// directions, reported "TEST STILL PASSED" for every mutation aimed at
	// them. They were unfalsifiable, which is the precise thing this repository
	// says a guard must never be.
	//
	// So the fallback walks the tree and honours .gitignore's simple patterns,
	// which is all this repository's has: 272 lines, no negation, no `**`.
	return walked(t)
}

func lines(t *testing.T, out string) []string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		t.Fatal("the file inventory is empty; this test would pass over an empty world")
	}
	return paths
}

// ignores reads the simple patterns of .gitignore: a trailing-slash directory,
// a `*.suffix`, a rooted `/path`, or a bare name. Anything cleverer is refused
// out loud rather than silently mis-applied, because a pattern this misreads
// turns into a file that looks un-triaged and a red nobody can explain.
func ignores(t *testing.T, dir string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	var pats []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "!") || strings.Contains(line, "**") {
			t.Fatalf(".gitignore carries a pattern this fallback cannot read: %q", line)
		}
		pats = append(pats, line)
	}
	return pats
}

// ignored applies git's own anchoring rule, which the first version of this
// dropped and paid for immediately: a pattern that contains a slash anywhere is
// anchored to the repository root, and only a bare name matches at any depth.
// `/feint` names the built binary at the root; read as a bare name it also ate
// `cmd/feint/`, and the walk then reported the entry point as a directory no
// rule covers. The failure was loud, which is the only reason it cost minutes.
func ignored(pats []string, rel string) bool {
	base := filepath.Base(rel)
	for _, pat := range pats {
		anchored := strings.Contains(strings.TrimSuffix(pat, "/"), "/")
		clean := strings.Trim(pat, "/")
		switch {
		case anchored:
			if rel == clean || strings.HasPrefix(rel, clean+"/") {
				return true
			}
		case strings.HasPrefix(pat, "*."):
			if strings.HasSuffix(base, pat[1:]) {
				return true
			}
		case strings.HasSuffix(pat, "/"):
			if base == clean {
				return true
			}
		default:
			if base == clean {
				return true
			}
		}
	}
	return false
}

func walked(t *testing.T) []string {
	t.Helper()
	dir := root(t)
	pats := append(ignores(t, dir), ".git/", ".claude/", ".upstream/", "notes/")
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil || rel == "." {
			return relErr
		}
		if ignored(pats, rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	if len(paths) == 0 {
		t.Fatal("the file inventory is empty; this test would pass over an empty world")
	}
	return paths
}

// A path this table does not name is an absence, and the tool prints it as one.
// This test is what puts the discovery on the commit that adds the directory
// rather than on the pull request that is surprised by it months later.
func TestEveryTrackedPathIsTriagedBySomeRule(t *testing.T) {
	var orphans []string
	for _, path := range tracked(t) {
		found := false
		for _, r := range rules {
			if matches(r.Path, path) {
				found = true
				break
			}
		}
		if !found {
			orphans = append(orphans, path)
		}
	}
	if len(orphans) > 0 {
		t.Errorf("%d tracked path(s) no rule in rules.go triages:\n  %s\n\n"+
			"Add a rule saying what a change there costs to prove. Leaving it unmatched\n"+
			"makes `testplan` answer \"nothing to run\" for it, which is the cheap default\n"+
			"this tool exists to refuse.", len(orphans), strings.Join(orphans, "\n  "))
	}
}

// The other direction, and the one nobody thinks to write. A rule naming a
// directory that no longer exists reads as coverage on every later day.
func TestNoRuleMatchesNothing(t *testing.T) {
	paths := tracked(t)
	for _, r := range rules {
		hit := false
		for _, path := range paths {
			if matches(r.Path, path) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("rule %q (%s) matches no tracked path: it is either a directory that "+
				"moved, or coverage of nothing", r.Path, r.Why)
		}
	}
}

var taskName = regexp.MustCompile(`(?m)^\[tasks\."?([a-z0-9:_-]+)"?\]`)

// A plan that prints a command nobody can run is worse than no plan: the reader
// pastes it, sees `mise: no such task`, and stops trusting the whole output.
func TestEveryTaskTheRulesNameExistsInMiseToml(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(root(t), "mise.toml"))
	if err != nil {
		t.Fatal(err)
	}
	exists := map[string]bool{}
	for _, m := range taskName.FindAllStringSubmatch(string(body), -1) {
		exists[m[1]] = true
	}
	if len(exists) == 0 {
		t.Fatal("no task parsed out of mise.toml; this test would pass over an empty world")
	}

	seen := map[string]bool{}
	for _, r := range rules {
		for _, cmd := range append([]string{}, r.Runs...) {
			seen[taskIn(cmd)] = true
		}
	}
	seen[taskIn(runtimeLeg)] = true
	for task := range seen {
		if task != "" && !exists[task] {
			t.Errorf("rules.go names task %q, which mise.toml does not define", task)
		}
	}
}

// taskIn extracts the mise task out of a command a rule prints.
func taskIn(cmd string) string {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f == "run" && i > 0 && strings.HasSuffix(fields[i-1], "mise") {
			if i+1 < len(fields) {
				return fields[i+1]
			}
			return ""
		}
	}
	// A bare task name, which normalise() prefixes later.
	if len(fields) > 0 && !strings.Contains(fields[0], "=") {
		return fields[0]
	}
	return ""
}

// The legs this table sends people to must be legs `leg.sh` accepts. The two
// lists drifted once already between the workflow matrix and the script, which
// is why tools/conformance carries TestEveryMatrixLegCanBeReproducedLocally for
// leg.sh; a third list naming legs deserves the same treatment rather than the
// same trust.
func TestEveryLegTheRulesNameIsALegTheScriptAccepts(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(root(t), "tools", "conformance", "leg.sh"))
	if err != nil {
		t.Fatal(err)
	}
	accepted := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasSuffix(trimmed, ") ;;") {
			continue
		}
		for _, name := range strings.Split(strings.TrimSuffix(trimmed, ") ;;"), "|") {
			accepted[strings.TrimSpace(name)] = true
		}
	}
	if len(accepted) == 0 {
		t.Fatal("no leg name parsed out of leg.sh; this test would pass over an empty world")
	}

	named := map[string]bool{}
	for _, r := range rules {
		for _, cmd := range r.Runs {
			if leg, ok := legIn(cmd); ok {
				named[leg] = true
			}
		}
	}
	if leg, ok := legIn(runtimeLeg); ok {
		named[leg] = true
	}
	if len(named) == 0 {
		t.Fatal("no rule names a conformance leg, which cannot be right")
	}
	for leg := range named {
		if !accepted[leg] {
			t.Errorf("rules.go sends a reader to leg %q, which tools/conformance/leg.sh refuses", leg)
		}
	}
}

func legIn(cmd string) (string, bool) {
	if !strings.Contains(cmd, "conformance:leg") {
		return "", false
	}
	_, after, found := strings.Cut(cmd, "--")
	if !found {
		return "", false
	}
	return strings.TrimSpace(after), true
}

// The one claim in this tool that is measured rather than tabulated. A pack file
// that speaks to the machine layer starts containers on the operator's own
// station, wherever it lives, and the table in rules.go would send it to a
// client leg alone.
func TestAFileThatImportsTheMachineLayerEarnsTheRuntimeLeg(t *testing.T) {
	dir := t.TempDir()
	reaching := "internal/providers/scaleway/servers.go"
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(reaching)), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "package scaleway\n\nimport (\n\t\"fmt\"\n\n\t\"" + machineLayer + "\"\n)\n\n" +
		"var _ = fmt.Sprint\nvar _ = machine.Binding{}\n"
	if err := os.WriteFile(filepath.Join(dir, reaching), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := build(dir, []string{reaching})
	if !hasRun(got, runtimeLeg) {
		t.Fatalf("a file importing %s did not earn %q; the plan was:\n%s",
			machineLayer, runtimeLeg, got)
	}

	// The control, and it is the half that makes the assertion mean something:
	// the same path, the same rule, an import that is not the machine layer.
	// Without it this test would pass over a build() that always asks for the
	// runtime leg — which would be the most expensive possible way to be wrong.
	plain := "internal/providers/scaleway/catalog.go"
	if err := os.WriteFile(filepath.Join(dir, plain), []byte("package scaleway\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if control := build(dir, []string{plain}); hasRun(control, runtimeLeg) {
		t.Fatalf("a file importing nothing but fmt earned %q; build() asks for the runtime "+
			"leg unconditionally and this test would have passed over that:\n%s", runtimeLeg, control)
	}
}

func hasRun(p plan, cmd string) bool {
	for _, r := range p.Runs {
		if r.Command == cmd {
			return true
		}
	}
	return false
}

// An un-triaged path must be loud and must not be cheap. The tempting behaviour
// is "matched nothing, so run nothing", and it is exactly how an operation that
// appeared upstream and that nobody sorted becomes a silent decision.
func TestAnUntriagedPathIsAFailureNotACheapDefault(t *testing.T) {
	got := build(t.TempDir(), []string{"quantum/teleporter.go"})
	if len(got.Untriaged) != 1 || got.Untriaged[0] != "quantum/teleporter.go" {
		t.Fatalf("an unmatched path was not reported as un-triaged: %+v", got.Untriaged)
	}
	if !strings.Contains(got.String(), "no rule triages") {
		t.Fatalf("the printed plan does not name the un-triaged path:\n%s", got)
	}
	if strings.Contains(got.String(), "Nothing beyond") {
		t.Fatal("an un-triaged path printed the reassuring answer reserved for a triaged one")
	}
}

// Cheapest first, so a reader stops at the first red rather than at the last.
func TestThePlanPutsTheCheapRunBeforeTheExpensiveOne(t *testing.T) {
	got := build(t.TempDir(), []string{"internal/core/machine/incus.go", "internal/contract/check.go"})
	if len(got.Runs) < 2 {
		t.Fatalf("expected at least two runs, got %+v", got.Runs)
	}
	if !strings.Contains(got.Runs[0].Command, "probe") {
		t.Errorf("the 0.7 s leg is not first: %q", got.Runs[0].Command)
	}
	if !strings.Contains(got.Runs[len(got.Runs)-1].Command, "runtime") {
		t.Errorf("the 590 s leg is not last: %q", got.Runs[len(got.Runs)-1].Command)
	}
}

// Every rule that claims its runs are sufficient says so by writing nothing in
// Unproven, and that is a claim. This does not judge the claim — no test can —
// but it refuses the third state, where a rule neither proves nor admits.
func TestEveryRuleWithRunsSaysWhatItDoesNotProve(t *testing.T) {
	for _, r := range rules {
		if len(r.Runs) > 1 && r.Unproven == prepushIsTheWholeGate {
			t.Errorf("rule %q runs %d commands and claims to leave nothing unproven; "+
				"a plan that expensive has a residual, and hiding it is how a green "+
				"comes to describe a smaller world than its reader believes",
				r.Path, len(r.Runs))
		}
	}
}

// Prose is prose wherever it sits. Without this, a README under the conformance
// harness earned the 208 s `fields` leg, and a tool that over-prescribes is
// ignored exactly as fast as one that under-prescribes.
func TestAMarkdownFileIsDocumentationWhereverItLives(t *testing.T) {
	for _, path := range []string{
		"tools/conformance/README.md",
		"internal/core/machine/README.md",
		"CONTRIBUTING.fr.md",
	} {
		got := build(t.TempDir(), []string{path})
		for _, r := range got.Runs {
			if strings.Contains(r.Command, "conformance") || strings.Contains(r.Command, "runtime") {
				t.Errorf("%s earned %q; changing prose cannot alter a served answer", path, r.Command)
			}
		}
	}
	// The control: the file beside that README, which is not prose.
	got := build(t.TempDir(), []string{"tools/conformance/score.sh"})
	if len(got.Runs) == 0 {
		t.Fatal("a harness script earned nothing; the documentation rule has swallowed the directory")
	}
}
