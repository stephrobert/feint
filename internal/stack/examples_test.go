package stack_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/stack"
)

// The reader is pointed at the repository's own three example stacks.
//
// They are the only configurations here that a real Terraform and a real
// OpenTofu apply and destroy every night (tools/conformance/stacks.sh), so they
// are the closest thing to ground truth that costs nothing, and a reader that
// cannot read them reads nothing worth reading.
//
// What is asserted is deliberately not a hand-written list of types: that would
// be a second copy of the stacks, drifting the day somebody edits one. It is
// the properties that must hold for any stack at all, plus the handful of facts
// about these three that a reader getting them wrong would be broken by.
func TestTheReaderReadsTheRepositorysOwnStacks(t *testing.T) {
	for _, provider := range []string{"scaleway", "outscale", "exoscale"} {
		dir := filepath.Join("..", "..", "examples", "stacks", provider)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		var files []stack.File
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a committed example
			if err != nil {
				t.Fatalf("%s/%s: %v", provider, e.Name(), err)
			}
			files = append(files, stack.File{Name: e.Name(), Source: string(body)})
		}
		if len(files) == 0 {
			t.Fatalf("%s: no .tf file found, so this test measures nothing", provider)
		}

		mod := stack.Read(dir, files)

		if len(mod.Unreadable) != 0 {
			t.Errorf("%s: the reader could not finish a file it must read: %+v", provider, mod.Unreadable)
		}
		if len(mod.Declarations) == 0 {
			t.Errorf("%s: no declaration read from %d file(s)", provider, len(files))
		}
		// Every declaration carries an exact type and name. A blank one means a
		// label was not read, and a label is the one thing this reader claims
		// to know exactly.
		for _, d := range mod.Declarations {
			if d.Type == "" || d.Name == "" {
				t.Errorf("%s: a declaration has no type or no name: %+v", provider, d)
			}
			if !strings.HasPrefix(d.Type, provider+"_") &&
				!strings.HasPrefix(d.Type, "aws_") && !strings.HasPrefix(d.Type, "null_") &&
				!strings.HasPrefix(d.Type, "random_") && !strings.HasPrefix(d.Type, "tls_") &&
				!strings.HasPrefix(d.Type, "local_") && !strings.HasPrefix(d.Type, "time_") {
				t.Logf("%s: %s is not a type family this test knows; not a failure, worth a look",
					provider, d.Type)
			}
		}
		// The provider requirement is declared by all three, with a literal
		// source. Reading it is what lets a verdict name the version it was
		// computed for.
		if len(mod.Required) == 0 {
			t.Errorf("%s: no required_providers entry read, so no provider version is known", provider)
		}
		for _, r := range mod.Required {
			if r.LocalName == "" {
				t.Errorf("%s: a required_providers entry has no local name: %+v", provider, r)
			}
		}
		t.Logf("%s: %d file(s), %d declaration(s), resources %v, data %v, providers %d, modules %d",
			provider, len(mod.Files), len(mod.Declarations),
			mod.Types(stack.ModeResource), mod.Types(stack.ModeData),
			len(mod.Required), len(mod.ModuleCalls))
	}
}

// The Exoscale stack is where `count` on a real configuration lives, and it is
// the case #888 would have got wrong.
//
// Its object-storage resource carries `count = var.sos_endpoint == "" ? 0 : 1`,
// so the type is declared and the instance count is decided at plan time. A
// reader that reported the declaration as unknown because of that would hide a
// type behind a conditional; a reader that reported the count as certain would
// promise an object nobody asked for.
func TestARealStackCarriesACountedDeclaration(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "stacks", "exoscale")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%v", err)
	}
	var files []stack.File
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tf") {
			body, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a committed example
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, stack.File{Name: e.Name(), Source: string(body)})
		}
	}
	mod := stack.Read(dir, files)

	counted := 0
	for _, d := range mod.Declarations {
		if d.Counted {
			counted++
			if d.Type == "" {
				t.Errorf("a counted declaration lost its type: %+v", d)
			}
		}
	}
	if counted == 0 {
		t.Fatalf("no counted declaration found in the Exoscale example; if its `count` was removed, "+
			"this test needs another subject rather than deleting. Declarations: %d", len(mod.Declarations))
	}
	t.Logf("%d counted declaration(s) of %d", counted, len(mod.Declarations))
}
