package stack_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/stack"
)

// read is one file, read.
func read(t *testing.T, src string) stack.Module {
	t.Helper()
	return stack.Read("testdir", []stack.File{{Name: "main.tf", Source: src}})
}

// A heredoc body does not move the lexer's idea of where strings are.
//
// THE DEFECT THIS PACKAGE WAS WRITTEN FOR, as it stands in the mechanism it
// replaces. internal/cli's stripHCLComments has three states — in a string, in
// a line comment, in a block comment — and no heredoc state at all. So a
// heredoc body holding one unbalanced quote, which a shell script in `user_data`
// supplies without trying, flips its string state. Everything after it is read
// as being inside a string, which means the `#` of a commented-out resource is
// no longer a comment, the resource survives the strip, and the hazard scan
// fires on dead text.
//
// That is the opposite of what TestAStackHazardInACommentStaysSilent promises,
// on an input that test does not cover: its cases are a comment, a comment in a
// string, and a hash inside a list, never a heredoc.
//
// A warning that fires on text nobody will apply is a warning people learn to
// ignore, and teaching that is worse than staying quiet.
func TestAHeredocDoesNotFlipTheScannersStringState(t *testing.T) {
	const src = `
resource "scaleway_instance_server" "web" {
  user_data = {
    cloud-init = <<-EOT
      #!/bin/sh
      echo "it's a shell script, and that apostrophe is one unbalanced quote
      curl https://example.invalid/ > /dev/null
    EOT
  }
}

# resource "scaleway_object_bucket" "parked" { name = "x" }
`
	mod := read(t, src)

	if len(mod.Unreadable) != 0 {
		t.Fatalf("the file was reported unreadable: %+v", mod.Unreadable)
	}
	for _, d := range mod.Declarations {
		if d.Type == "scaleway_object_bucket" {
			t.Errorf("a commented-out resource was read as declared: %s at line %d", d.Address(), d.Line)
		}
	}
	if strings.Contains(mod.Stripped, "scaleway_object_bucket") {
		t.Error("the commented-out resource survived into the stripped text, which is what the " +
			"hazard scan reads; this is the measured false positive")
	}
	// And the accepting half, which matters as much: the real declaration is
	// still there. A lexer that dropped everything after a heredoc would pass
	// the assertions above and report an empty stack.
	if got := mod.Types(stack.ModeResource); len(got) != 1 || got[0] != "scaleway_instance_server" {
		t.Errorf("declared types = %v, want [scaleway_instance_server]", got)
	}
}

// The heredoc ends on a line of its own, not wherever its name appears.
//
// "The template ends when the given identifier subsequently appears again on a
// line of its own" (hclsyntax spec). A substring search closes the document on
// the first body line that merely mentions the word, and everything after it is
// then read as code — which brings back the very state corruption the heredoc
// handling exists to prevent, one line later.
//
// Caught by mutation: with the terminator matched by strings.Contains instead of
// on the trimmed line, this test is the only one that reddens.
func TestAHeredocEndsOnALineOfItsOwn(t *testing.T) {
	const src = `
resource "scaleway_instance_server" "web" {
  user_data = {
    cloud-init = <<-EOT
      # this line mentions EOT in prose and must not close the document
      echo "and this one leaves a quote open
    EOT
  }
}

# resource "scaleway_object_bucket" "parked" { name = "x" }
`
	mod := read(t, src)
	if len(mod.Unreadable) != 0 {
		t.Fatalf("the file was reported unreadable: %+v", mod.Unreadable)
	}
	for _, d := range mod.Declarations {
		if d.Type == "scaleway_object_bucket" {
			t.Errorf("the heredoc closed on a line that only mentions its name, so a commented-out "+
				"resource was read as declared: %s", d.Address())
		}
	}
	if strings.Contains(mod.Stripped, "scaleway_object_bucket") {
		t.Error("the commented-out resource survived into the stripped text")
	}
}

// The heredoc body itself is kept in the stripped text.
//
// Deleting it would be the easy way to make the lexer's state safe, and it would
// silence a true finding to fix a false one: a startup script is exactly where a
// real cloud hostname gets written, and that is what internal/cli's escape scan
// looks for. The body is data, not code — kept for the reader of text, never
// handed to the reader of blocks.
func TestTheStrippedTextKeepsAHeredocBody(t *testing.T) {
	const src = `
resource "scaleway_instance_server" "web" {
  user_data = {
    cloud-init = <<-EOT
      #!/bin/sh
      aws s3 cp x s3://b --endpoint-url https://s3.fr-par.scw.cloud
    EOT
  }
}
`
	mod := read(t, src)
	if !strings.Contains(mod.Stripped, "s3.fr-par.scw.cloud") {
		t.Error("a real cloud host inside a startup script was dropped from the stripped text, so the " +
			"escape scan can no longer see it")
	}
}

// A block's labels are read as exact values, in both the forms the language
// allows.
//
// "Block labels can either be quoted literal strings or naked identifiers"
// (hclsyntax spec), and Terraform 1.16.1 accepts `resource null_resource x {}`
// (measured 2026-11-12). A reader that took only the quoted form would miss a
// declaration and report a stack as smaller than it is.
func TestBothLabelFormsAreRead(t *testing.T) {
	const src = `
resource "scaleway_instance_ip" "quoted" {}
resource null_resource bare {}
data "scaleway_account_project" "p" {}
`
	mod := read(t, src)
	if got := mod.Types(stack.ModeResource); len(got) != 2 ||
		got[0] != "null_resource" || got[1] != "scaleway_instance_ip" {
		t.Errorf("resource types = %v, want [null_resource scaleway_instance_ip]", got)
	}
	if got := mod.Types(stack.ModeData); len(got) != 1 || got[0] != "scaleway_account_project" {
		t.Errorf("data types = %v, want [scaleway_account_project]", got)
	}
	// Resource and data are kept apart because they are not the same claim: one
	// is created and destroyed, the other only read.
	for _, d := range mod.Declarations {
		if d.Type == "scaleway_account_project" && d.Mode != stack.ModeData {
			t.Errorf("a data block was read as mode %q", d.Mode)
		}
	}
}

// `count` marks the instance count as plan-time and leaves the type exact.
//
// This is where #888's own wording is wrong. Reporting the declaration as
// unknown because its count is an expression would hide a known-unsupported
// type behind `count = var.enabled`. Measured: with `count = 0` the provider is
// still required and the resource is still in the plan's `configuration`, so
// the requirement is not conditional even when the instance is.
//
// The repository's own Exoscale example depends on this: its `aws_s3_bucket`
// carries `count = var.sos_endpoint == "" ? 0 : 1`.
func TestCountMarksTheInstanceCountAndNotTheType(t *testing.T) {
	const src = `
resource "aws_s3_bucket" "assets" {
  count = var.sos_endpoint == "" ? 0 : 1
  bucket = "x"
}
resource "scaleway_instance_ip" "each" {
  for_each = toset(var.zones)
}
resource "scaleway_vpc" "plain" {}
`
	mod := read(t, src)
	byType := map[string]stack.Declaration{}
	for _, d := range mod.Declarations {
		byType[d.Type] = d
	}
	for _, want := range []string{"aws_s3_bucket", "scaleway_instance_ip"} {
		d, ok := byType[want]
		if !ok {
			t.Fatalf("%s was not read at all", want)
		}
		if !d.Counted {
			t.Errorf("%s: Counted is false, so a plan-time instance count would be reported as certain", want)
		}
	}
	if byType["scaleway_vpc"].Counted {
		t.Error("a resource with neither count nor for_each was marked as counted, which would report " +
			"every declaration as plan-time and make the field mean nothing")
	}
}

// `provider =` is kept verbatim, and the type's prefix is not used as a
// provider.
//
// Measured 2026-11-12: `scaleway_instance_server` with no required_providers
// resolves to registry.terraform.io/hashicorp/scaleway, and with
// `required_providers { scw = { source = "scaleway/scaleway" } }` the same
// resource resolves to hashicorp/scaleway BESIDE scaleway/scaleway. So the
// prefix decides nothing, and this reader resolves nothing: it reports what is
// written and leaves the rule to the layer that knows the module tree.
func TestTheProviderMetaArgumentIsKeptVerbatim(t *testing.T) {
	const src = `
resource "scaleway_instance_server" "secondary" {
  provider = scaleway.paris
}
`
	mod := read(t, src)
	if len(mod.Declarations) != 1 {
		t.Fatalf("declarations = %d, want 1", len(mod.Declarations))
	}
	if got := mod.Declarations[0].Provider; got != "scaleway.paris" {
		t.Errorf("provider = %q, want %q", got, "scaleway.paris")
	}
}

// required_providers entries are read with their source and version.
func TestRequiredProvidersAreReadWithTheirSource(t *testing.T) {
	const src = `
terraform {
  required_providers {
    scw = {
      source  = "scaleway/scaleway"
      version = "= 2.83.0"
    }
    null = {
      source = "hashicorp/null"
    }
  }
  backend "s3" {
    bucket = "state"
  }
}
`
	mod := read(t, src)
	if len(mod.Required) != 2 {
		t.Fatalf("required providers = %+v, want two entries", mod.Required)
	}
	first := mod.Required[0]
	if first.LocalName != "scw" || first.Source != "scaleway/scaleway" || first.Version != "= 2.83.0" {
		t.Errorf("first entry = %+v", first)
	}
	if mod.Backend == nil || mod.Backend.Type != "s3" {
		t.Errorf("backend = %+v, want type s3", mod.Backend)
	}
}

// A module source that is an expression is reported as not literal.
//
// Not a corner any more: Terraform 1.16.1 evaluates `source = local.src` and a
// `const = true` variable at init, and OpenTofu 1.11.4 evaluates a plain input
// variable (both measured). This reader evaluates none of them, which is why
// the flag exists rather than a guess.
func TestAModuleSourceThatIsAnExpressionIsNotLiteral(t *testing.T) {
	const src = `
module "local" {
  source = "./network"
}
module "computed" {
  source = local.src
}
module "registry" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
}
`
	mod := read(t, src)
	if len(mod.ModuleCalls) != 3 {
		t.Fatalf("module calls = %d, want 3", len(mod.ModuleCalls))
	}
	byName := map[string]stack.ModuleCall{}
	for _, m := range mod.ModuleCalls {
		byName[m.Name] = m
	}
	if m := byName["local"]; !m.SourceLiteral || m.Source != "./network" {
		t.Errorf("local module = %+v, want a literal ./network", m)
	}
	if m := byName["computed"]; m.SourceLiteral {
		t.Errorf("an expression source was reported as literal: %+v", m)
	}
	if m := byName["registry"]; !m.SourceLiteral || m.Source != "terraform-aws-modules/vpc/aws" {
		t.Errorf("registry module = %+v", m)
	}
}

// A `#` inside a string is data, which is the promise the old scanner already
// made and this one keeps.
func TestAHashInsideAStringIsData(t *testing.T) {
	const src = `
resource "scaleway_instance_ip" "local" {
  tags = ["#not-a-comment", "scaleway_object_bucket"]
}
`
	mod := read(t, src)
	if got := mod.Types(stack.ModeResource); len(got) != 1 || got[0] != "scaleway_instance_ip" {
		t.Errorf("declared types = %v, want [scaleway_instance_ip]", got)
	}
	if !strings.Contains(mod.Stripped, "#not-a-comment") {
		t.Error("a hash inside a string was taken for a comment")
	}
}

// A quote inside an interpolation does not end the template.
func TestAQuoteInsideAnInterpolationDoesNotEndTheTemplate(t *testing.T) {
	const src = `
resource "scaleway_instance_server" "web" {
  name = "web-${lookup(var.names, "primary", "fallback")}-1"
  zone = "fr-par-1"
}
resource "scaleway_vpc" "after" {}
`
	mod := read(t, src)
	if got := mod.Types(stack.ModeResource); len(got) != 2 {
		t.Errorf("declared types = %v, want both resources; a nested quote ended the template early", got)
	}
}

// An unterminated construct makes the file unreadable, and unreadable is a
// reported state.
//
// A reader that returned nothing for a file it could not tokenize would be
// indistinguishable from a file that declares nothing, and one of those two is a
// stack whose verdict must be unknown.
func TestAnUnterminatedConstructMakesTheFileUnreadable(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		{"heredoc", "resource \"a\" \"b\" {\n  x = <<-EOT\n  never closed\n}\n"},
		{"string", "resource \"a\" \"b\" {\n  x = \"never closed\n}\n"},
		{"block comment", "/* never closed\nresource \"a\" \"b\" {}\n"},
	} {
		mod := stack.Read("d", []stack.File{{Name: "main.tf", Source: c.src}})
		if len(mod.Unreadable) == 0 {
			t.Errorf("%s: the file was not reported unreadable", c.name)
		}
		if len(mod.Files) != 0 {
			t.Errorf("%s: the file was counted as read: %v", c.name, mod.Files)
		}
		if mod.Stripped != "" {
			t.Errorf("%s: an untokenizable file still contributed text a substring scan would read:\n%s",
				c.name, mod.Stripped)
		}
	}
}

// A type written as a template is reported, not guessed.
//
// The engine refuses this outright — "Template sequences are not allowed in this
// string" — so it cannot come from a configuration anyone can apply. It is here
// because the alternative to reporting it is inventing a type name.
func TestAnInterpolatedTypeIsReportedRatherThanGuessed(t *testing.T) {
	const src = `
resource "${var.t}" "x" {}
`
	mod := read(t, src)
	if len(mod.Unreadable) == 0 {
		t.Fatal("a non-literal block type was accepted silently")
	}
	for _, d := range mod.Declarations {
		if d.Type != "" {
			t.Errorf("a type was invented for a template label: %q", d.Type)
		}
	}
}

// Dynamic block labels are recorded, because they are how a configuration
// generates nested blocks whose number is plan-time.
func TestDynamicBlocksAreRecorded(t *testing.T) {
	const src = `
resource "scaleway_instance_security_group" "sg" {
  dynamic "inbound_rule" {
    for_each = var.rules
    content {
      action = inbound_rule.value.action
    }
  }
}
`
	mod := read(t, src)
	if len(mod.Declarations) != 1 {
		t.Fatalf("declarations = %d, want 1", len(mod.Declarations))
	}
	d := mod.Declarations[0]
	if len(d.Dynamic) != 1 || d.Dynamic[0] != "inbound_rule" {
		t.Errorf("dynamic = %v, want [inbound_rule]", d.Dynamic)
	}
	// The `for_each` inside the dynamic block is not the resource's own, and
	// must not be read as one: a resource with a dynamic block is not a
	// resource with a plan-time instance count.
	if d.Counted {
		t.Error("a for_each inside a dynamic block was read as the resource's own count")
	}
}
