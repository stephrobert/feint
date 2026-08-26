package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The declaration is held against the source, not against a list somebody keeps.
//
// #180 was a claim that had drifted from the code: `capabilities.firewall` was
// true for the whole process while one pack of three handed a rule to the
// runtime. Replacing it with a per-pack declaration only moves the drift unless
// something compares the declaration with what the pack actually does — the
// same reasoning that stopped the README's client matrix being a constant.
//
// So the truth is read where it lives: a pack wires the firewall when a
// non-test file of its own references machine.GroupSync — the shared
// orchestration a pack builds to hand its groups to the runtime (#509; the
// marker was machine.Firewaller until that type left the packs' vocabulary
// with the skeleton). Nothing else counts, and in particular a comment does
// not: the marker is the type, which the compiler resolves.
//
// This is what fails if a pack starts handing rules over and forgets to say so,
// and equally if a pack says so and hands nothing over. Both directions matter:
// the first understates and the second is the defect #180 was filed for.
func TestEveryPackThatWiresTheFirewallSaysSo(t *testing.T) {
	declarationMatchesSource(t, "machine.GroupSync", "EnforcesFirewall",
		func(p emulator.Pack) bool {
			fe, ok := p.(emulator.FirewallEnforcer)
			return ok && fe.EnforcesFirewall()
		})
}

// The balancer is the same claim one capability over (#481), and it is held by
// the same instrument because it drifted the same way: `capabilities.balancing`
// was true under OVN while the Scaleway pack handed nothing to the runtime's
// balancing half, so a suite told to key on the capability would have asserted
// a distribution that pack never promised. `enforced.balancing` is the
// per-pack answer, and this test is what keeps it from becoming a second list
// somebody forgets.
//
// The marker was `machine.Balancer` until #511 took the driver's optional
// halves out of the packs' vocabulary — the same re-point #509 forced on the
// firewall's marker. `machine.BalancerSpec` replaces it and says the same
// thing: the spec exists only to be handed over, Binding.EnsureBalancer being
// its one consumer, so a pack that builds one wires the dataplane and a pack
// that does not, does not.
func TestEveryPackThatWiresTheBalancerSaysSo(t *testing.T) {
	declarationMatchesSource(t, "machine.BalancerSpec", "EnforcesBalancing",
		func(p emulator.Pack) bool {
			be, ok := p.(emulator.BalancingEnforcer)
			return ok && be.EnforcesBalancing()
		})
}

// declarationMatchesSource holds one `enforced.*` declaration against the
// source: a pack wires the capability when a non-test file of its own
// references the marker type, and the declaration must agree in both
// directions.
func declarationMatchesSource(t *testing.T, marker, method string, declared func(emulator.Pack) bool) {
	t.Helper()
	// Relative rather than through moduleRoot: that helper lives in package
	// cli_test, and this test needs packsFor, which is unexported. A second copy
	// of the root finder would be one fact in two places.
	packsDir := filepath.Join("..", "providers")

	entries, err := os.ReadDir(packsDir)
	if err != nil {
		t.Fatalf("read the packs directory: %v", err)
	}

	wires := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		found, err := referencesType(filepath.Join(packsDir, entry.Name()), marker)
		if err != nil {
			t.Fatalf("read pack %s: %v", entry.Name(), err)
		}
		wires[entry.Name()] = found
	}
	if len(wires) == 0 {
		t.Fatal("no pack directory was read, so this test would pass while measuring nothing")
	}

	declares := map[string]bool{}
	mounted, err := packsFor(emulator.DefaultEnv())
	if err != nil {
		t.Fatalf("build the packs: %v", err)
	}
	for _, p := range mounted {
		declares[p.Name()] = declared(p)
	}

	for name, actually := range wires {
		declaredHere, mounted := declares[name]
		if !mounted {
			// A directory that mounts no pack is not this test's business.
			continue
		}
		switch {
		case actually && !declaredHere:
			t.Errorf("%s references %s and does not implement "+
				"%s: /_feint/health understates what it delivers, and a "+
				"user is told not to rely on something that works", name, marker, method)
		case !actually && declaredHere:
			t.Errorf("%s declares %s and no non-test file of it "+
				"references %s: this is the claim #180 was filed for, "+
				"a capability published for a pack that hands nothing to the runtime",
				name, method, marker)
		}
	}
}

// referencesType reports whether any non-test Go file directly under dir names
// the marker type — "machine.GroupSync", "machine.BalancerSpec" — in code.
// Non-test on purpose: a fake in a test file proves the pack can be tested,
// never that it wires anything.
//
// The marker is resolved on the AST, exactly as the doc above promises, and not
// by substring: the Exoscale pack discusses `machine.Balancer` in comments to
// say why it does NOT use it, and `machine.Balancer` is a prefix of
// `machine.BalancerSpec`, which the driver's own types share. A substring
// reader would have reported that pack as wiring the dataplane it never
// touches — a false #481 manufactured by the instrument, the exact failure
// measurement-integrity records for an ACL reader filtering on the wrong
// prefix.
func referencesType(dir, marker string) (bool, error) {
	pkg, sel, ok := strings.Cut(marker, ".")
	if !ok {
		return false, fmt.Errorf("marker %q is not of the form package.Type", marker)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if err != nil {
			return false, err
		}
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			expr, isSel := n.(*ast.SelectorExpr)
			if !isSel || expr.Sel.Name != sel {
				return true
			}
			if ident, isIdent := expr.X.(*ast.Ident); isIdent && ident.Name == pkg {
				found = true
			}
			return !found
		})
		if found {
			return true, nil
		}
	}
	return false, nil
}
