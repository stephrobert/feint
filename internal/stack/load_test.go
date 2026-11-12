package stack_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/stack"
)

// write lays a directory of files out for one test.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func typesOf(m stack.Module) string {
	return strings.Join(m.Types(stack.ModeResource), " ")
}

// A module is its own directory's files, and not what sits below them.
//
// Measured 2026-11-12: a resource in sub/ that no `module` block names did not
// appear in the plan. The mechanism this replaces walked recursively, so it
// reported text nobody would apply — the same noise its own top-level gate was
// written against, one level down. An operator told their stack is
// incompatible because of a file the engine never opens learns to ignore the
// tool.
func TestAModuleIsItsOwnDirectoryAndNotWhatIsBelowIt(t *testing.T) {
	dir := write(t, map[string]string{
		"main.tf":        `resource "scaleway_vpc" "here" {}`,
		"sub/nested.tf":  `resource "aws_s3_bucket" "not_part_of_this_module" {}`,
		"modules/net.tf": `resource "aws_instance" "nor_this_one" {}`,
	})

	mod := stack.Load(dir, stack.EngineTerraform)

	if got := typesOf(mod); got != "scaleway_vpc" {
		t.Errorf("types = %q, want only the top-level one; a directory nobody calls is not part of "+
			"this module", got)
	}
}

// OpenTofu reads a .tofu file instead of the .tf of the same name, and
// Terraform does not read .tofu at all.
//
// Both measured 2026-11-12 with OpenTofu 1.11.4 and Terraform 1.16.1. This is
// why a verdict must name the engine it was computed for: the same directory is
// two different configurations.
func TestTheEngineDecidesWhichFilesAreTheModule(t *testing.T) {
	files := map[string]string{
		"main.tf":   `resource "scaleway_vpc" "from_tf" {}`,
		"main.tofu": `resource "exoscale_private_network" "from_tofu" {}`,
		"other.tf":  `resource "outscale_net" "read_by_both" {}`,
	}
	dir := write(t, files)

	tf := stack.Load(dir, stack.EngineTerraform)
	if got := typesOf(tf); got != "outscale_net scaleway_vpc" {
		t.Errorf("terraform reads %q; it must read main.tf and ignore main.tofu entirely", got)
	}

	tofu := stack.Load(dir, stack.EngineOpenTofu)
	if got := typesOf(tofu); got != "exoscale_private_network outscale_net" {
		t.Errorf("opentofu reads %q; main.tofu must shadow main.tf, not add to it", got)
	}
}

// An override file replaces what it names, and is read last.
//
// Measured 2026-11-12: an override.tf replaced a module's source, and
// .terraform/modules/modules.json recorded the override's value, not the
// original's. A reader that ignored overrides would follow the wrong directory
// and report an inventory from files the engine does not use.
func TestAnOverrideFileReplacesAModuleSource(t *testing.T) {
	dir := write(t, map[string]string{
		"main.tf": `
module "net" {
  source = "./a"
}
`,
		"override.tf": `
module "net" {
  source = "./b"
}
`,
		"a/main.tf": `resource "scaleway_vpc" "from_a" {}`,
		"b/main.tf": `resource "outscale_net" "from_b" {}`,
	})

	mod := stack.Load(dir, stack.EngineTerraform)

	if got := typesOf(mod); got != "outscale_net" {
		t.Errorf("types = %q, want only what ./b declares: the override replaced the source", got)
	}
	if len(mod.ModuleCalls) != 1 {
		t.Errorf("module calls = %d, want one; an override must replace a call and not add a second",
			len(mod.ModuleCalls))
	}
}

// A local module is followed, and its declarations are part of the inventory.
func TestALocalModuleIsFollowedAndCounted(t *testing.T) {
	dir := write(t, map[string]string{
		"main.tf": `
resource "scaleway_vpc" "root" {}
module "net" {
  source = "./modules/net"
}
`,
		"modules/net/main.tf": `
resource "scaleway_vpc_private_network" "inner" {}
`,
	})

	mod := stack.Load(dir, stack.EngineTerraform)

	if got := typesOf(mod); got != "scaleway_vpc scaleway_vpc_private_network" {
		t.Errorf("types = %q, want the root's and the module's", got)
	}
	if len(mod.ModuleCalls) != 1 || mod.ModuleCalls[0].ReadFrom == "" {
		t.Errorf("the call is not marked as read: %+v", mod.ModuleCalls)
	}
	// The declaration carries where it came from, so a report can send its
	// reader to the right file.
	for _, d := range mod.Declarations {
		if d.Type == "scaleway_vpc_private_network" && d.Module != "net" {
			t.Errorf("a module's declaration carries module %q, want %q", d.Module, "net")
		}
	}
}

// A module that cannot be read is named, with a reason, and never assumed
// empty.
//
// The two kinds are kept apart because they are different facts: a registry
// source would need an install, an expression source cannot be read at all
// without evaluating it, and this reader evaluates none. Both leave a hole in
// the inventory, and a hole that is not named is a verdict about files nobody
// opened.
func TestAModuleThatCannotBeReadIsNamedWithItsReason(t *testing.T) {
	dir := write(t, map[string]string{
		"main.tf": `
module "registry" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
}
module "computed" {
  source = local.where
}
`,
	})

	mod := stack.Load(dir, stack.EngineTerraform)

	if len(mod.ModuleCalls) != 2 {
		t.Fatalf("module calls = %d, want 2", len(mod.ModuleCalls))
	}
	for _, c := range mod.ModuleCalls {
		if c.ReadFrom != "" {
			t.Errorf("%s was reported as read from %q, and nothing here could read it", c.Name, c.ReadFrom)
		}
		if c.Unread == "" {
			t.Errorf("%s is neither read nor explained", c.Name)
		}
	}
}

// A module an init already installed is read from the engine's own resolution.
//
// .terraform/modules/modules.json is where `terraform get` writes the directory
// it resolved every source to — including the ones this reader cannot resolve,
// since the engine evaluates what it will not. Reading it costs nothing and no
// network, and it is the engine's answer rather than ours.
func TestAnInstalledModuleIsReadFromTheEnginesOwnResolution(t *testing.T) {
	dir := write(t, map[string]string{
		"main.tf": `
module "remote" {
  source = "terraform-aws-modules/vpc/aws"
}
`,
		".terraform/modules/modules.json": `{"Modules":[
  {"Key":"","Source":"","Dir":"."},
  {"Key":"remote","Source":"terraform-aws-modules/vpc/aws","Dir":".terraform/modules/remote"}
]}`,
		".terraform/modules/remote/main.tf": `resource "aws_vpc" "installed" {}`,
	})

	mod := stack.Load(dir, stack.EngineTerraform)

	if got := typesOf(mod); got != "aws_vpc" {
		t.Errorf("types = %q, want the installed module's; the engine had already resolved it", got)
	}
	if len(mod.ModuleCalls) == 0 || mod.ModuleCalls[0].ReadFrom == "" {
		t.Errorf("the installed module is not marked as read: %+v", mod.ModuleCalls)
	}
}

// One directory called twice is read once, and both calls are marked read.
//
// The repository's own Outscale example does this: it calls ./modules/net under
// two names. The inventory is a set of types, so reading it twice would change
// nothing and cost twice; what must not happen is the second call being left
// looking unread, which would make the verdict unknown over a module that was
// read.
func TestADirectoryCalledTwiceIsReadOnceAndBothCallsAreMarked(t *testing.T) {
	dir := write(t, map[string]string{
		"main.tf": `
module "first" {
  source = "./modules/net"
}
module "second" {
  source = "./modules/net"
}
`,
		"modules/net/main.tf": `resource "outscale_net" "one" {}`,
	})

	mod := stack.Load(dir, stack.EngineTerraform)

	if got := typesOf(mod); got != "outscale_net" {
		t.Errorf("types = %q", got)
	}
	for _, c := range mod.ModuleCalls {
		if c.ReadFrom == "" {
			t.Errorf("call %s is left unread although its directory was read: %+v", c.Name, c)
		}
	}
}

// The repository's own Outscale example, loaded with its modules.
//
// It calls ./modules/net twice, and those modules carry declarations the root
// does not. Before the loader this inventory was the root module alone, which
// is why the report had to answer unknown for it.
func TestTheOutscaleExampleIsLoadedWithItsModules(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "stacks", "outscale")
	mod := stack.Load(dir, stack.EngineTerraform)

	if len(mod.Unreadable) != 0 {
		t.Errorf("unreadable: %+v", mod.Unreadable)
	}
	if len(mod.ModuleCalls) == 0 {
		t.Fatal("no module call read from a stack that has two")
	}
	for _, c := range mod.ModuleCalls {
		if c.ReadFrom == "" {
			t.Errorf("module %s was not followed: %s", c.Name, c.Unread)
		}
	}
	fromModules := 0
	for _, d := range mod.Declarations {
		if d.Module != "" {
			fromModules++
		}
	}
	if fromModules == 0 {
		t.Error("no declaration came from a module, so the two calls contributed nothing")
	}
	t.Logf("%d declarations, %d of them from modules; types %v",
		len(mod.Declarations), fromModules, mod.Types(stack.ModeResource))
}
