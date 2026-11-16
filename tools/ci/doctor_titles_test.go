package ci

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The conformance job asks `feint doctor` whether a run left a directory, by
// grepping its titles. #974 reworded those titles, and the step died on the
// night of PR #980 with a bare `grep` exit 1: "interception authority" and
// "outlived their" were no longer anything doctor printed. A rewording of
// doctor must redden this unit test, not the night.

const leftoverStep = "No interception authority outlived the run"

// grepPhrase is the single-quoted pattern of every grep in the step.
var grepPhrase = regexp.MustCompile(`grep\b[^'\n]*?'([^']+)'`)

// doctorLiterals is every string literal of internal/cli's non-test Go files:
// the titles doctor can print, whole or as the fixed part of a format.
func doctorLiterals(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "internal", "cli")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "doctor") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out = append(out, s)
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatalf("no string literal read from %s/doctor*.go: this test would judge nothing", dir)
	}
	return out
}

func printedByDoctor(literals []string, phrase string) bool {
	for _, s := range literals {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

// TestTheLeftoverStepGrepsForDoctorsOwnTitles is cited by the step in
// conformance.yml: each phrase it greps for is in a string doctor prints.
func TestTheLeftoverStepGrepsForDoctorsOwnTitles(t *testing.T) {
	block := stepBlock(readWorkflow(t, "conformance.yml"), leftoverStep)
	if block == "" {
		t.Fatalf("conformance.yml has no step %q: renamed, or the question is no longer asked", leftoverStep)
	}
	if !strings.Contains(block, "feint doctor --vm off") {
		t.Fatalf("the step no longer asks doctor:\n%s", block)
	}
	phrases := map[string]bool{}
	for _, m := range grepPhrase.FindAllStringSubmatch(block, -1) {
		phrases[m[1]] = true
	}
	// The three outcomes: none left, some left, a run still going. Fewer
	// phrases means one outcome is no longer told apart.
	if len(phrases) < 4 {
		t.Fatalf("the step greps for %d phrase(s), want the four of its outcomes: %v\n%s", len(phrases), phrases, block)
	}
	literals := doctorLiterals(t)
	for phrase := range phrases {
		if !printedByDoctor(literals, phrase) {
			t.Errorf("the step greps for %q, which no doctor title prints any more", phrase)
		}
	}
	// The accepting half's mirror: the phrases the step used before #974 are
	// refused by the same reading, so it can tell a stale phrase.
	for _, stale := range []string{"interception authority", "outlived their"} {
		if printedByDoctor(literals, stale) {
			t.Errorf("%q is still a doctor literal: this test cannot tell a stale phrase from a live one", stale)
		}
	}
}
