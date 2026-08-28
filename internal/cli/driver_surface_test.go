package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// reach is one place a pack's own source names something of
// internal/core/machine: a package-level name written machine.X, or a member —
// method or field — read off a value whose type came from there.
type reach struct {
	key   string // "Attachment", or "Binding.PowerOff"
	pack  string
	where string // "internal/providers/scaleway/servers.go:445"
	how   string
}

// machinePackage is what internal/core/machine offers, read from its own
// source: the denominator the surface is judged against.
type machinePackage struct {
	// names are the exported package-level names, mapped to their kind.
	names map[string]string
	// members are the exported methods and fields of exported types, keyed
	// "Type.Member".
	members map[string]bool
	// yields maps a member to the machine type it produces, so a value passed
	// from one member to the next stays resolvable: the delivery a balancer
	// hand-off returns is a machine value too, and a scanner that lost it
	// there would stop watching at the first hop.
	yields map[string]string
	// results maps a member to every type it returns, positionally, for the
	// `a, b := x.M()` form.
	results map[string][]string
	// hidden maps an *unexported* interface of the package to its method
	// names. Since #514 that is where the driver and its five pack-facing
	// halves live, so two things need reading out of them: the method set the
	// runtime-blindness derivation compares implementations against, and the
	// fact that they are unexported at all — which is what
	// TestThePacksCannotNameTheDriver's companion asserts here rather than
	// leaving to a build that would simply stop failing.
	hidden map[string][]string
}

// readMachinePackage parses internal/core/machine and reports what it exports.
//
// Derived, never listed by hand, and the reason is written in #511's own
// history: three successive counts of this same surface were wrong because
// each started from a list of verbs somebody remembered — test files left in
// the population, then Attach and Detach missing from the list, then the
// interface assertions missing altogether. A grep that does not enumerate what
// it ignores cannot say what it missed. So the denominator is the package's
// own declarations, and the surface below is checked against them: a name that
// stops existing, or is renamed, fails here rather than quietly stopping to
// mean anything.
func readMachinePackage(t *testing.T) machinePackage {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "core", "machine")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("list the machine package: %v", err)
	}
	pkg := machinePackage{
		names:   map[string]string{},
		members: map[string]bool{},
		yields:  map[string]string{},
		results: map[string][]string{},
		hidden:  map[string][]string{},
	}
	parsed := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		parsed++
		for _, decl := range tree.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				readMachineFunc(pkg, d)
			case *ast.GenDecl:
				readMachineGenDecl(pkg, d)
			}
		}
	}
	if parsed < 10 || len(pkg.names) < 40 {
		t.Fatalf("read %d files and %d exported names from the machine package: the reader is "+
			"broken, and a broken reader makes every surface entry look stale", parsed, len(pkg.names))
	}
	return pkg
}

func readMachineFunc(pkg machinePackage, d *ast.FuncDecl) {
	if !d.Name.IsExported() {
		return
	}
	if d.Recv == nil {
		pkg.names[d.Name.Name] = "func"
		pkg.results[d.Name.Name] = machineResults(d.Type)
		if types := pkg.results[d.Name.Name]; len(types) > 0 {
			pkg.yields[d.Name.Name] = types[0]
		}
		return
	}
	recv := receiverName(d.Recv.List[0].Type)
	if recv == "" || !ast.IsExported(recv) {
		return
	}
	key := recv + "." + d.Name.Name
	pkg.members[key] = true
	pkg.results[key] = machineResults(d.Type)
	if types := pkg.results[key]; len(types) > 0 {
		pkg.yields[key] = types[0]
	}
}

func readMachineGenDecl(pkg machinePackage, d *ast.GenDecl) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				if t, ok := s.Type.(*ast.InterfaceType); ok {
					for _, m := range t.Methods.List {
						for _, name := range m.Names {
							pkg.hidden[s.Name.Name] = append(pkg.hidden[s.Name.Name], name.Name)
						}
					}
				}
				continue
			}
			pkg.names[s.Name.Name] = "type"
			switch t := s.Type.(type) {
			case *ast.InterfaceType:
				for _, m := range t.Methods.List {
					for _, name := range m.Names {
						key := s.Name.Name + "." + name.Name
						pkg.members[key] = true
						if fn, ok := m.Type.(*ast.FuncType); ok {
							pkg.results[key] = machineResults(fn)
							if types := pkg.results[key]; len(types) > 0 {
								pkg.yields[key] = types[0]
							}
						}
					}
				}
			case *ast.StructType:
				for _, f := range t.Fields.List {
					for _, name := range f.Names {
						if !name.IsExported() {
							continue
						}
						key := s.Name.Name + "." + name.Name
						pkg.members[key] = true
						if local, ok := localType(f.Type); ok {
							pkg.yields[key] = local
						}
					}
				}
			}
		case *ast.ValueSpec:
			kind := "var"
			if d.Tok == token.CONST {
				kind = "const"
			}
			for _, name := range s.Names {
				if name.IsExported() {
					pkg.names[name.Name] = kind
				}
			}
		}
	}
}

func receiverName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// machineResults names, positionally, the machine types a signature returns;
// "" where a result is something else.
func machineResults(fn *ast.FuncType) []string {
	if fn.Results == nil {
		return nil
	}
	var out []string
	for _, f := range fn.Results.List {
		name, _ := localType(f.Type)
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			out = append(out, name)
		}
	}
	return out
}

// localType names the machine type an expression written *inside* the machine
// package denotes: "Binding", "[]Attachment", or "" for anything else.
func localType(e ast.Expr) (string, bool) {
	switch t := e.(type) {
	case *ast.StarExpr:
		return localType(t.X)
	case *ast.ArrayType:
		if inner, ok := localType(t.Elt); ok {
			return "[]" + inner, true
		}
	case *ast.Ident:
		if ast.IsExported(t.Name) {
			return t.Name, true
		}
	}
	return "", false
}

// qualifiedType names the machine type an expression written in a *pack*
// denotes: machine.Binding, []machine.Attachment.
func qualifiedType(e ast.Expr) (string, bool) {
	switch t := e.(type) {
	case *ast.StarExpr:
		return qualifiedType(t.X)
	case *ast.ArrayType:
		if inner, ok := qualifiedType(t.Elt); ok {
			return "[]" + inner, true
		}
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok && id.Name == machinePackageName {
			return t.Sel.Name, true
		}
	}
	return "", false
}

// machinePackageName is the identifier the packs import internal/core/machine
// under. Every scan below resolves `machine.X` by this name, so a file that
// imported the package as something else would be read as touching nothing —
// a silent empty scan, which is the exact shape of failure this file exists to
// refuse. refuseAnAliasedImport asks the question rather than the comment
// asserting the answer.
const machinePackageName = "machine"

const machinePackagePath = `"github.com/stephrobert/feint/internal/core/machine"`

// aliasedMachineImport names the alias a file gives the machine package, and is
// empty when the file imports it plainly or not at all.
//
// Widening the scanner to follow an alias would be the other answer, and it is
// the wrong one here: the alias would have to be threaded through every
// resolution below, and a scanner that silently coped would let the next
// reader believe the constant above is a convention rather than a fact. The
// caller fails on the file that broke it instead — and this is a value rather
// than a t.Fatalf so the detectors' own positive control can exercise it.
func aliasedMachineImport(tree *ast.File) string {
	for _, spec := range tree.Imports {
		if spec.Path.Value != machinePackagePath {
			continue
		}
		if spec.Name != nil && spec.Name.Name != machinePackageName {
			return spec.Name.Name
		}
	}
	return ""
}

// surfaceScanner walks one pack and reports every reach into the machine
// package.
type surfaceScanner struct {
	pkg     machinePackage
	pack    string
	root    string
	results map[string][]string
	fields  map[string]string // pack struct field name -> machine type
	locals  map[string]string
	fset    *token.FileSet
	rel     string
	out     []reach
}

// driverSurfaceReaches reports every reach into the machine package made by
// the non-test sources of one pack directory.
//
// Non-test on purpose, and the reason is the measurement that opened #511: the
// first count of this surface was taken with `grep -rh … | grep -v _test`,
// which with -h has no filename to filter on and therefore dropped *lines*
// containing "_test" while keeping every test file in the population. Test
// files legitimately build fake drivers — that is how a pack is tested at all
// — so counting them measures the harness. Here the exclusion is by filename,
// on the file being opened.
func driverSurfaceReaches(t *testing.T, pkg machinePackage, dir string) []reach {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("list %s: %v", dir, err)
	}
	sources := make([]string, 0, len(files))
	for _, file := range files {
		if !strings.HasSuffix(file, "_test.go") {
			sources = append(sources, file)
		}
	}

	s := &surfaceScanner{
		pkg:     pkg,
		pack:    filepath.Base(dir),
		root:    repoRoot(t),
		results: map[string][]string{},
		fields:  map[string]string{},
	}

	// Two passes: what this pack's own functions and structs carry has to be
	// known before any body is read, because p.binding() is declared in one
	// file and called from a dozen others. Resolving per file was the first
	// version of this scanner and it lost every call outside machines.go.
	trees := make([]*ast.File, 0, len(sources))
	fsets := make([]*token.FileSet, 0, len(sources))
	for _, file := range sources {
		fset := token.NewFileSet()
		tree, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		if alias := aliasedMachineImport(tree); alias != "" {
			t.Fatalf("%s imports the machine package as %q: every scan below resolves machine.X "+
				"by the name %q, so this file would be read as reaching nothing at all. Drop the "+
				"alias, or teach the scanner to carry one per file", file, alias, machinePackageName)
		}
		s.declare(tree)
		trees = append(trees, tree)
		fsets = append(fsets, fset)
	}
	for i, tree := range trees {
		s.fset = fsets[i]
		rel, err := filepath.Rel(s.root, sources[i])
		if err != nil {
			rel = sources[i]
		}
		s.rel = rel
		s.scan(tree)
	}
	return s.out
}

// declare records what this pack's own declarations carry of the machine
// package: a function returning one, a struct field holding one.
func (s *surfaceScanner) declare(tree *ast.File) {
	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			results := packResults(d.Type)
			if len(results) == 0 {
				continue
			}
			s.results[d.Name.Name] = results
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, f := range st.Fields.List {
					name, ok := qualifiedType(f.Type)
					if !ok {
						continue
					}
					for _, ident := range f.Names {
						s.fields[ident.Name] = name
					}
				}
			}
		}
	}
}

func packResults(fn *ast.FuncType) []string {
	if fn.Results == nil {
		return nil
	}
	var out []string
	machine := false
	for _, f := range fn.Results.List {
		name, _ := qualifiedType(f.Type)
		if name != "" {
			machine = true
		}
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			out = append(out, name)
		}
	}
	if !machine {
		return nil
	}
	return out
}

func (s *surfaceScanner) scan(tree *ast.File) {
	for _, decl := range tree.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			s.walk(decl)
			continue
		}
		s.locals = map[string]string{}
		s.bindParams(fn.Type)
		if fn.Recv != nil {
			s.bindFields(fn.Recv)
		}
		s.walk(fn)
	}
}

func (s *surfaceScanner) bindParams(fn *ast.FuncType) {
	if fn.Params == nil {
		return
	}
	for _, f := range fn.Params.List {
		name, ok := qualifiedType(f.Type)
		if !ok {
			continue
		}
		for _, ident := range f.Names {
			s.locals[ident.Name] = name
		}
	}
}

func (s *surfaceScanner) bindFields(recv *ast.FieldList) {
	for _, f := range recv.List {
		name, ok := qualifiedType(f.Type)
		if !ok {
			continue
		}
		for _, ident := range f.Names {
			s.locals[ident.Name] = name
		}
	}
}

// walk reads a declaration, recording assignments as it meets them so a value
// stays followed across statements: `b := p.binding()` then `b.Stop(…)` is a
// reach, and a scanner that only understood `p.binding().Stop(…)` would report
// a clean pack for a bypass one local variable deep. That variant was measured
// on this very repository while the scanner was being written — the load
// balancer's three verbs were invisible until this arrived.
func (s *surfaceScanner) walk(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			s.record(stmt)
		case *ast.DeclStmt:
			if decl, ok := stmt.Decl.(*ast.GenDecl); ok {
				for _, spec := range decl.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					s.recordValueSpec(vs)
				}
			}
		case *ast.RangeStmt:
			if elem, ok := strings.CutPrefix(s.typeOf(stmt.X), "[]"); ok && elem != "" {
				if ident, ok := stmt.Value.(*ast.Ident); ok {
					s.locals[ident.Name] = elem
				}
			}
		case *ast.SelectorExpr:
			s.reachSelector(stmt)
		}
		return true
	})
}

func (s *surfaceScanner) record(stmt *ast.AssignStmt) {
	if len(stmt.Rhs) == len(stmt.Lhs) {
		for i, lhs := range stmt.Lhs {
			if ident, ok := lhs.(*ast.Ident); ok {
				if typ := s.typeOf(stmt.Rhs[i]); typ != "" {
					s.locals[ident.Name] = typ
				}
			}
		}
		return
	}
	if len(stmt.Rhs) != 1 {
		return
	}
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			continue
		}
		if typ := s.resultAt(stmt.Rhs[0], i); typ != "" {
			s.locals[ident.Name] = typ
		}
	}
}

func (s *surfaceScanner) recordValueSpec(vs *ast.ValueSpec) {
	if typ, ok := qualifiedType(vs.Type); ok {
		for _, ident := range vs.Names {
			s.locals[ident.Name] = typ
		}
		return
	}
	if len(vs.Values) != len(vs.Names) {
		return
	}
	for i, ident := range vs.Names {
		if typ := s.typeOf(vs.Values[i]); typ != "" {
			s.locals[ident.Name] = typ
		}
	}
}

// typeOf names the machine type an expression in pack code evaluates to, "" for
// anything else.
func (s *surfaceScanner) typeOf(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.ParenExpr:
		return s.typeOf(t.X)
	case *ast.UnaryExpr:
		return s.typeOf(t.X)
	case *ast.StarExpr:
		return s.typeOf(t.X)
	case *ast.CompositeLit:
		if name, ok := qualifiedType(t.Type); ok {
			return name
		}
	case *ast.TypeAssertExpr:
		if t.Type != nil {
			if name, ok := qualifiedType(t.Type); ok {
				return name
			}
		}
	case *ast.IndexExpr:
		if elem, ok := strings.CutPrefix(s.typeOf(t.X), "[]"); ok {
			return elem
		}
	case *ast.CallExpr:
		return s.resultAt(t, 0)
	case *ast.SelectorExpr:
		return s.selectorType(t)
	case *ast.Ident:
		return s.locals[t.Name]
	}
	return ""
}

func (s *surfaceScanner) selectorType(t *ast.SelectorExpr) string {
	if id, ok := t.X.(*ast.Ident); ok && id.Name == machinePackageName {
		// machine.X used as a value: a var or a const of the package.
		return s.pkg.yields[t.Sel.Name]
	}
	if owner := s.typeOf(t.X); owner != "" {
		return s.pkg.yields[owner+"."+t.Sel.Name]
	}
	return s.fields[t.Sel.Name]
}

// resultAt names the machine type the i-th result of a call evaluates to.
func (s *surfaceScanner) resultAt(e ast.Expr, i int) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	at := func(results []string) string {
		if i < len(results) {
			return results[i]
		}
		return ""
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return at(s.results[fn.Name])
	case *ast.SelectorExpr:
		if id, ok := fn.X.(*ast.Ident); ok && id.Name == machinePackageName {
			return at(s.pkg.results[fn.Sel.Name])
		}
		if owner := s.typeOf(fn.X); owner != "" {
			return at(s.pkg.results[owner+"."+fn.Sel.Name])
		}
		return at(s.results[fn.Sel.Name])
	}
	return ""
}

// reachSelector records the reach a selector expression is, if it is one.
func (s *surfaceScanner) reachSelector(sel *ast.SelectorExpr) {
	if id, ok := sel.X.(*ast.Ident); ok && id.Name == machinePackageName {
		s.add(sel.Sel.Name, sel, "machine."+sel.Sel.Name)
		return
	}
	owner := s.typeOf(sel.X)
	if owner == "" || strings.HasPrefix(owner, "[]") {
		return
	}
	s.add(owner+"."+sel.Sel.Name, sel, "a value of type machine."+owner)
}

func (s *surfaceScanner) add(key string, node ast.Node, how string) {
	s.out = append(s.out, reach{
		key:   key,
		pack:  s.pack,
		where: fmt.Sprintf("%s:%d", s.rel, s.fset.Position(node.Pos()).Line),
		how:   how,
	})
}

// notInTheDriverSurface lists the reaches a pack keeps outside the declared
// surface, each with the reason — the discipline Declined() applies to a
// refusal, applied to an internal boundary, and the shape
// TestEveryBarrageExemptionSaysWhy already holds for the two maps beside it.
//
// Keyed "<pack>/<key>" so an exemption cannot quietly widen to a whole pack,
// and empty, which is the honest state after this lot: every site the packs
// held on 0f8b58e either went through a service or gained one. "Not yet
// migrated" and "cannot be" are different reasons and would be written
// differently here; there is neither today.
var notInTheDriverSurface = map[string]string{}

// mustNotBeNameable names what internal/core/machine must not export at all.
//
// This list is the inversion of the one below, and the inversion is the point
// of #514. Everything here used to be exported and excluded from PackSurface,
// which held it by an AST scan over the packs' sources — a convention. On
// 154c204 a pack could still write `var _ machine.Driver` and
// `go build ./internal/providers/scaleway/` exited 0, measured. Since #514 the
// six are unexported, so the sentence fails the build instead, and what this
// list holds is the *return*: re-exporting any of them, under any spelling,
// reopens the door in one edit and nothing else in the repository would say
// so. internal/core/machine's own package documentation (runtime.go) carries
// the reasoning; TestThePacksCannotNameTheDriver compiles the sentence and
// requires the failure.
//
// Each entry must still exist as an unexported interface of the package, so a
// deletion or a rename is a failure too: an exclusion naming nothing
// constrains nothing, which is exactly what the sibling list below asserts the
// other way round.
var mustNotBeNameable = []string{
	// The runtime itself. A pack holding one calls Start, Remove or
	// RemoveNetwork past Binding.ours and past the driver's mustOwn — the hole
	// a crafted snapshot walked through.
	"driver",
	// Its five pack-facing halves. Reaching one by assertion bypasses the
	// shared layer exactly as surely as calling a method does, which is the
	// correction that took #511's count from eleven sites to twenty-nine.
	"router", "firewaller", "peerer", "isolator", "balancer",
}

// mustStayOutside names what the declared surface may never admit.
//
// It is the answer to the trap #511 names first: a surface that authorises
// everything a pack reaches today documents the state of affairs instead of
// constraining it. So what is excluded is asserted, not merely absent — the
// driver's implementations, its operator-facing halves, its argument
// vocabulary, and the low-level Binding verbs the two orchestrators exist to
// sequence. Every entry below is exported by internal/core/machine and
// therefore nameable; none may appear in PackSurface.
//
// The driver interface and its five pack-facing halves left this list for
// mustNotBeNameable above, and that is a promotion rather than a removal: a
// name the compiler refuses needs no scan behind it, and a name the scan still
// has to catch is a name somebody can still write.
var mustStayOutside = []string{
	// Every implementation of the runtime. These stay exported — Noop is the
	// metadata-only default the emulator and forty tests build on, Recorder is
	// the shared contract recorder of #515, Incus is the runtime itself — so
	// the scan is what keeps them out of a pack, and the residue is written
	// down rather than implied: `machine.Noop{}.Remove(ctx, name)` still
	// compiles in a pack, and is caught here rather than by the build.
	"Noop", "Incus", "Recorder",
	// The operator-facing halves. internal/cli reaches these through
	// machine.Runtime's own methods, so no pack needs them; they are excluded
	// by the scan rather than by the compiler because `feint clean`, `feint
	// doctor` and `feint images` are not packs and the handle answers for
	// them.
	"Capable", "Waiter",
	"ImageBuilder", "ImageLister", "Pruner", "Repairer", "Surveyor", "Watcher",
	"PlumbingReleaser",
	// The handle itself, and its door. It is the emulator's and the CLI's
	// spelling of a runtime; a pack that names it has gone looking for the
	// value #511 took out of its reach.
	"Runtime", "Use",
	// The driver's own argument vocabulary: what the shared layer builds from
	// what a pack declares. A pack assembling one of these is a pack writing
	// the call the layer exists to write.
	"Spec", "NetworkSpec", "AddressSpec", "FirewallBinding", "Machine",
	// Host object names and the capability half, which belong to the driver
	// and to /_feint/health respectively.
	"NetworkName", "NetworkPrefix", "LabelKey", "MaxNetworkNameLen",
	"Capabilities", "CapabilitiesOf", "Declared",
	// The package-level isolation pass takes a Driver, which is the value no
	// pack may hold; Binding.ReconcileIsolation is the door.
	"ReconcileIsolation",
	// Binding's low-level half. PowerOn is here and Reconciler.PowerOn is in
	// the surface on purpose: the plan's order — addresses, memberships,
	// firewall last — is a property of the runtime, and a pack that starts a
	// machine through the binding skips it.
	"Binding.Start", "Binding.Stop", "Binding.Remove", "Binding.Address",
	"Binding.Name", "Binding.PowerOn", "Binding.Refresh", "Binding.ForgetPlacements",
	// The unkinded address reader (#541). It was in the surface until an
	// Exoscale instance with no public IP published its private-network
	// address as `public-ip`: the binding records whatever the runtime
	// answered and says nothing about what kind of address it is, so every
	// pack republishing it under a field whose name asserts one was asserting
	// what nobody had checked. Reconciler.PublicAddressOf and
	// PrivateAddressOf are the doors, and this line is what stops the old one
	// from being quietly reopened.
	"Binding.AddressOf",
	"Binding.RouteAddress", "Binding.UnrouteAddress",
	"Binding.SyncRuleSet", "Binding.ApplyRuleSets", "Binding.DropRuleSet",
	"Binding.WithRuntime",
	// The firewall step of the boot replay. The Reconciler runs it, last, and
	// a pack running it itself puts the expansion before the interfaces it is
	// supposed to see.
	"GroupSync.AfterBoot",
}

// The packs reach nothing of internal/core/machine that the contract does not
// name (#511).
//
// The measurement that opened the issue was that three packs reached
// twenty-nine sites of that package and nothing said which were legitimate.
// The corridor was opened first — GroupSync (#509), Plan and Reconciler
// (#510), the shared recorder (#515) — so the remaining question was only
// which door each gesture goes through. This is the door, and machine.
// PackSurface is the list of what is behind it.
//
// It reports the pack, the gesture and the line, because a discipline failure
// that only says "a pack reached too far" is a failure somebody has to
// reproduce before they can act on it.
//
// What this holds that the compiler does not: emulator.Env no longer carries a
// Driver and machine.Binding's driver field is unexported, so a pack cannot
// obtain a driver value at all — that half is a build error since this lot.
// What stays for this test is everything reachable *without* a driver value:
// constructing machine.Noop, naming an optional half in a signature, calling a
// low-level Binding verb the orchestrators exist to sequence. Typing cannot
// see those, and they are how the boundary would come back.
func TestNoPackReachesPastTheDeclaredDriverSurface(t *testing.T) {
	pkg := readMachinePackage(t)
	surface := machine.PackSurface()

	var offences []string
	total := 0
	// The fourth pack is in the population, not beside it (#517). The three
	// real packs were migrated onto this boundary, so each of them knows what
	// it used to do by hand; testdata/provider-four never did, and it is the
	// only member of this scan that can answer whether the declared surface
	// suffices rather than whether three authors remembered it. It carries no
	// exemption of its own, which is the whole claim.
	for _, dir := range disciplinedPackDirs(t) {
		reaches := driverSurfaceReaches(t, pkg, dir)
		total += len(reaches)
		for _, r := range reaches {
			if _, allowed := surface[r.key]; allowed {
				continue
			}
			if reason := notInTheDriverSurface[r.pack+"/"+r.key]; reason != "" {
				continue
			}
			offences = append(offences, fmt.Sprintf("%s reaches %s at %s (%s)",
				r.pack, r.key, r.where, r.how))
		}
	}

	// A scan that found nothing looks exactly like a repository where no pack
	// touches the runtime, and that repository does not exist: three packs
	// drive machines, networks, addresses, rule sets and a balancer. The floor
	// is well under today's count and well over zero.
	if total < 60 {
		t.Fatalf("only %d reaches into internal/core/machine found across the packs: the scan is "+
			"broken, and a broken scan reports a disciplined repository", total)
	}

	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("%d reach(es) into internal/core/machine that machine.PackSurface does not name. "+
			"A gesture the driver does not expose is a service the driver gains, never a call a "+
			"pack makes around it; if one genuinely cannot go through the layer, add "+
			"\"<pack>/<key>\" to notInTheDriverSurface with the reason:\n  %s",
			len(offences), strings.Join(offences, "\n  "))
	}
}

// The declared surface says something, and still means what it says.
//
// Three halves, and each answers one way the list could become decoration. It
// must resolve: every entry names something internal/core/machine really
// exports, so a rename makes this fail instead of silently emptying the
// contract. It must exclude: mustStayOutside is asserted absent, because a
// list that admits everything a pack reaches today is an inventory, not a
// boundary — #511 names that trap first and this is what answers it. And the
// six names of mustNotBeNameable must not come back as exported names at all,
// which is #514's half: the compiler holds them today, and a single edit
// re-exporting one would put them back behind a scan without anything saying
// so.
func TestTheDeclaredDriverSurfaceIsSmallerThanThePackage(t *testing.T) {
	pkg := readMachinePackage(t)
	surface := machine.PackSurface()

	for _, key := range mustNotBeNameable {
		exported := strings.ToUpper(key[:1]) + key[1:]
		if kind, back := pkg.names[exported]; back {
			t.Errorf("internal/core/machine exports %s again (as a %s): #514 unexported it so that "+
				"`var _ machine.%s` in a pack fails the build, and an exported spelling puts the "+
				"boundary back behind a scan somebody can widen", exported, kind, exported)
		}
		if len(pkg.hidden[key]) == 0 {
			t.Errorf("internal/core/machine has no unexported interface %q any more: an exclusion "+
				"naming nothing constrains nothing, and the driver contract cannot have lost its "+
				"verbs", key)
		}
	}

	for key, why := range surface {
		if len(strings.Fields(why)) < 4 {
			t.Errorf("the surface entry for %s says %q, which does not say what a pack asks it for", key, why)
		}
		typ, member, qualified := strings.Cut(key, ".")
		if !qualified {
			if _, ok := pkg.names[key]; !ok {
				t.Errorf("PackSurface admits %q, which internal/core/machine does not export: "+
					"a contract naming something gone constrains nothing", key)
			}
			continue
		}
		if _, ok := pkg.names[typ]; !ok {
			t.Errorf("PackSurface admits %q, whose type is not exported by internal/core/machine", key)
			continue
		}
		if !pkg.members[key] {
			t.Errorf("PackSurface admits %q, which is not a method or a field of machine.%s", member, typ)
		}
	}

	for _, key := range mustStayOutside {
		if _, admitted := surface[key]; admitted {
			t.Errorf("PackSurface admits %s, which the contract excludes: a surface that authorises "+
				"everything the packs reach guarantees nothing, and this is one of the names the "+
				"boundary exists to keep out", key)
		}
		typ, _, qualified := strings.Cut(key, ".")
		if !qualified {
			if _, ok := pkg.names[key]; !ok {
				t.Errorf("mustStayOutside names %q, which internal/core/machine no longer exports: "+
					"an exclusion nobody can violate is not an exclusion", key)
			}
			continue
		}
		if !pkg.members[key] {
			t.Errorf("mustStayOutside names %q, which machine.%s no longer has: same reason", key, typ)
		}
	}
}
