package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A command that answers and that `--help` does not name is a command only
// someone reading the source will find.
//
// `shapes` was in exactly that state until 2026-08-13: it dispatched, it ran,
// it was counted in the written guide, and `feint --help` listed nineteen verbs
// where the switch accepted twenty. Reported by a reader exploring the CLI the
// way anyone does, which is the only way this class of defect surfaces — no
// test failed, because nothing compared the two lists.
//
// Adding the missing line fixes today. This compares the two lists so the
// twenty-first verb cannot ship invisible: the dispatch is read from the source
// rather than from a table kept beside it, on the same reasoning as the Outscale
// tag prefixes and the provider lists, both of which fell behind in silence this
// week.
func TestEveryDispatchedCommandIsInTheHelp(t *testing.T) {
	dispatched := topLevelCommands(t)
	if len(dispatched) < 15 {
		t.Fatalf("only %d commands found in the dispatch: the scan is broken, "+
			"not the help, and would otherwise pass while measuring nothing", len(dispatched))
	}

	// What a reader sees, rendered rather than read from a constant: the help
	// is written by a function, and comparing against its output is comparing
	// against the thing the user meets.
	var shown strings.Builder
	usage(&shown)
	help := shown.String()

	for _, name := range dispatched {
		// The help lists each verb as "  feint <name> ", which is also how a
		// reader finds it. Matching that shape rather than the bare word keeps
		// a mention inside a paragraph from passing for an entry.
		if !strings.Contains(help, "\n  feint "+name+" ") {
			t.Errorf("`feint %s` dispatches and `feint --help` never names it: "+
				"a reader exploring the CLI cannot discover it", name)
		}
	}
}

// A command the README never names is a command a reader decides against
// installing without knowing it exists.
//
// The help is the second page somebody reads; the README is the first, and it is
// the only one a reader sees before deciding whether to try this at all. Five of
// the twenty-two verbs were missing from it on 2026-08-16 — `evidence`, `images`
// and `shapes` outright, `stop` and `restart` folded into one entry — and ten
// were missing from the French one, which had quietly fallen a release behind.
//
// The defect is not the five lines. It is that the generated blocks are checked
// (`feint docs --check` regenerates the coverage tables and fails when they move)
// while the hand-written command list was checked by nobody, so the sixth would
// have gone the same way. This is the same test as the one above, one document
// further out.
//
// Both READMEs, because the French one is the one that fell behind: a check on
// the English one alone would have passed on the day the gap was widest.
func TestEveryCommandIsNamedInTheReadmes(t *testing.T) {
	dispatched := topLevelCommands(t)
	if len(dispatched) < 15 {
		t.Fatalf("only %d commands found in the dispatch: the scan is broken, "+
			"not the README, and would otherwise pass while measuring nothing", len(dispatched))
	}

	for _, doc := range []string{"README.md", "README.fr.md"} {
		body, err := os.ReadFile(filepath.Join("..", "..", doc))
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		page := string(body)
		var missing []string
		for _, name := range dispatched {
			// Spelled out — "feint stop", not `stop` inside an entry about
			// another verb. The grouped line `feint start` / `stop` / `restart`
			// is what the first version of this list did, and it is why two
			// verbs read as documented while nothing named them: a reader
			// searching the page for "feint stop" found nothing.
			if !strings.Contains(page, "feint "+name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s never names %d of the %d commands the binary dispatches: %s.\n"+
				"A verb no page names is one only a reader of the source will find.",
				doc, len(missing), len(dispatched), strings.Join(missing, ", "))
		}
	}
}

// topLevelCommands reads the verbs the first switch of run() accepts.
//
// From the source, because a list written beside the dispatch is a second copy
// that drifts — which is the defect this test exists to catch, one level up.
func topLevelCommands(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "cli.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cli.go: %v", err)
	}

	// The help aliases and the version flags answer without being verbs a
	// reader would look up in a command list.
	notAVerb := map[string]bool{
		"-h": true, "--help": true, "help": true,
		"-v": true, "--version": true,
	}

	var out []string
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		// The dispatch switches on args[1]; nothing else in this file does.
		index, ok := sw.Tag.(*ast.IndexExpr)
		if !ok {
			return true
		}
		if ident, ok := index.X.(*ast.Ident); !ok || ident.Name != "args" {
			return true
		}
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range clause.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				name, err := strconv.Unquote(lit.Value)
				if err != nil || notAVerb[name] {
					continue
				}
				out = append(out, name)
			}
		}
		found = true
		return false
	})
	if !found {
		t.Fatal("the dispatch switch on args[1] was not found in cli.go")
	}
	return out
}
