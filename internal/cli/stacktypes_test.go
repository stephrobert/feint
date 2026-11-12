package cli

import (
	"path/filepath"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/stack"
)

// packsForStackTest builds the real server once and hands back its packs, so
// these tests join against what the packs declare today rather than against a
// fixture of what they declared when the test was written.
func packsForStackTest(t *testing.T) []emulator.Pack {
	t.Helper()
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	return srv.Packs()
}

// Every operation a stack table names is one its own pack decided about.
//
// This is the join that makes the tables checkable instead of believable. A
// name this pack neither serves nor declines is either a typo or a row that has
// outlived its subject, and both read the same way in a report: a verdict about
// an operation nobody has.
//
// It also catches the direction nobody thinks about. The day an operation moves
// from Declined() to a route — which is the whole point of this project — the
// row keeps working and silently changes its answer from `incompatible` to
// `served`, with no edit anywhere. That is the behaviour wanted, and it is only
// safe because this test proves the name still joins.
func TestEveryStackOperationIsOneThatPackDecided(t *testing.T) {
	for _, p := range packsForStackTest(t) {
		table, ok := p.(packStackTypes)
		if !ok {
			continue
		}
		known := map[string]bool{}
		for _, r := range p.Routes() {
			known[r.Operation] = true
		}
		for _, d := range p.Declined() {
			known[d.Operation] = true
		}
		for typ, op := range table.StackOperations() {
			if !known[op] {
				t.Errorf("%s: the table maps %s to %q, which this pack neither serves nor declines; "+
					"a report would carry a verdict about an operation nobody has", p.Name(), typ, op)
			}
		}
	}
}

// No two packs claim the same Terraform type.
//
// They cannot today — the prefixes differ — and the guard is here because the
// lookup is a single map, so a collision would silently give one provider's
// answer about another provider's resource. A fourth pack is where that would
// happen, and a fourth pack is exactly when nobody would be looking.
func TestNoTwoPacksClaimTheSameStackType(t *testing.T) {
	owner := map[string]string{}
	for _, p := range packsForStackTest(t) {
		table, ok := p.(packStackTypes)
		if !ok {
			continue
		}
		for typ := range table.StackOperations() {
			if first, taken := owner[typ]; taken {
				t.Errorf("%s and %s both claim %s", first, p.Name(), typ)
			}
			owner[typ] = p.Name()
		}
	}
}

// The measured case: three declarations a pack refuses make the whole
// configuration incompatible, and each refused row carries the pack's own
// reason.
//
// THE MEASUREMENT THIS REPLACES, run on 2026-11-12 against a real emulator with
// provider outscale/outscale 1.8.0. The repository's own Outscale example, plus
// these three resources, applied:
//
//	Plan: 51 to add
//	48 resources created
//	Error: Unable to create Virtual Gateway — 404 OperationNotEmulated,
//	       "feint does not serve CreateVirtualGateway"
//	Error: Unable to create Client Gateway — the same
//	50 entries left in the state
//
// And outscale_vpn_connection was never attempted: its dependencies had failed,
// so an operator who fixed the two visible errors would pay a second full apply
// to discover the third. Staged discovery, each step costing an apply and
// leaving a half-built lab, is what this preflight exists to replace — not an
// unreadable error message, since the emulator's own message names the
// operation and points at /_feint/routes.
func TestThreeRefusedDeclarationsMakeTheConfigurationIncompatible(t *testing.T) {
	const src = `
resource "outscale_net" "main" {
  ip_range = "10.0.0.0/16"
}
resource "outscale_virtual_gateway" "vgw" {
  connection_type = "ipsec.1"
}
resource "outscale_client_gateway" "cgw" {
  bgp_asn         = 65000
  public_ip       = "198.18.0.1"
  connection_type = "ipsec.1"
}
resource "outscale_vpn_connection" "tunnel" {
  connection_type = "ipsec.1"
}
`
	mod := stack.Read("d", []stack.File{{Name: "main.tf", Source: src}})
	rep := stackReport(packsForStackTest(t), mod)

	if rep.Verdict != stack.VerdictIncompatible {
		t.Fatalf("verdict = %q, want incompatible; findings: %+v", rep.Verdict, rep.Findings)
	}

	refused := map[string]stack.Finding{}
	for _, f := range rep.Refused() {
		refused[f.Name] = f
	}
	for _, want := range []string{"outscale_virtual_gateway", "outscale_client_gateway", "outscale_vpn_connection"} {
		f, ok := refused[want]
		if !ok {
			t.Errorf("%s is not among the refused rows, so the message would not name it and the "+
				"operator would discover it on a later apply", want)
			continue
		}
		if f.Reason == "" {
			t.Errorf("%s is refused with no reason: a refusal nobody can act on", want)
		}
		if f.Operation == "" {
			t.Errorf("%s is refused without naming the operation", want)
		}
		if f.Provider != "outscale" {
			t.Errorf("%s was answered by %q", want, f.Provider)
		}
	}
	// The served declaration beside them keeps its standing. A report that
	// turned everything red once anything was refused would tell the operator
	// nothing about what to change.
	for _, f := range rep.Findings {
		if f.Name == "outscale_net" && f.Standing != stack.StandingServed {
			t.Errorf("outscale_net reads as %q, and it is applied every night", f.Standing)
		}
	}
}

// A type nothing knows about is unknown, and unknown is not compatible.
//
// This is the assertion that keeps the preflight honest about its own reach.
// The table is not an inventory of the provider's 57 resource types and must
// not behave like one.
func TestATypeNoTableKnowsIsUnknownAndNotCompatible(t *testing.T) {
	const src = `
resource "outscale_net" "main" {
  ip_range = "10.0.0.0/16"
}
resource "outscale_oks_cluster" "k8s" {
  name = "x"
}
`
	mod := stack.Read("d", []stack.File{{Name: "main.tf", Source: src}})
	rep := stackReport(packsForStackTest(t), mod)

	if rep.Verdict != stack.VerdictUnknown {
		t.Errorf("verdict = %q, want unknown: one declared type has no mapping at all", rep.Verdict)
	}
	for _, f := range rep.Findings {
		if f.Name == "outscale_oks_cluster" && f.Standing != stack.StandingUnknown {
			t.Errorf("an unmapped type reads as %q", f.Standing)
		}
	}
}

// The repository's own Outscale example, loaded with its modules, is
// compatible.
//
// It is applied and destroyed every night by a real Terraform and a real
// OpenTofu, so if this stack cannot reach `compatible`, no stack can. It calls
// ./modules/net twice, and the loader follows both: before it did, this report
// had to answer `unknown` here, which was the honest verdict over an inventory
// that stopped at the root module.
//
// The route from `unknown` to `compatible` is the loader reading two more
// files, never the verdict relaxing. The test that held the old answer is the
// same test, with the subject completed rather than the rule bent.
func TestTheRepositorysOwnOutscaleStackIsCompatible(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "stacks", "outscale")
	mod := stack.Load(dir, stack.EngineTerraform)
	rep := stackReport(packsForStackTest(t), mod)

	// Every type the report did read is served. This is the accepting half: a
	// join that recognised nothing would make every verdict `unknown` and the
	// whole preflight worthless, and it would pass the refusal test above.
	var notServed []stack.Finding
	for _, f := range rep.Findings {
		if f.Standing != stack.StandingServed {
			notServed = append(notServed, f)
		}
	}
	if len(notServed) != 0 {
		t.Errorf("types of the nightly-applied Outscale example that this report does not call served: %+v",
			notServed)
	}

	// Nothing is left unresolved: both calls were followed.
	if len(rep.Unresolved) != 0 {
		t.Errorf("unresolved = %v, and the loader follows local modules", rep.Unresolved)
	}
	if rep.Verdict != stack.VerdictCompatible {
		t.Errorf("verdict = %q on a stack applied every night, want compatible", rep.Verdict)
	}
	// The module's own type is in the inventory. Without it the verdict would
	// be right by luck: the root module alone is also all-served.
	found := false
	for _, f := range rep.Findings {
		if f.Name == "outscale_subnet" {
			found = true
		}
	}
	if !found {
		t.Error("outscale_subnet, declared only inside ./modules/net, is missing from the inventory: " +
			"the verdict covers the root module alone")
	}
}
