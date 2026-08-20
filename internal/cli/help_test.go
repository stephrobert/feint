package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// The help names every flag the binary accepts, and no flag it refuses.
//
// This is the assertion the frozen CLI surface used to stand in for, and could
// not (#334). That surface was built by parsing this very help, so the two
// agreed by construction and measured nothing: `feint proxy --intercept`
// shipped in v0.9.0 accepted by the binary, named by `feint proxy --help`,
// absent from `feint --help`, and therefore absent from the frozen surface for
// six days. The surface now comes from the FlagSets, which leaves the help
// needing a check of its own — with a subject on each side, rather than one
// parser standing in for both.
//
// Both directions are asserted, and the second is the one that was missing:
//
//   - a flag on a FlagSet that no help block names: a reader of `feint --help`
//     cannot discover it, which is exactly how --intercept shipped mute;
//   - a flag a help block names that no FlagSet registers: a reader types it
//     and the binary answers "flag provided but not defined". `feint version`
//     was in that state — its block named --version and -v, which are aliases
//     of the verb and not flags of it — and so was `feint snapshot`, whose
//     block mentioned `serve --state` in a sentence about file formats.
//
// Both are compared per verb, on the help's own granularity: `feint --help`
// gives `snapshot` one block, so that block is measured against the flags of
// "snapshot save", "snapshot load" and "snapshot list" together.
func TestTheHelpNamesEveryFlagTheBinaryAccepts(t *testing.T) {
	named := flagsTheHelpNames(t)
	if len(named) < 15 {
		t.Fatalf("only %d verbs parsed out of the help: the parser is broken, not the help, "+
			"and would otherwise pass while measuring nothing", len(named))
	}

	accepted := map[string]map[string]bool{}
	for set, flags := range flagsTheBinaryAccepts(t) {
		verb, _, _ := strings.Cut(set, " ")
		if accepted[verb] == nil {
			accepted[verb] = map[string]bool{}
		}
		for _, name := range flags {
			accepted[verb][name] = true
		}
	}
	if len(accepted) < 15 {
		t.Fatalf("only %d verbs built a flag set: the observation is broken, not the help", len(accepted))
	}

	for verb, flags := range accepted {
		block, described := named[verb]
		if !described {
			t.Errorf("the binary dispatches `feint %s` and `feint --help` renders no block for it, "+
				"so none of its flags can be named there", verb)
			continue
		}
		shown := map[string]bool{}
		for _, name := range block {
			shown[name] = true
		}
		for name := range flags {
			if !shown[name] {
				t.Errorf("the binary accepts `feint %s %s` and `feint --help` never names it: "+
					"a flag only the source and `feint %s --help` know about is a flag that shipped mute",
					verb, name, verb)
			}
		}
		for name := range shown {
			if !flags[name] {
				t.Errorf("`feint --help` names `feint %s %s` and no flag set registers it: "+
					"a reader who types it is answered \"flag provided but not defined\"", verb, name)
			}
		}
	}

	for verb := range named {
		if _, dispatches := accepted[verb]; !dispatches {
			t.Errorf("`feint --help` renders a block for `feint %s` and no such verb builds a flag set", verb)
		}
	}
}

// flagsTheHelpNames parses the rendered help: a line "  feint <verb> " opens a
// verb, its indented continuation lines belong to it, a line at column zero
// closes it. Flags are every "-x" / "--xyz" token in the verb's block.
//
// Parsing is the right tool here and the wrong one for the frozen surface, and
// the difference is the subject: this reads the document *in order to check it*
// against the FlagSets, where the surface used to read the document and call
// the result the binary's behaviour.
func flagsTheHelpNames(t *testing.T) map[string][]string {
	t.Helper()
	var rendered strings.Builder
	usage(&rendered)

	verbLine := regexp.MustCompile(`^  feint ([a-z][a-z-]*) `)
	// A flag token starts after a space, bracket, parenthesis or quote — never
	// in the middle of a word, so "read-only" and "incus-vm" stay words.
	flagToken := regexp.MustCompile(`(?:^|[\s\[("])(-{1,2}[a-zA-Z][a-zA-Z0-9-]*)`)

	verbs := map[string][]string{}
	current := ""
	seen := map[string]map[string]bool{}
	for _, line := range strings.Split(rendered.String(), "\n") {
		if m := verbLine.FindStringSubmatch(line); m != nil {
			current = m[1]
			if verbs[current] == nil {
				verbs[current] = []string{}
				seen[current] = map[string]bool{}
			}
		} else if !strings.HasPrefix(line, " ") {
			// Prose at column zero: the preamble, the exit-code paragraph, the
			// note on the version aliases. Whatever dashes they carry belong to
			// no verb.
			current = ""
			continue
		}
		if current == "" {
			continue
		}
		for _, m := range flagToken.FindAllStringSubmatch(line, -1) {
			name := m[1]
			if !seen[current][name] {
				seen[current][name] = true
				verbs[current] = append(verbs[current], name)
			}
		}
	}
	for _, flags := range verbs {
		sort.Strings(flags)
	}
	return verbs
}
