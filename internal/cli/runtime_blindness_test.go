package cli

import (
	"fmt"
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

// A provider pack may describe runtime intent, never runtime mechanics (#516).
//
// This is the second half of #511's door, and it is a different rule. #511
// constrains which primitives a pack may *call*: PackSurface names them and
// the compiler holds the rest, since emulator.Env carries no Driver. This one
// constrains what a pack may *know*. A pack can satisfy the whole call surface
// and still write `if mode == "incus-ovn" { … }` — legal through any facade,
// and already the abstraction leak that recreates the bugs. Capabilities exist
// so it is never needed: a pack branches on what is declared (Binding.Balances,
// capabilities.isolation), never on a runtime's name. CLAUDE.md already holds
// that rule for the conformance suites; this holds it for the packs.
//
// It is TestTheCoreNamesNoProvider read in the mirror: the core names no
// provider, a provider names no runtime.
//
// # Measured before it was written
//
// On this branch, 2026-08-26, over the 87 non-test files of the three packs:
// zero reference to machine.Incus, zero runtime config key, zero `/1.0/` path,
// zero mode comparison. What a case-insensitive sweep for incus|ovn finds is
// comments — dozens, each citing the measurement behind a limit — and two
// decline reasons, documentation-as-data, which are the exemptions below.
//
// So this is a ratchet, not a cleanup, and it is written in the position
// TestTheCoreNamesNoProvider was born in. Which means the only proof worth
// anything is the red: a test that passes on an already-clean tree proves it
// can pass, never that it can find. Each shape below was planted in a real
// pack and shown red before this landed, and tools/falsify/specs/
// runtime-blindness.json keeps them plantable.

// runtimeTell is one thing in a pack's source that only somebody who knows what
// is behind --vm could have written.
//
// The match runs against **string literal values and the type names a pack
// selects out of the machine package** — never against comment text. That
// distinction is the hard part of this detector and it is measured, not
// assumed: a naive line scan over the packs reports thirty-odd hits and
// twenty-eight of them are prose citing a measurement ("editing a live OVN NIC
// re-plugs it"), which is the provenance of a limit and is worth keeping. #511
// paid the same lesson from the other side — a grep reported four
// machine.PeerNetworks in the packs where the AST scanner sees zero, all four
// in comments.
type runtimeTell struct {
	// name is what a failure and an exemption key call this tell.
	name string
	// match is applied to a string literal's *value*, case-insensitively where
	// the word could be capitalised in prose.
	match *regexp.Regexp
	// why says what makes it mechanics rather than intent, so a failure is
	// actionable without opening this file.
	why string
}

// declaredRuntimeTells is the vocabulary of the runtime that is not derivable
// from a type name: the technologies a mode is built on, the configuration keys
// the driver writes, its REST root, and the host-side device names it hands
// out.
//
// Declared rather than derived, and therefore at risk of becoming decoration —
// which is what TestTheRuntimeVocabularyIsTheRuntimesOwn refuses: every entry
// here must still match something the runtime's own sources write. A key the
// driver stops using fails there rather than silently guarding nothing, the
// same discipline mustStayOutside carries in driver_surface_test.go.
var declaredRuntimeTells = []runtimeTell{
	{
		name:  "ovn",
		match: regexp.MustCompile(`(?i)\bovn\b`),
		why: "OVN is the network technology one mode is built on, and the only mode that can " +
			"isolate two VPCs. A pack that names it is branching on which runtime is behind the " +
			"driver instead of on what the driver declares — capabilities.isolation is the question",
	},
	{
		name:  "kvm",
		match: regexp.MustCompile(`(?i)\bkvm\b`),
		why: "kvm is the alias --vm accepts for incus-vm: a virtualisation choice, which is the " +
			"operator's and never the pack's",
	},
	{
		name:  "security.acls",
		match: regexp.MustCompile(`(?i)security\.acls`),
		why: "the runtime key a rule set is attached to a network or a NIC under. A pack declares " +
			"a FirewallSpec and hands it to GroupSync; how it lands on the host is the driver's",
	},
	{
		name:  "ipv4.routes",
		match: regexp.MustCompile(`(?i)ipv4\.routes`),
		why: "the runtime key a routed public address is written to. A pack asks Reconciler.Route " +
			"for an address that must reach a machine, and says nothing about how",
	},
	{
		name:  "ipv4.address",
		match: regexp.MustCompile(`(?i)ipv4\.address`),
		why:   "the runtime key a network's gateway and a routed NIC's addresses are written to",
	},
	{
		name:  "ipv4.nat",
		match: regexp.MustCompile(`(?i)ipv4\.nat`),
		why: "the runtime key outbound access is switched with. A pack declares BackingNetwork.NAT; " +
			"the spelling on the host is the driver's",
	},
	{
		name:  "nictype",
		match: regexp.MustCompile(`(?i)nictype`),
		why:   "the runtime key that says a NIC is routed rather than bridged",
	},
	{
		name:  "runtime API path",
		match: regexp.MustCompile(`/1\.0/`),
		why: "/1.0/ is the runtime's own REST root, which the driver queries directly. A pack " +
			"reaching it has stopped going through any layer at all",
	},
	{
		name:  "host device name",
		match: regexp.MustCompile(`(?i)\beth[0-9]+\b`),
		why: "the host-side device name the driver allocates. Which interface an attachment becomes " +
			"is the driver's arithmetic — a pack declares a Plan and reads addresses back",
	},
}

// runtimeImplementationImports are the packages a pack may not import: a
// runtime implementation, or the means to drive one without naming it.
//
// An entry ending in "/" is a prefix; the others are exact. os/exec is the
// blunt one and the one that matters most: a pack that runs a command has
// bypassed Binding.ours, the driver's mustOwn and every ownership check the
// shared layer holds, with a name that arrived through PUT /_feint/state.
var runtimeImplementationImports = []struct{ path, why string }{
	{
		path: "os/exec",
		why: "running a command is how the driver reaches the host, and a pack that does it has " +
			"walked past every ownership check the shared layer holds on names that arrive by snapshot",
	},
	{
		path: "github.com/stephrobert/feint/internal/core/machine/",
		why: "a subpackage of the machine package is where a runtime implementation would live; " +
			"the packs import the machine package itself, and nothing under it",
	},
}

// runtimeKnowledgeExemptions lists the places a pack legitimately writes a word
// of the runtime's vocabulary, each with the reason — the discipline Declined()
// applies to a refusal, applied to knowledge, and the shape
// TestEveryBarrageExemptionSaysWhy already holds for the maps beside it.
//
// Keyed "<pack>/<file>:<tell>" rather than "<pack>/<file>": a file excused for
// naming a mode in a decline reason is not thereby excused for reading a
// runtime configuration key, and a whole-pack key would excuse both.
//
// Both entries are emulator.Because strings — documentation-as-data, served on
// /_feint/declined and read by a human deciding whether an operation is worth
// asking for. Naming the mode is the honest half of those sentences: the answer
// changes with the mode, and a reason that hid that would be worse writing, not
// safer code. The detector sees them because it reads literals, which is
// deliberate — a decline reason and a mode comparison are the same token to any
// scanner, and the difference is a judgement somebody has to write down here.
var runtimeKnowledgeExemptions = map[string]string{
	"outscale/declined.go:incus": "the decline reason for ReadVmsHealth, served on /_feint/declined: it says nothing " +
		"probes a backend under --vm off and nothing probes one under incus-ovn either, where the " +
		"runtime does distribute connections but reports no per-backend health. The two modes are " +
		"the measurement, and a reason that dropped them would claim a limit it never established",
	"outscale/declined.go:ovn": "the same sentence, which names the mode in full. Its subject is what a reader may " +
		"expect from each mode, not what the pack does with either — no branch reads this string",
	"scaleway/pack.go:ovn": "the decline reason for ListSubnetOverlaps, which says the VPC connectors stay declined " +
		"until OVN mode has measured peering. It names the mode because the condition of return is " +
		"the mode, and the pack itself never asks which mode is running",
}

// runtimeLeak is one place a pack's source names runtime mechanics.
type runtimeLeak struct {
	tell  string // "incus", "security.acls", "os/exec"
	pack  string // "scaleway"
	file  string // "pack.go"
	where string // "internal/providers/scaleway/pack.go:713"
	how   string // "a string literal", "the type machine.Incus", "an import"
	why   string
}

// runtimeScan is what one pass over a pack saw, so a silent pass can be told
// from a clean one.
type runtimeScan struct {
	leaks    []runtimeLeak
	files    int
	literals int
}

// driverImplementations names the exported types of internal/core/machine that
// implement machine.Driver, derived from the package's own declarations.
//
// Derived rather than listed, and for the reason #511 wrote down after three
// wrong counts of the same surface: a list somebody remembered is a list that
// stops being true. A fourth runtime — the remote or libvirt driver #514 leaves
// the door open for — becomes forbidden vocabulary the day it compiles, with
// nobody having to remember this file exists.
func driverImplementations(t *testing.T, pkg machinePackage) []string {
	t.Helper()
	var methods []string
	for key := range pkg.members {
		if typ, method, ok := strings.Cut(key, "."); ok && typ == "Driver" {
			methods = append(methods, method)
		}
	}
	if len(methods) < 5 {
		t.Fatalf("machine.Driver reads as %d method(s): the derivation is broken, and a broken "+
			"derivation names every type an implementation or none", len(methods))
	}
	var impls []string
	for name, kind := range pkg.names {
		if kind != "type" || name == "Driver" {
			continue
		}
		complete := true
		for _, method := range methods {
			if !pkg.members[name+"."+method] {
				complete = false
				break
			}
		}
		if complete {
			impls = append(impls, name)
		}
	}
	sort.Strings(impls)
	// Noop and Incus at the very least, since one is the default and the other
	// is the only runtime. A derivation that found one found the interface and
	// missed the types.
	if len(impls) < 2 {
		t.Fatalf("only %v implement machine.Driver: the derivation is broken, and its silence "+
			"about the rest would read as a pack naming no runtime", impls)
	}
	return impls
}

// runtimeTells is the whole vocabulary: the derived implementation names, then
// the declared technologies, keys, paths and device names.
func runtimeTells(impls []string) []runtimeTell {
	tells := make([]runtimeTell, 0, len(impls)+len(declaredRuntimeTells))
	for _, impl := range impls {
		tells = append(tells, runtimeTell{
			name: strings.ToLower(impl),
			// A substring, not a word: "incus-ovn", "incusbr0" and "incus-vm"
			// are all the same knowledge, and only the first would survive a
			// word boundary.
			match: regexp.MustCompile(`(?i)` + regexp.QuoteMeta(impl)),
			why: "machine." + impl + " implements machine.Driver: it is one of the runtimes behind " +
				"--vm, and which one is running is the operator's business and never the pack's",
		})
	}
	return append(tells, declaredRuntimeTells...)
}

// runtimeKnowledgeLeaks reads one pack's non-test sources and reports every
// place they name runtime mechanics.
//
// Non-test on purpose, and for the reason #511 measured: a pack's tests
// legitimately build fake drivers and name modes — that is how a pack is tested
// at all — so counting them measures the harness.
//
// What it reads, and this is the whole design: an import path, a type selected
// out of the machine package, and the *value* of a string literal. Not a line,
// not a comment.
//
// The file is parsed *with* its comments and the walk then refuses them
// explicitly, which is deliberate and costs one parser flag. Parsing without
// them would exclude comments by accident — nothing in the AST to meet — and an
// exclusion by accident cannot be falsified: there would be no line to
// neutralise, and "this detector ignores comments" would be a sentence in a doc
// comment, which is the exact shape CLAUDE.md calls un commentaire n'est pas un
// contrôle. Written as code, tools/falsify/specs/runtime-blindness.json turns
// the refusal into a scan and the positive control goes red on the fixture's
// own prose.
func runtimeKnowledgeLeaks(t *testing.T, dir string, tells []runtimeTell, impls map[string]bool) runtimeScan {
	t.Helper()
	root := repoRoot(t)
	pack := filepath.Base(dir)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("list %s: %v", dir, err)
	}

	var scan runtimeScan
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		tree, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		scan.files++
		// The same precondition driver_surface_test.go states: every
		// machine.X below is resolved by that one name, so a file importing
		// the package as something else would read as naming no runtime at
		// all — a silent empty scan, which is the failure this file exists to
		// refuse.
		if alias := aliasedMachineImport(tree); alias != "" {
			t.Fatalf("%s imports the machine package as %q: the implementation-name scan resolves "+
				"machine.X by the name %q, so this file would be read as naming no runtime. Drop "+
				"the alias, or teach the scanner to carry one per file", file, alias, machinePackageName)
		}
		rel, relErr := filepath.Rel(root, file)
		if relErr != nil {
			rel = file
		}
		base := filepath.Base(file)
		add := func(tell, how, why string, node ast.Node) {
			scan.leaks = append(scan.leaks, runtimeLeak{
				tell:  tell,
				pack:  pack,
				file:  base,
				where: fmt.Sprintf("%s:%d", rel, fset.Position(node.Pos()).Line),
				how:   how,
				why:   why,
			})
		}

		ast.Inspect(tree, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CommentGroup:
				// A comment is not behaviour, and the packs' comments are the
				// provenance of their limits: "editing a live OVN NIC re-plugs
				// it" appears three times, each beside the code that pays for
				// it. Refusing them here rather than never seeing them is what
				// makes this exclusion a decision somebody can falsify.
				return false
			case *ast.ImportSpec:
				// Returning false keeps the path out of the literal scan
				// below: an import is judged as an import, once.
				path, err := strconv.Unquote(n.Path.Value)
				if err != nil {
					return false
				}
				for _, forbidden := range runtimeImplementationImports {
					hit := path == forbidden.path
					if strings.HasSuffix(forbidden.path, "/") {
						hit = strings.HasPrefix(path, forbidden.path)
					}
					if hit {
						add(forbidden.path, "an import of "+path, forbidden.why, n)
					}
				}
				return false
			case *ast.SelectorExpr:
				id, ok := n.X.(*ast.Ident)
				if !ok || id.Name != machinePackageName {
					return true
				}
				// Containment rather than equality, because the constructors
				// carry the name too: machine.NewIncusOVN() is the same
				// knowledge as machine.Incus and needs no driver value to
				// write. Nothing PackSurface admits contains an
				// implementation's name, so the accepting half is unaffected —
				// TestNoPackReachesPastTheDeclaredDriverSurface holds the rest.
				lowered := strings.ToLower(n.Sel.Name)
				for impl := range impls {
					if strings.Contains(lowered, strings.ToLower(impl)) {
						add(strings.ToLower(impl), "the name machine."+n.Sel.Name,
							"machine."+impl+" implements machine.Driver: naming it, or anything "+
								"built from it, is knowing which runtime is behind --vm", n)
						break
					}
				}
				return true
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				scan.literals++
				value, err := strconv.Unquote(n.Value)
				if err != nil {
					value = n.Value
				}
				for _, tell := range tells {
					if tell.match.MatchString(value) {
						add(tell.name, "a string literal", tell.why, n)
					}
				}
				return true
			}
			return true
		})
	}
	return scan
}

// No pack knows which runtime is behind the driver (#516).
//
// It reports the pack, the tell and the line, because a discipline failure that
// only says "a pack knows too much" is a failure somebody has to reproduce
// before they can act on it.
//
// # What this cannot see, stated rather than implied
//
// A detector that does not say where it stops reads as one that sees
// everything, and this repository has paid for that reading seven times in a
// day. So:
//
//   - A word assembled at run time. `"in"+"cus"`, fmt.Sprintf("incus-%s", …),
//     a mode read from the environment or from /_feint/health: no literal
//     carries the word, and nothing here matches. The compiler half of #511 is
//     what makes that path uninteresting — a pack cannot obtain a driver to ask
//     — but it is not this test that closes it.
//   - Knowledge with no name. A pack branching on a *proxy* for the mode — the
//     number of declared capabilities, the shape of an error string, a timing —
//     names nothing and is invisible here. Only the runtime witness suite
//     (#486) sees the consequence.
//   - Comments, deliberately. The provenance of a measured limit belongs beside
//     the code that pays for it, and the packs carry dozens of such sentences.
//     The positive control asserts this rather than the comment above asserting
//     it.
//   - A mode name reached through some other package's identifier —
//     environment.RuntimeModes indexed, a constant somebody adds elsewhere.
//     Only machine.X is resolved here, because that is the package whose
//     boundary this milestone draws.
//   - A second leak of an already-exempted word in an already-exempted file.
//     The key is <pack>/<file>:<tell>, which is the granularity that keeps a
//     decline reason from excusing a configuration key in the same file; it
//     does not count occurrences. Widening to the literal's own text was the
//     alternative and it rots on every reworded sentence.
//   - Test files, deliberately: a pack's own tests build fake drivers.
//   - Anything outside internal/providers/**.
//
// The residue is what #514 calls honest: forgetting cannot be made impossible,
// only visible in more than one place.
func TestNoPackKnowsWhichRuntimeIsBehindTheDriver(t *testing.T) {
	pkg := readMachinePackage(t)
	impls := driverImplementations(t, pkg)
	named := map[string]bool{}
	for _, impl := range impls {
		named[impl] = true
	}
	tells := runtimeTells(impls)

	var offences []string
	files, literals := 0, 0
	for _, dir := range packDirs(t) {
		scan := runtimeKnowledgeLeaks(t, dir, tells, named)
		files += scan.files
		literals += scan.literals
		for _, leak := range scan.leaks {
			if reason := runtimeKnowledgeExemptions[leak.pack+"/"+leak.file+":"+leak.tell]; reason != "" {
				continue
			}
			offences = append(offences, fmt.Sprintf("%s names %q at %s, as %s — %s",
				leak.pack, leak.tell, leak.where, leak.how, leak.why))
		}
	}

	// A scan that parsed nothing looks exactly like three runtime-blind packs.
	// Both floors are well under what the tree holds — 87 files and some
	// thousands of literals on 2026-08-26 — and well over zero.
	if files < 50 || literals < 500 {
		t.Fatalf("the scan read %d pack file(s) and %d string literal(s): it is broken, and a "+
			"broken scan reports packs that name no runtime", files, literals)
	}

	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("%d place(s) where a pack names the runtime behind the driver. A pack describes "+
			"intent — this machine belongs to these networks, these addresses must reach it, this "+
			"rule set allows this traffic — and never mechanics. If the shared layer cannot express "+
			"the behaviour, the driver gains a service; if a documentation string must genuinely "+
			"name a mode, add \"<pack>/<file>:<tell>\" to runtimeKnowledgeExemptions with the "+
			"reason:\n  %s", len(offences), strings.Join(offences, "\n  "))
	}
}

// The vocabulary is the runtime's own, and still is.
//
// Same reason TestTheDeclaredDriverSurfaceIsSmallerThanThePackage exists beside
// the surface test: a declared list nobody checks becomes decoration, and a
// configuration key the driver stopped writing would go on reading as a guard
// while guarding nothing. Every declared tell must still match something the
// runtime's own sources write — the driver package, and the mode table in
// internal/cli that chooses a driver, which is where the aliases `ovn` and
// `kvm` live.
func TestTheRuntimeVocabularyIsTheRuntimesOwn(t *testing.T) {
	root := repoRoot(t)
	sources := []string{filepath.Join(root, "internal", "cli", "cli.go")}
	found, err := filepath.Glob(filepath.Join(root, "internal", "core", "machine", "*.go"))
	if err != nil {
		t.Fatalf("list the machine package: %v", err)
	}
	for _, file := range found {
		if !strings.HasSuffix(file, "_test.go") {
			sources = append(sources, file)
		}
	}
	if len(sources) < 20 {
		t.Fatalf("only %d runtime source(s) to read: every tell below would fail for want of a "+
			"denominator", len(sources))
	}

	// Tokens, for the same reason the pack scan reads tokens: the driver's own
	// comments discuss its keys at length, and a vocabulary verified against
	// prose would survive the key itself being deleted.
	var vocabulary []string
	for _, file := range sources {
		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(tree, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.Ident:
				vocabulary = append(vocabulary, n.Name)
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if value, err := strconv.Unquote(n.Value); err == nil {
						vocabulary = append(vocabulary, value)
					}
				}
			}
			return true
		})
	}

	for _, tell := range declaredRuntimeTells {
		if len(strings.Fields(tell.why)) < 8 {
			t.Errorf("the tell %q says %q, which does not tell a reader why it is mechanics",
				tell.name, tell.why)
		}
		used := false
		for _, token := range vocabulary {
			if tell.match.MatchString(token) {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("nothing in the driver package or the mode table writes %q any more: a tell "+
				"the runtime no longer uses is a guard nobody can violate, which is not a guard",
				tell.name)
		}
	}

	for _, forbidden := range runtimeImplementationImports {
		if len(strings.Fields(forbidden.why)) < 8 {
			t.Errorf("the forbidden import %q says %q, which is not a reason a reviewer can weigh",
				forbidden.path, forbidden.why)
		}
		if strings.HasSuffix(forbidden.path, "/") {
			// A prefix guards a shape rather than a package: nothing lives
			// under internal/core/machine today. What is checkable is that the
			// prefix still names a real place, so a moved package leaves the
			// entry pointing at nothing and this says so.
			dir := filepath.Join(root, strings.TrimPrefix(strings.TrimSuffix(forbidden.path, "/"),
				"github.com/stephrobert/feint/"))
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Errorf("the forbidden import prefix %q names %s, which is not a directory: the "+
					"entry guards nothing", forbidden.path, dir)
			}
			continue
		}
		imported := false
		for _, file := range sources {
			tree, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			for _, spec := range tree.Imports {
				if path, err := strconv.Unquote(spec.Path.Value); err == nil && path == forbidden.path {
					imported = true
				}
			}
		}
		if !imported {
			t.Errorf("the runtime no longer imports %q, so forbidding it to the packs guards a "+
				"path nobody takes", forbidden.path)
		}
	}
}
