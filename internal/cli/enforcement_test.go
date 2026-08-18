package cli

import (
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
// non-test file of its own references machine.Firewaller. Nothing else counts,
// and in particular a comment does not: the marker is the type, which the
// compiler resolves.
//
// This is what fails if a pack starts handing rules over and forgets to say so,
// and equally if a pack says so and hands nothing over. Both directions matter:
// the first understates and the second is the defect #180 was filed for.
func TestEveryPackThatWiresTheFirewallSaysSo(t *testing.T) {
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
		found, err := referencesFirewaller(filepath.Join(packsDir, entry.Name()))
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
		fe, ok := p.(emulator.FirewallEnforcer)
		declares[p.Name()] = ok && fe.EnforcesFirewall()
	}

	for name, actually := range wires {
		declared, mounted := declares[name]
		if !mounted {
			// A directory that mounts no pack is not this test's business.
			continue
		}
		switch {
		case actually && !declared:
			t.Errorf("%s references machine.Firewaller and does not implement "+
				"EnforcesFirewall: /_feint/health understates what it delivers, and a "+
				"user is told not to rely on something that works", name)
		case !actually && declared:
			t.Errorf("%s declares EnforcesFirewall and no non-test file of it "+
				"references machine.Firewaller: this is the claim #180 was filed for, "+
				"a capability published for a pack that hands nothing to the runtime", name)
		}
	}
}

// referencesFirewaller reports whether any non-test Go file directly under dir
// names machine.Firewaller. Non-test on purpose: a fake in a test file proves
// the pack can be tested, never that it wires anything.
func referencesFirewaller(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a path this repository owns
		if err != nil {
			return false, err
		}
		if strings.Contains(string(body), "machine.Firewaller") {
			return true, nil
		}
	}
	return false, nil
}
