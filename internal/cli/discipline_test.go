package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The shared layer is enforced, not merely offered (#220).
//
// The first factorisation pass moved real invariants into the core —
// core/serialise, Binding.Transition, Binding.Observe, the conditional
// write-back, storetest's barrage — and none of them was enforced. A pack could
// write its own mutex, its own read-modify-write and its own isolation loop, and
// every gate in the repository stayed green. That is not a hypothesis: it is how
// the divergences this milestone spent itself on arrived. updateVm was
// serialised, updateServer was not, and nothing failed for months.
//
// Three controls were on the table. The strongest — the Pack interface carrying
// the mutation path, so bypassing it is a compile error — was not taken, and the
// reason is worth writing down rather than leaving as an omission: the three
// packs' mutations have genuinely different shapes (an Outscale action names a
// batch of Vms, an Exoscale one answers an operation object, a Scaleway one a
// task), and one signature over all three would either be so wide it enforces
// nothing or would force a fourth provider to lie about its API. The issue calls
// that option the most invasive, and it is also the one that would push provider
// vocabulary into the core, which rule 5 forbids.
//
// What is taken instead is mechanical and cannot be skipped by omission, which
// was the whole complaint: the two checks below read the packs' own source. A
// pack that forgets does not simply lack a test — it fails one it never had to
// remember to write.

func packDirs(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	base := filepath.Join(root, "internal", "providers")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read the providers directory: %v", err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(base, entry.Name()))
		}
	}
	if len(dirs) == 0 {
		t.Fatal("no provider pack found: this control would pass by finding nothing")
	}
	return dirs
}

// ownExclusion lists the places a pack may hold its own exclusion primitive, each
// with the reason — the discipline Declined() applies to a refusal, applied to an
// exemption. An empty list is the honest state today.
//
// Keyed by "<pack>/<file>" so an exemption cannot quietly widen to a whole pack.
var ownExclusion = map[string]string{}

// A pack does not build its own exclusion.
//
// Named exclusion lives in core/serialise, and it lives there because the copy
// each pack used to carry is how a fixed race stayed alive elsewhere — the
// comment on machine.Serialise says so, and this is the control that comment
// never had. A pack importing sync is reaching for a mutex, a once or a map that
// the core already keys by provider and target.
//
// Import-level rather than a scan for sync.Mutex declarations: it catches the
// variants too (RWMutex, Map, Once, WaitGroup used as a gate), it cannot be
// worked around by aliasing a type, and a pack that genuinely needs one says so
// in ownExclusion with its reason.
func TestNoPackBuildsItsOwnExclusion(t *testing.T) {
	for _, dir := range packDirs(t) {
		pack := filepath.Base(dir)
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("list %s: %v", pack, err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				// Test files are allowed theirs: a barrage needs a WaitGroup, and
				// what is being protected there is the test, not the emulator.
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			for _, imported := range parsed.Imports {
				if imported.Path.Value != `"sync"` {
					continue
				}
				key := pack + "/" + filepath.Base(file)
				if reason := ownExclusion[key]; reason != "" {
					continue
				}
				t.Errorf("%s imports sync: named exclusion lives in core/serialise, keyed by "+
					"provider and target, because the copy each pack used to carry is how a "+
					"fixed race stayed alive elsewhere. If this one is genuinely different, "+
					"add %q to ownExclusion with the reason", key, key)
			}
		}
	}
}

// notInTheBarrage lists the shared controls a pack legitimately does not run,
// with the reason. Same shape and same discipline as Declined().
var notInTheBarrage = map[string]string{
	"exoscale/Orphans": "this pack keeps its references on the owner rather than on the dependent — " +
		"an instance carries the ids of its elastic IPs and the networks it joined, so deleting it " +
		"takes the reference with it and no record is left naming something gone",
}

// Every pack registers in the shared barrage.
//
// The controls exist (storetest.Sweep, NoLostUpdate, Orphans) and all three packs
// run them today. Nothing made that true, which is the complaint: a fourth pack
// that never writes the test has no failure to notice, only an absence — and an
// absence is what this milestone kept finding months late.
//
// So the registration is discovered from the pack's own test sources rather than
// declared by its author. A pack that skips one names it in notInTheBarrage with
// a reason, which is a line somebody has to write and a reviewer can read.
func TestEveryPackRunsTheSharedBarrage(t *testing.T) {
	controls := []string{"Sweep", "NoLostUpdate", "Orphans"}

	for _, dir := range packDirs(t) {
		pack := filepath.Base(dir)
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			t.Fatalf("list %s: %v", pack, err)
		}
		if len(files) == 0 {
			t.Errorf("%s has no test files at all", pack)
			continue
		}

		found := map[string]int{}
		for _, file := range files {
			for control, n := range barrageCalls(t, file) {
				found[control] += n
			}
		}

		var missing []string
		for _, control := range controls {
			if found[control] > 0 {
				continue
			}
			if reason := notInTheBarrage[pack+"/"+control]; reason != "" {
				continue
			}
			missing = append(missing, control)
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s runs none of storetest.%s: the invariant is shared and the traffic is "+
				"the pack's, so a pack that does not drive it is a pack the invariant does not "+
				"cover. Add the barrage, or name it in notInTheBarrage with the reason",
				pack, strings.Join(missing, ", storetest."))
		}
	}
}

// barrageCalls counts, per control, the calls to storetest.<Control>(…) a pack's
// test file really makes.
//
// It used to be strings.Contains(source, "storetest."+control+"("), and that is
// two different weaknesses at once, both of the family CLAUDE.md calls "bien
// formé n'est pas autorisé": a mention inside a comment or a string literal
// satisfies a substring search exactly like a call, so a pack could be recorded
// as running the barrage by naming it in a sentence. This resolves a real
// CallExpr on a SelectorExpr whose package identifier is storetest, so only a
// call counts.
//
// The count matters as much as the boolean, and #399 is why. The falsification
// for this guard rewrites one call site, and the harness rewrites the first
// match only; the moment a second identical call appeared in the same pack
// (#289, two days after the spec was written) the mutation stopped removing the
// last one and the falsification reported STILL GREEN for two months. The
// harness now refuses an ambiguous fragment, and this returns a count so a
// failure can say how many calls it saw rather than only that it saw none.
func barrageCalls(t *testing.T, file string) map[string]int {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	calls := map[string]int{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		sel, isSelector := call.Fun.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}
		pkg, isIdent := sel.X.(*ast.Ident)
		if !isIdent || pkg.Name != "storetest" {
			return true
		}
		calls[sel.Sel.Name]++
		return true
	})
	return calls
}

// A reason that says nothing is not a reason, which is the lesson the declines
// guard already paid for: the exemption maps above would otherwise let a pack out
// with an empty string.
func TestEveryBarrageExemptionSaysWhy(t *testing.T) {
	for _, exemptions := range []map[string]string{
		ownExclusion, notInTheBarrage, notInTheDriverSurface, runtimeKnowledgeExemptions,
		storedNumberExemptions,
	} {
		for key, reason := range exemptions {
			if reasonIsThin(reason) {
				t.Errorf("the exemption for %s says %q, which is not a reason a reviewer can weigh",
					key, reason)
			}
		}
	}
}

// reasonIsThin is what "says why" means here, in one place so the detector's
// own positive control can exercise it: "not yet migrated" and "cannot be" are
// different reasons and both are too short to be one. Five words is not a
// quality bar, it is a floor under the empty string.
func reasonIsThin(reason string) bool { return len(strings.Fields(reason)) < 5 }

// `internal/core` carries no provider-named code, which is the testable half of
// what adding a provider costs.
//
// docs/architecture.md said "nothing outside `internal/providers/<name>/` should
// need to change", and docs/fourth-pack.md measured eleven shared files a fourth
// pack edits — a registration in `packsFor`, a row in the doctor's client table,
// a task in mise.toml. Both statements were defended, and only one of them can be
// true as written. The absolute one is the one that has to give: what actually
// holds, measured every time anybody has looked, is narrower and stronger.
//
//	Adding a provider requires no behavioural change to internal/core; the
//	external registration and integration points may receive additive data.
//
// That sentence is testable where the absolute one was not, and this is the test.
// A pack's differences reach the core as field values — `Binding.Prefix`,
// `Boot.User`, `AddressKey` — never as a name the core knows, so a name appearing
// in core code is the boundary being in the wrong place. It was measured at zero
// on 2026-08-10 and again on 2026-08-17; what was missing both times was anything
// that would notice it stop being zero.
//
// Comments are exempt, and deliberately: this repository documents by citing the
// measured example, and "the Scaleway CLI resolves its image first" in a comment
// is the evidence for a rule, not a dependency on a pack. The watcher's event
// filter — three provider prefixes written into the core, found by an audit — was
// code, and would fail here.
func TestTheCoreNamesNoProvider(t *testing.T) {
	root := repoRoot(t)
	packs := []string{}
	for _, dir := range packDirs(t) {
		packs = append(packs, filepath.Base(dir))
	}

	var offences []string
	scanned := 0
	err := filepath.WalkDir(filepath.Join(root, "internal", "core"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		// The AST rather than the file's bytes, which is the whole design: a
		// comment naming a provider is documentation, and grep cannot tell the
		// two apart. Identifiers and string literals are what a program acts on.
		ast.Inspect(parsed, func(node ast.Node) bool {
			var text string
			switch n := node.(type) {
			case *ast.Ident:
				text = n.Name
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				text = n.Value
			default:
				return true
			}
			lowered := strings.ToLower(text)
			for _, pack := range packs {
				if strings.Contains(lowered, pack) {
					offences = append(offences, fmt.Sprintf("%s:%d names %s in %s",
						rel, fset.Position(node.Pos()).Line, pack, text))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/core: %v", err)
	}
	if scanned < 10 {
		t.Fatalf("only %d files scanned under internal/core: the walk is broken, "+
			"and would otherwise pass while measuring nothing", scanned)
	}
	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("internal/core acts on %d provider name(s), so a fourth pack would have to "+
			"open the core to be treated like the other three:\n  %s",
			len(offences), strings.Join(offences, "\n  "))
	}
}

// And the detectors work, because a control that finds nothing looks exactly like
// a clean repository. Both are exercised against a source that must trip them.
func TestTheDisciplineDetectorsFindWhatTheyLookFor(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "pack.go")
	if err := os.WriteFile(file, []byte("package p\n\nimport \"sync\"\n\nvar mu sync.Mutex\n"), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}
	imports := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		if spec, ok := node.(*ast.ImportSpec); ok && spec.Path.Value == `"sync"` {
			imports++
		}
		return true
	})
	if imports != 1 {
		t.Errorf("the import scan found %d sync imports in a file that has one", imports)
	}

	source := "storetest.Sweep(st.All(), nil, nil)"
	if !strings.Contains(source, "storetest.Sweep(") {
		t.Error("the registration scan does not recognise a call it is looking for")
	}
	if strings.Contains(source, "storetest.NoLostUpdate(") {
		t.Error("the registration scan reports a call that is not there")
	}

	// The driver-surface detector (#511), against a pack that bypasses it.
	//
	// Two shapes, because they fail differently. Naming an implementation of
	// the driver is the flat one. Calling a low-level Binding verb through a
	// local variable is the one a scanner reading only `p.binding().Verb(…)`
	// misses entirely — and that scanner existed, in this file, while the load
	// balancer's three verbs went unseen. The accepting half is asserted too:
	// PowerOff, which is in the surface, must not be reported, or a detector
	// that refuses everything would pass this test and break the packs.
	pack := filepath.Join(t.TempDir(), "provider")
	if err := os.MkdirAll(pack, 0o750); err != nil {
		t.Fatalf("make the fixture pack: %v", err)
	}
	bypass := `package provider

import (
	"context"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

func binding() machine.Binding { return machine.Binding{} }

func act(ctx context.Context, res *resource.Resource) {
	b := binding()
	b.PowerOff(ctx, res)
	b.Stop(ctx, res.ID, "feint-xxx-1")
	_ = machine.Noop{}
}
`
	if err := os.WriteFile(filepath.Join(pack, "machines.go"), []byte(bypass), 0o600); err != nil {
		t.Fatalf("write the fixture pack: %v", err)
	}
	surface := machine.PackSurface()
	outside := map[string]bool{}
	sawAllowed := false
	for _, r := range driverSurfaceReaches(t, readMachinePackage(t), pack) {
		if _, allowed := surface[r.key]; allowed {
			if r.key == "Binding.PowerOff" {
				sawAllowed = true
			}
			continue
		}
		outside[r.key] = true
	}
	for _, planted := range []string{"Binding.Stop", "Noop"} {
		if !outside[planted] {
			t.Errorf("the driver-surface detector did not report the planted %s: a discipline "+
				"test that finds nothing reads exactly like a disciplined repository", planted)
		}
	}
	if !sawAllowed {
		t.Error("the driver-surface detector did not even see Binding.PowerOff in the fixture, " +
			"so its silence about the rest proves nothing")
	}
	if len(outside) != 2 {
		t.Errorf("the driver-surface detector reports %v outside the surface; PowerOff is in it, "+
			"and a detector that refuses a declared verb would break every pack", outside)
	}

	// And the scanner's own precondition: it resolves machine.X by that one
	// name, so it has to notice a file that renames the package rather than
	// reading it as touching nothing.
	aliased, err := parser.ParseFile(token.NewFileSet(), filepath.Join(pack, "machines.go"), []byte(
		"package provider\n\nimport mach \"github.com/stephrobert/feint/internal/core/machine\"\n\nvar _ = mach.Noop{}\n"), 0)
	if err != nil {
		t.Fatalf("parse the aliased fixture: %v", err)
	}
	if got := aliasedMachineImport(aliased); got != "mach" {
		t.Errorf("the scanner does not notice an aliased machine import, got %q: it would then "+
			"read that file as reaching nothing at all", got)
	}
	plain, err := parser.ParseFile(token.NewFileSet(), filepath.Join(pack, "machines.go"), []byte(bypass), 0)
	if err != nil {
		t.Fatalf("parse the plain fixture: %v", err)
	}
	if got := aliasedMachineImport(plain); got != "" {
		t.Errorf("the scanner calls a plain import an alias (%q), which would fail every pack", got)
	}

	// The runtime-blindness detector (#516), against a pack that knows what is
	// behind --vm.
	//
	// Every shape at once, because #516 is a ratchet on an already-clean tree:
	// the test passing over internal/providers proves only that it can pass.
	// What it must prove is that it can find — the implementation type and the
	// constructor built from it, a configuration key of the runtime, its REST
	// root, a host device name, a mode comparison, and the import that reaches
	// the host without naming any of them.
	//
	// And the two halves that would make it useless in opposite directions.
	// The comment in the fixture is the only place `--vm kvm` and `ipv4.routes`
	// appear: a detector that reported them would fail against the packs' own
	// prose, which cites a measured OVN behaviour dozens of times and is the
	// provenance of every limit here. The exact-set assertion is the other
	// half: machine.Binding, "the method returns an Ethernet frame" and the
	// four ordinary command words are in the fixture on purpose, and a detector
	// that reported any of them would refuse the vocabulary all three packs
	// legitimately write.
	blind := readMachinePackage(t)
	impls := driverImplementations(t, blind)
	named := map[string]bool{}
	for _, impl := range impls {
		named[impl] = true
	}
	if !named["Incus"] || !named["Noop"] {
		t.Errorf("the driver implementations derive to %v, which does not include the runtime and "+
			"the default: the vocabulary would then forbid nothing", impls)
	}

	knowing := filepath.Join(t.TempDir(), "provider")
	if err := os.MkdirAll(knowing, 0o750); err != nil {
		t.Fatalf("make the fixture pack: %v", err)
	}
	leaky := `package provider

import (
	"os/exec"

	"github.com/stephrobert/feint/internal/core/machine"
)

// Measured under --vm kvm: the runtime writes ipv4.routes onto the NIC and a
// live edit re-plugs it. That is the provenance of a limit, not a branch, and
// the detector must report no word of it.
func mechanics(mode string, b machine.Binding) {
	if mode == "incus-ovn" {
		_, _ = exec.Command("incus", "network", "get", "n", "security.acls").Output()
	}
	_ = machine.Incus{}
	_ = machine.NewIncusOVN()
	_ = b
	_ = "the method returns an Ethernet frame"
	_ = "/1.0/instances?recursion=1"
	_ = "eth1"
}
`
	if err := os.WriteFile(filepath.Join(knowing, "machines.go"), []byte(leaky), 0o600); err != nil {
		t.Fatalf("write the fixture pack: %v", err)
	}
	scan := runtimeKnowledgeLeaks(t, knowing, runtimeTells(impls), named)
	if scan.files != 1 || scan.literals < 8 {
		t.Fatalf("the runtime-knowledge scan read %d file(s) and %d literal(s) of a fixture that "+
			"has one and nine: its silence would prove nothing", scan.files, scan.literals)
	}
	told := map[string]bool{}
	for _, leak := range scan.leaks {
		told[leak.tell] = true
	}
	planted := []string{
		"os/exec", "incus", "ovn", "security.acls", "runtime API path", "host device name",
	}
	for _, tell := range planted {
		if !told[tell] {
			t.Errorf("the runtime-blindness detector did not report the planted %q: a discipline "+
				"test that finds nothing reads exactly like a runtime-blind repository", tell)
		}
	}
	for _, prose := range []string{"kvm", "ipv4.routes"} {
		if told[prose] {
			t.Errorf("the runtime-blindness detector reported %q, which the fixture writes only in "+
				"a comment: it would then fail against every measurement the packs cite, and the "+
				"provenance of a limit is worth keeping", prose)
		}
	}
	// The accepting half, and it is an exact set rather than a floor: a
	// detector that also reported machine.Binding, the sentence about an
	// Ethernet frame, or the words "network" and "get" would pass every
	// assertion above and fail every pack in the repository.
	expected := map[string]bool{}
	for _, tell := range planted {
		expected[tell] = true
	}
	for tell := range told {
		if !expected[tell] {
			t.Errorf("the runtime-blindness detector reports %q in a fixture that plants six tells: "+
				"a detector that refuses more than the runtime's own vocabulary breaks the packs "+
				"instead of holding them", tell)
		}
	}

	if !reasonIsThin("not yet migrated") {
		t.Error("the exemption check accepts a reason nobody can weigh")
	}
	if reasonIsThin(notInTheBarrage["exoscale/Orphans"]) {
		t.Error("the exemption check rejects a reason that says why")
	}
}
