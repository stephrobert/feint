package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The falsifications a diff has earned, read off the specs rather than
// remembered.
//
// This is a discipline every batch on this repository has followed by hand and
// stated in its own pull request — *"the 31 specs naming a touched file replayed
// in full"* — which means it has also been forgotten by hand. A guard whose test
// stopped biting is invisible to `mise run check`: the test still passes, and it
// passes over nothing. `falsify:lint` checks that each declared fragment still
// applies textually and that the test each mutation names exists in the package
// it declares (#923); replaying the mutation is what proves the test bites.
//
// 270 specs name 326 distinct files today, in 2244 mutations, so the mapping is
// far past the size anybody holds in their head, and it is exact: a spec says
// which file it mutates. Nothing here is a judgement — the specs are the source, and a spec
// that stops naming a file stops earning its replay on the same commit.
//
// That sentence is recounted, not remembered, and it is the one place where
// remembering it was embarrassing: the figure is the argument for generating
// the mapping. It read "128 specs name 186 distinct files" for long enough to be
// wrong by half, was corrected by hand to 251 and 1665 on 2026-11-14, and was
// wrong again one merge later. figures.go now recounts it against the specs, and
// TestEveryPublishedSpecFigureMatchesTheLiveCount fails on the three numbers in
// the line above (#937).
//
// `falsify:all` is the whole set and belongs to the night. What this prints is
// the subset the diff can possibly have broken.

// specFile is the part of a falsify spec this package reads, here and in
// censusOfSpecs: one reader, so the routing and the published count cannot
// disagree about what a spec says.
type specFile struct {
	Package   string `json:"package"`
	Mutations []struct {
		File    string `json:"file"`
		Test    string `json:"test"`
		Package string `json:"package"`
	} `json:"mutations"`
}

// The three links that earn a spec its replay, and the two sentences said when
// a spec or its tests cannot be read: unknown earns the replay, it never
// excuses it. Each is printed as the reason, so a
// reader sees which link fired rather than a bare command.
//
// The second and third links exist because a spec stops biting in three ways,
// not one: the guard it mutates changes, the spec itself changes, or the test
// its mutations are judged by changes. Measured on #965's own diff, which
// changed tools/falsify/specs/peering-pairs.json and
// internal/core/machine/incus_ovn_peering_test.go but not incus_ovn.go: the
// first link alone asked for no replay of that spec.
// TestASpecIsEarnedByEachOfItsThreeLinks fails without either new link.
const (
	becauseMutatesAChangedFile = "this spec mutates a file the diff touches, so it is the subset of " +
		"`falsify:all` that can have stopped biting"
	becauseTheSpecChanged    = "this spec changed, so its mutations may no longer apply or no longer bite"
	becauseANamedTestChanged = "a test this spec names changed, so the test judging its mutations " +
		"may have stopped biting"
	becauseATestCannotBeJudged = "a test file changed or was removed where this spec's tests may " +
		"live, and which tests it held, which package the spec means, or which tests its pattern " +
		"selects cannot be read, so the replay is earned rather than guessed away"
	becauseTheSpecCannotBeRead = "this spec cannot be read as JSON, so nothing else here can judge " +
		"it and the harness is what will say why it refuses"
)

// earned is one replay and one reason for it. A spec with several links
// earns several entries, one per link, and the plan prints each.
type earned struct {
	command string
	why     string
}

// falsifications lists the specs a diff has earned, each with the link that
// earned it. removed holds the `_test.go` files the diff deleted: they feed
// the test link alone, since a deleted test cannot be read to say what it held.
func falsifications(root string, paths, removed []string) []earned {
	changed := make(map[string]bool, len(paths))
	for _, p := range paths {
		changed[p] = true
	}
	tests := changedTests(root, paths, removed)

	dir := filepath.Join(root, "tools", "falsify", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No specs directory: a worktree without them, or a caller pointing at
		// a synthetic tree. Silence is right here and nowhere else in this
		// tool — the specs are an artefact this reads, not a claim it makes.
		return nil
	}

	var out []earned
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		specPath := "tools/falsify/specs/" + entry.Name()
		cmd := "mise run falsify -- " + specPath
		// Read before the body is parsed: a changed spec that no longer
		// parses is exactly the one whose replay must be asked for, and the
		// harness is what will say why it refuses.
		if changed[specPath] {
			out = append(out, earned{cmd, becauseTheSpecChanged})
		}
		// A spec that cannot be read is earned whatever the diff: skipping it
		// would answer "no link" for a spec nobody can say anything about.
		// TestASpecIsEarnedByEachOfItsThreeLinks/an_unreadable_spec_is_earned_whatever_the_diff
		// fails without it.
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		var spec specFile
		if err != nil || json.Unmarshal(body, &spec) != nil {
			out = append(out, earned{cmd, becauseTheSpecCannotBeRead})
			continue
		}
		for _, m := range spec.Mutations {
			if changed[m.File] {
				out = append(out, earned{cmd, becauseMutatesAChangedFile})
				break
			}
		}
		if why := judgedByAChangedTest(spec, tests); why != "" {
			out = append(out, earned{cmd, why})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].command < out[j].command })
	return out
}

// testFacts is what the changed test files of one package directory declare.
type testFacts struct {
	names map[string]bool
	// unreadable is set when a changed test file there cannot be read or
	// parsed, or was removed. Which tests it held is then unknown, and
	// unknown routes rather than excuses.
	unreadable bool
}

// changedTests reads the top-level functions each changed `_test.go` declares,
// keyed by its directory, which is the package a spec's `package` names. A
// removed test file marks its directory unreadable.
func changedTests(root string, paths, removed []string) map[string]*testFacts {
	facts := map[string]*testFacts{}
	of := func(p string) *testFacts {
		d := path.Dir(p)
		if facts[d] == nil {
			facts[d] = &testFacts{names: map[string]bool{}}
		}
		return facts[d]
	}
	for _, p := range removed {
		if strings.HasSuffix(p, "_test.go") {
			of(p).unreadable = true
		}
	}
	for _, p := range paths {
		if !strings.HasSuffix(p, "_test.go") {
			continue
		}
		f := of(p)
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(p)), nil, parser.SkipObjectResolution)
		if err != nil {
			f.unreadable = true
			continue
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				f.names[fn.Name.Name] = true
			}
		}
	}
	return facts
}

// judgedByAChangedTest answers the second link: does a changed test file, in
// the package a mutation declares, hold a test that mutation selects. It
// returns the reason to print, or "" when nothing links.
func judgedByAChangedTest(spec specFile, tests map[string]*testFacts) string {
	unknown := false
	for _, m := range spec.Mutations {
		pkg := m.Package
		if pkg == "" {
			pkg = spec.Package
		}
		selects, readable := testSelector(m.Test)
		for d, f := range tests {
			covered, known := packageCovers(pkg, d)
			if !known {
				unknown = true
				continue
			}
			if !covered {
				continue
			}
			if !readable || f.unreadable {
				unknown = true
				continue
			}
			for n := range f.names {
				if selects(n) {
					return becauseANamedTestChanged
				}
			}
		}
	}
	if unknown {
		return becauseATestCannotBeJudged
	}
	return ""
}

// packageCovers answers whether a package directory a diff names, such as
// `internal/core/machine`, is one the `go test` pattern a spec declares
// selects: `./internal/core/machine/`, `./internal/providers/...` or `./...`.
// The second result is false when the pattern is a shape this does not read,
// and the caller then routes rather than excuses.
//
// A spec that declares no package at all covers every directory: an
// over-asked replay costs a run, a missed one costs the guard.
func packageCovers(pkg, dir string) (covered, known bool) {
	p := strings.TrimSpace(pkg)
	if p == "" {
		return true, true
	}
	if p != "." && !strings.HasPrefix(p, "./") {
		return false, false
	}
	p = strings.TrimPrefix(p, "./")
	if rest, wild := strings.CutSuffix(p, "..."); wild {
		if strings.Contains(rest, "...") {
			return false, false
		}
		// `x/...` is x and everything under it; `x...` is any path that
		// starts with x, the way the go command reads it.
		if rest == "" {
			return true, true
		}
		if base, isDir := strings.CutSuffix(rest, "/"); isDir {
			return dir == base || strings.HasPrefix(dir, base+"/"), true
		}
		return strings.HasPrefix(dir, rest), true
	}
	if strings.Contains(p, "...") {
		return false, false
	}
	return path.Clean(p) == dir, true
}

// regexMeta is falsify.py's REGEX_META: the characters that make a `-run`
// element a regular expression rather than a name.
const regexMeta = `\.^$*+?()[]{}|`

// testSelector reads a mutation's `test` field the way falsify.py hands it to
// `go test -run`, and answers which top-level test names it selects.
//
// falsify.py's anchored() anchors a pattern whose every element is a plain
// name (`TestX` becomes `^TestX$`), so a plain top-level name selects itself
// alone. Any other pattern goes to `-run` unchanged, and `-run` matches each
// element as an unanchored regular expression with Go's regexp, the engine
// used here: `TestA|TestB` also selects TestAB. Elements split on `/` outside
// brackets and parentheses, as run_elements() does, and the first one is the
// top-level test (what follows is a subtest, declared inside it).
//
// The second result is false when the top element is empty or does not
// compile, and the caller then routes rather than excuses.
func testSelector(pattern string) (func(string) bool, bool) {
	elements := runElements(pattern)
	top := elements[0]
	if top == "" {
		return never, false
	}
	plain := true
	for _, e := range elements {
		if e == "" || strings.ContainsAny(e, regexMeta) {
			plain = false
		}
	}
	if plain {
		return func(name string) bool { return name == top }, true
	}
	re, err := regexp.Compile(top)
	if err != nil {
		return never, false
	}
	return re.MatchString, true
}

// never selects nothing. testSelector returns it with false rather than nil,
// so a caller that forgot to check the second result excuses instead of
// panicking, and the test that asserts the reason says so.
func never(string) bool { return false }

// runElements is falsify.py's run_elements: a `-run` pattern split on `/`
// outside brackets and parentheses.
func runElements(pattern string) []string {
	var out []string
	depth, start := 0, 0
	for i, ch := range pattern {
		switch ch {
		case '[', '(':
			depth++
		case ']', ')':
			depth = max(0, depth-1)
		case '/':
			if depth == 0 {
				out = append(out, pattern[start:i])
				start = i + 1
			}
		}
	}
	return append(out, pattern[start:])
}

// testOnly reports whether a path is compiled into tests and nothing else.
//
// A `_test.go` file and a `testdata/` tree cannot change an answer a real client
// reads, so no conformance leg can judge them — the same argument that makes
// prose prose wherever it lives. What judges them is `mise run check`, which
// prepush already runs, and the falsifications above, which is the half worth
// having: a change to a test is exactly the change that can stop a guard biting
// without turning anything red.
func testOnly(path string) bool {
	return strings.HasSuffix(path, "_test.go") ||
		strings.HasPrefix(path, "testdata/") ||
		strings.Contains(path, "/testdata/")
}
