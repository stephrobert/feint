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

// A file compiled into tests alone cannot change a served answer, so it earns no
// conformance leg — the same argument that makes prose prose wherever it lives.
// It is still triaged: a `_test.go` in a package no rule names must redden.
func TestAFileCompiledOnlyIntoTestsEarnsNoConformanceRun(t *testing.T) {
	for _, path := range []string{
		"internal/core/machine/incus_test.go",
		"internal/providers/scaleway/testdata/servers.json",
	} {
		got := build(t.TempDir(), []string{path})
		for _, r := range got.Runs {
			if strings.Contains(r.Command, "conformance") {
				t.Errorf("%s earned %q; nothing compiled into tests alone reaches a client", path, r.Command)
			}
		}
		if len(got.Untriaged) > 0 {
			t.Errorf("%s was reported un-triaged; the exemption must not swallow the triage", path)
		}
	}
	// The control: the production file beside it must still earn its leg,
	// otherwise this test would pass over an exemption that swallowed the
	// whole directory.
	got := build(t.TempDir(), []string{"internal/core/machine/incus.go"})
	if len(got.Runs) == 0 {
		t.Fatal("a production file in the machine layer earned nothing")
	}
	// And a test file in a package no rule names is still an absence.
	if orphan := build(t.TempDir(), []string{"quantum/teleporter_test.go"}); len(orphan.Untriaged) != 1 {
		t.Fatalf("an un-triaged test file was excused rather than reported: %+v", orphan.Untriaged)
	}
}

// The falsifications a diff has earned, read off the specs. This is the
// discipline every batch here has followed by hand and stated in its own pull
// request, which means it has also been forgotten by hand.
func TestAChangedFileEarnsTheSpecsThatMutateIt(t *testing.T) {
	dir := t.TempDir()
	specs := filepath.Join(dir, "tools", "falsify", "specs")
	if err := os.MkdirAll(specs, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(specs, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("names-it.json", `{"mutations":[{"file":"internal/core/store/store.go","find":"a","replace":"b","test":"T"}]}`)
	write("names-another.json", `{"mutations":[{"file":"internal/probe/probe.go","find":"a","replace":"b","test":"T"}]}`)

	got := build(dir, []string{"internal/core/store/store.go"})
	want := "mise run falsify -- tools/falsify/specs/names-it.json"
	if !hasRun(got, want) {
		t.Fatalf("a changed file did not earn the spec that mutates it; the plan was:\n%s", got)
	}
	// The control, and it is what stops this passing over a build() that
	// prints every spec it can find: the spec naming another file must not
	// appear.
	if hasRun(got, "mise run falsify -- tools/falsify/specs/names-another.json") {
		t.Fatal("a spec naming an untouched file was earned; the mapping is not read, it is emitted")
	}
}

// The real corpus, not a fixture: the mapping is only worth having if it is
// exact against what this repository actually declares.
func TestTheRealSpecsAreReadAndOnlyTheMatchingOnesAreEarned(t *testing.T) {
	dir := root(t)
	got := falsifications(dir, []string{"internal/core/machine/incus_ovn.go"})
	if len(got) == 0 {
		t.Fatal("no spec earned for a file 20 mutations name; the specs are not being read")
	}
	for _, cmd := range got {
		if !strings.HasPrefix(cmd, "mise run falsify -- tools/falsify/specs/") {
			t.Errorf("earned command is not runnable as printed: %q", cmd)
		}
	}
	if none := falsifications(dir, []string{"quantum/teleporter.go"}); len(none) != 0 {
		t.Errorf("a file no spec names earned %v", none)
	}
}

// MECHANISM ONE OF #588, and the one that had to ship whatever else did.
//
// This tool was wrong five times in one week. Four erred expensively, which is
// survivable; the fifth (#521) named four runs and 27 specs, all of which went
// green, while the leg that reproduces the defect was in none of them. Read as
// a ceiling, that plan said the work was done — and the property that would
// have stopped it was written once, in prose, in CONTRIBUTING.md, where the
// reader about to act had not looked.
//
// So it is printed. In every shape a plan takes, including the two that read
// most like an all-clear: "nothing beyond prepush", and a plan whose runs are
// all cheap. The un-triaged shape carries it too, because a reader who is being
// told to add a rule is exactly a reader deciding what to run.
func TestEveryPlanSaysWhatItCannotKnow(t *testing.T) {
	// Three populations, chosen because they are the three branches of
	// String(): a plan with runs, a plan with none, and a plan that refuses.
	for _, tc := range []struct {
		what  string
		paths []string
	}{
		{"a plan with runs", []string{"internal/core/machine/incus.go"}},
		{"a plan with nothing to run", []string{"LICENSE"}},
		{"a plan that refuses an un-triaged path", []string{"quantum/teleporter.go"}},
	} {
		got := build(t.TempDir(), tc.paths).String()
		for _, want := range []string{"which population the defect lives in", "#521", "floor"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s does not say %q, so it can be read as a ceiling:\n%s", tc.what, want, got)
			}
		}
	}
	// The control, and it is what stops this passing over a String() that
	// prints the notice and nothing else: the plan must still say what it does
	// prescribe.
	if runs := build(t.TempDir(), []string{"internal/core/machine/incus.go"}).String(); !strings.Contains(runs, "conformance:leg -- runtime") {
		t.Fatalf("the plan lost its runs while gaining its closing line:\n%s", runs)
	}
}

// MECHANISM TWO OF #588: an `Unproven` sentence is checked against the artefact
// it names.
//
// The sentence that opened this: "a change to the stored shape is proved across
// a restart by `mise run conformance:environment`" (#567). That suite is
// tools/conformance/environment/up.sh, and it contains no `snapshot` and no
// `--state` — it was false the day it was written, and greppable that same day.
func TestEveryUnprovenClaimHoldsAgainstTheArtefactItNames(t *testing.T) {
	problems := checkClaims(root(t), rules)
	if len(problems) > 0 {
		t.Errorf("%d claim(s) no longer hold:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
	// A control against the whole mechanism being asleep: the real table must
	// actually carry claims, or the test above passes over an empty world in
	// the most literal way.
	cited := 0
	for _, r := range rules {
		cited += len(r.Cites)
	}
	if cited == 0 {
		t.Fatal("no rule cites anything; this test would pass over a table that reads nothing")
	}
}

// The witness this repository's own skill asks for: a control whose success is
// "nothing was found" must first be shown to be able to find. Each planted
// defect below is one of the ways a claim goes false, and the last of them is
// the one #567 was.
func TestTheClaimCheckerFindsAClaimThatHasGoneFalse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "artefact.sh"), []byte("#!/bin/sh\necho    hello   world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		what string
		rule rule
		want string
	}{
		{
			what: "a sentence that names an artefact and cites nothing",
			rule: rule{Path: "x/", Unproven: "this is proved by `mise run conformance:environment`"},
			want: "cites nothing",
		},
		{
			what: "a claim whose sentence has been edited out from under it",
			rule: rule{Path: "x/", Unproven: "a rewritten sentence",
				Cites: []claim{{About: "what it used to say", In: "artefact.sh", Shows: []string{"hello"}}}},
			want: "not a fragment of its Unproven",
		},
		{
			what: "a claim that asks nothing of the artefact it names",
			rule: rule{Path: "x/", Unproven: "held by artefact.sh",
				Cites: []claim{{About: "held by artefact.sh", In: "artefact.sh"}}},
			want: "asks nothing of it",
		},
		{
			what: "a claim resting on a token the artefact does not carry",
			rule: rule{Path: "x/", Unproven: "held by artefact.sh",
				Cites: []claim{{About: "held by artefact.sh", In: "artefact.sh", Shows: []string{"goodbye"}}}},
			want: "no longer there",
		},
		{
			what: "a claim naming an artefact that is not there at all",
			rule: rule{Path: "x/", Unproven: "held by gone.sh",
				Cites: []claim{{About: "held by gone.sh", In: "gone.sh", Shows: []string{"anything"}}}},
			want: "cannot be read",
		},
		{
			what: "a claim of absence the artefact has since contradicted — the #567 shape",
			rule: rule{Path: "x/", Unproven: "artefact.sh says nothing about hello",
				Cites: []claim{{About: "artefact.sh says nothing about hello", In: "artefact.sh", Absent: []string{"hello"}}}},
			want: "needs rewriting",
		},
	} {
		problems := checkClaims(dir, []rule{tc.rule})
		if len(problems) == 0 {
			t.Errorf("%s was not reported; the checker cannot find what it searches for", tc.what)
			continue
		}
		if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
			t.Errorf("%s was reported as %q, which does not name the defect", tc.what, problems)
		}
	}
	// The accepting half, without which a checker that refuses everything would
	// pass every case above and make the table unwritable. Whitespace is
	// collapsed on both sides, which is what lets a claim quote a gofmt-aligned
	// struct field the way a reader would say it.
	held := rule{Path: "x/", Unproven: "held by artefact.sh, which says hello world and nothing about goodbye",
		Cites: []claim{{
			About:  "held by artefact.sh, which says hello world and nothing about goodbye",
			In:     "artefact.sh",
			Shows:  []string{"hello world"},
			Absent: []string{"goodbye"},
		}}}
	if problems := checkClaims(dir, []rule{held}); len(problems) > 0 {
		t.Errorf("a claim that holds was reported anyway: %q", problems)
	}
}

// MECHANISM THREE OF #588: a rule may not prescribe a run that cannot drive
// what it governs.
//
// `functional.sh` sent to `conformance:leg -- fields` (#566/#477) is the shape:
// a real leg, which runs, and which has no machine runtime — the one population
// that gate refuses to work in. TestEveryLegTheRulesNameIsALegTheScriptAccepts
// already ties leg names to leg.sh; this ties a suite's need for a runtime to
// its leg's, and it reads leg.sh for both halves rather than a list kept here.
//
// What it can see, and it is deliberately the half a machine can read without
// guessing:
//
//   - a script that resolves its runtime through tools/runtime-mode.sh, which
//     refuses `--vm off` by construction (asserted below, so the marker cannot
//     rot silently);
//   - a suite leg.sh runs only on legs leg.sh itself refuses when FEINT_VM is
//     off.
//
// What it cannot see is written down in rules.go beside the four suites: the
// general property is that a prescribed run must invoke the file it was
// prescribed for, and four scripts under tools/conformance still fail it.
func TestARuleMayNotPrescribeARunThatCannotDriveWhatItGoverns(t *testing.T) {
	dir := root(t)
	legSh, err := os.ReadFile(filepath.Join(dir, "tools", "conformance", "leg.sh"))
	if err != nil {
		t.Fatal(err)
	}
	refused := legsRefusedWithoutARuntime(string(legSh))
	if len(refused) == 0 {
		t.Fatal("no leg refuses to run without a machine runtime, which cannot be right: " +
			"leg.sh refuses `runtime` rather than letting its four suites skip themselves")
	}
	resolver, err := os.ReadFile(filepath.Join(dir, "tools", "runtime-mode.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resolver), `if [ "$mode" = "off" ]`) {
		t.Fatal("tools/runtime-mode.sh no longer refuses --vm off, so naming it no longer means " +
			"a script needs a runtime; this test's whole population came from that refusal")
	}

	// Half one: a run naming a leg that leg.sh refuses without a runtime must
	// supply one, or the reader pastes it and is refused at second zero.
	for _, r := range rules {
		for _, cmd := range r.Runs {
			leg, ok := legIn(cmd)
			if !ok || !refused[leg] {
				continue
			}
			if !declaresARuntime(cmd) {
				t.Errorf("rule %q prescribes %q, and tools/conformance/leg.sh refuses that leg "+
					"with no FEINT_VM: the reader is sent to a run that exits 2 before it measures anything",
					r.Path, cmd)
			}
		}
	}

	// Half two: a suite that cannot be driven without a machine runtime must
	// earn a run that has one. This is the #566/#477 shape, and it is checked
	// against the plan rather than against the table, so a catch-all routing it
	// cheaply reddens exactly as a wrong rule does.
	arms := legArms(string(legSh))
	if len(arms) == 0 {
		t.Fatal("no leg arm parsed out of leg.sh; this test would pass over an empty world")
	}
	population := 0
	for _, path := range tracked(t) {
		if !strings.HasPrefix(path, "tools/conformance/") || !strings.HasSuffix(path, ".sh") {
			continue
		}
		why := needsAMachineRuntime(dir, path, arms, refused)
		if why == "" {
			continue
		}
		population++
		got := build(dir, []string{path})
		if !anyRunDeclaresARuntime(got) {
			t.Errorf("%s %s, and its plan prescribes no run that declares one:\n%s", path, why, got)
		}
	}
	if population == 0 {
		t.Fatal("no suite in tools/conformance was found to need a machine runtime; the four " +
			"dataplane suites do, so this test just measured its own breakage rather than the table")
	}
	// The control: a suite that runs perfectly well with no runtime must not be
	// dragged into the 590 s leg by this, or the cheapest way to satisfy the
	// test above would be to make every rule expensive.
	if why := needsAMachineRuntime(dir, "tools/conformance/scaleway/scw-cli.sh", arms, refused); why != "" {
		t.Errorf("the Scaleway CLI suite was read as needing a machine runtime (%s); the whole "+
			"conformance matrix runs it with none", why)
	}
}

// legsRefusedWithoutARuntime reads leg.sh's own refusal rather than a list kept
// here: `if [ "$leg" = "runtime" ] && [ "$vm" = "off" ]`.
var legRefusal = regexp.MustCompile(`\[ "\$leg" = "([a-z0-9-]+)" \] && \[ "\$vm" = "off" \]`)

func legsRefusedWithoutARuntime(body string) map[string]bool {
	refused := map[string]bool{}
	for _, m := range legRefusal.FindAllStringSubmatch(body, -1) {
		refused[m[1]] = true
	}
	return refused
}

// legArms maps each leg to the suites its case arm runs. Lines outside an arm
// are deliberately ignored: guard.sh and score.sh run on every leg, and
// attributing them to one would make this test claim they need a runtime.
var suitePath = regexp.MustCompile(`tools/[A-Za-z0-9_./-]+\.sh`)

func legArms(body string) map[string][]string {
	arms := map[string][]string{}
	var current []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == ";;":
			current = nil
			continue
		case strings.HasSuffix(trimmed, ")") && !strings.ContainsAny(trimmed, " \t*"):
			current = strings.Split(strings.TrimSuffix(trimmed, ")"), "|")
			continue
		case strings.HasPrefix(trimmed, "#") || len(current) == 0:
			continue
		}
		for _, suite := range suitePath.FindAllString(trimmed, -1) {
			for _, leg := range current {
				arms[leg] = append(arms[leg], suite)
			}
		}
	}
	return arms
}

// needsAMachineRuntime answers from the artefacts, and says why when it does.
func needsAMachineRuntime(root, path string, arms map[string][]string, refused map[string]bool) string {
	body, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return ""
	}
	if strings.Contains(string(body), "tools/runtime-mode.sh") {
		return "resolves its runtime through tools/runtime-mode.sh, which refuses --vm off"
	}
	carried, all := false, true
	for leg, suites := range arms {
		for _, suite := range suites {
			if suite != path {
				continue
			}
			carried = true
			if !refused[leg] {
				all = false
			}
		}
	}
	if carried && all {
		return "is run by no leg but one tools/conformance/leg.sh refuses without a runtime"
	}
	return ""
}

// declaresARuntime reports whether a command hands a machine runtime to what it
// starts. `FEINT_VM=off` is not one: it is the value every leg of the CI matrix
// carries, and the value the suites above refuse.
func declaresARuntime(cmd string) bool {
	return strings.Contains(cmd, "FEINT_VM=") && !strings.Contains(cmd, "FEINT_VM=off")
}

func anyRunDeclaresARuntime(p plan) bool {
	for _, r := range p.Runs {
		if declaresARuntime(r.Command) {
			return true
		}
	}
	return false
}
