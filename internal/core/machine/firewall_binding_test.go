package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// fakeFirewaller records what the shared layer hands the runtime, so a test
// asserts on the argument rather than on a green return (#475: the defect was
// precisely that nothing was handed over while everything answered 200).
type fakeFirewaller struct {
	ensured  []FirewallSpec
	applied  []FirewallBinding
	removed  []string
	applyErr error
}

func (f *fakeFirewaller) EnsureFirewall(_ context.Context, spec FirewallSpec) error {
	f.ensured = append(f.ensured, spec)
	return nil
}

func (f *fakeFirewaller) ApplyFirewall(_ context.Context, _ string, binding FirewallBinding) error {
	f.applied = append(f.applied, binding)
	return f.applyErr
}

func (f *fakeFirewaller) RemoveFirewall(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func firewallBinding(buf *bytes.Buffer) Binding {
	return Binding{
		Provider: "test",
		Prefix:   "feint-test-",
		Log:      slog.New(slog.NewTextHandler(buf, nil)),
	}
}

// TestAPermissiveGroupBindsNothingOnEveryRuntime holds the unconditional half
// of the shared attach (#337, moved here by #475). The empty binding used to
// be OVN's alone, for the pipeline-priority reason EnforcesNothing keeps; but
// a default group — pure accept, filtering nothing upstream — rides every
// server create, including onto a machine whose only interface is a routed
// NIC that accepts no rule set at all. On the bridge runtime that attach was
// an ERROR log the control plane answered over as if the group were enforced.
// The only faithful translation of "filters nothing" is absence, on every
// runtime and now for every pack.
func TestAPermissiveGroupBindsNothingOnEveryRuntime(t *testing.T) {
	permissive := FirewallSpec{
		Name:           "sg-permissive",
		DefaultIngress: "allow",
		DefaultEgress:  "allow",
		Rules: []FirewallRule{
			{Direction: "ingress", Action: "allow"},
			{Direction: "egress", Action: "allow"},
		},
	}
	restrictive := permissive
	restrictive.Name = "sg-restrictive"
	restrictive.DefaultIngress = "drop"

	var buf bytes.Buffer
	fw := &fakeFirewaller{}
	b := firewallBinding(&buf)

	b.ApplyRuleSets(context.Background(), fw, "feint-test-a", permissive)
	if len(fw.applied) != 1 || len(fw.applied[0].Names) != 0 {
		t.Fatalf("a group that filters nothing must attach nothing, got %+v", fw.applied)
	}

	// The accepting half: a group that restricts something still attaches, and
	// its defaults ride the binding.
	fw.applied = nil
	b.ApplyRuleSets(context.Background(), fw, "feint-test-a", restrictive)
	if len(fw.applied) != 1 {
		t.Fatalf("%d applications, want 1", len(fw.applied))
	}
	got := fw.applied[0]
	if len(got.Names) != 1 || got.Names[0] != "sg-restrictive" || got.DefaultIngress != "drop" {
		t.Fatalf("a restricting group must still bind with its defaults, got %+v", got)
	}

	// And stacked sets combine deny-dominant: one restricting neighbour keeps
	// the default closed whatever a permissive one says, while the permissive
	// one still attaches nothing.
	fw.applied = nil
	b.ApplyRuleSets(context.Background(), fw, "feint-test-a", permissive, restrictive)
	got = fw.applied[0]
	if len(got.Names) != 1 || got.DefaultIngress != "drop" {
		t.Fatalf("stacked sets must combine deny-dominant, got %+v", got)
	}
}

// TestAnUnenforceableGroupIsReportedAsTheDeclaredLimit separates the two
// meanings one ERROR line used to conflate (#337, moved here by #475). A rule
// set the runtime has no mechanism for — ErrFirewallUnenforceable, the routed
// NIC — is a declared limit, published as
// capabilities.firewall_public_only=false, and is reported as a warning that
// names the declaration. A failure nothing declared stays an error.
func TestAnUnenforceableGroupIsReportedAsTheDeclaredLimit(t *testing.T) {
	restrictive := FirewallSpec{Name: "sg-r", DefaultIngress: "drop", DefaultEgress: "allow"}

	var buf bytes.Buffer
	fw := &fakeFirewaller{applyErr: fmt.Errorf("apply firewall to m/eth0: %w", ErrFirewallUnenforceable)}
	b := firewallBinding(&buf)

	b.ApplyRuleSets(context.Background(), fw, "feint-test-a", restrictive)
	declared := buf.String()
	if !strings.Contains(declared, "level=WARN") {
		t.Fatalf("a declared limit must be a warning, got %q", declared)
	}
	if !strings.Contains(declared, "firewall_public_only") {
		t.Fatalf("the warning must name the declaring capability, got %q", declared)
	}

	buf.Reset()
	fw.applyErr = errors.New("the daemon went away")
	b.ApplyRuleSets(context.Background(), fw, "feint-test-a", restrictive)
	if failure := buf.String(); !strings.Contains(failure, "level=ERROR") {
		t.Fatalf("an undeclared failure must stay an error, got %q", failure)
	}
}

// TestDropRuleSetHandsTheNameToTheRuntime is the accepting half of the removal
// path: the set of a deleted group really reaches RemoveFirewall, whose own
// ownership check is what protects the host.
func TestDropRuleSetHandsTheNameToTheRuntime(t *testing.T) {
	var buf bytes.Buffer
	fw := &fakeFirewaller{}
	firewallBinding(&buf).DropRuleSet(context.Background(), fw, "scw-abc")
	if len(fw.removed) != 1 || fw.removed[0] != "scw-abc" {
		t.Fatalf("removed %v, want [scw-abc]", fw.removed)
	}
}

// TestTheUnenforceableWarningDoesNotCallTheMachinePublicOnly is the subject
// half of #548: a declaration whose subject is wrong is worse than none,
// because it reads like proof.
//
// The line said "this machine's public-only interface", and the shape it fires
// on most is not a public-only machine at all: a Scaleway server created with
// its flexible IP carries a routed eth0 *and* a private eth1 on a managed
// network wearing the rule set. Measured on 2026-08-27 under `--vm incus-ovn`,
// that exact warning was emitted for feint-scw-675132c2… while `incus query`
// showed two NICs. An operator reading it concluded the machine was one of the
// public-only ones and that theirs was covered.
//
// What the runtime cannot filter is the interface, and the operator's next
// gesture needs the address: the log carries the driver's error, which names
// both.
func TestTheUnenforceableWarningDoesNotCallTheMachinePublicOnly(t *testing.T) {
	var buf bytes.Buffer
	fw := &fakeFirewaller{applyErr: fmt.Errorf(
		"apply firewall to m: a routed NIC accepts no security option, so what it carries "+
			"is covered by nothing: eth0 (203.0.113.2): %w", ErrFirewallUnenforceable)}

	firewallBinding(&buf).ApplyRuleSets(context.Background(), fw, "feint-test-a",
		FirewallSpec{Name: "sg-r", DefaultIngress: "drop", DefaultEgress: "allow"})

	said := buf.String()
	if strings.Contains(said, "public-only interface") {
		t.Errorf("the warning still describes the machine as public-only, which #548 measured false: %q", said)
	}
	for _, want := range []string{"eth0", "203.0.113.2", "firewall_public_only"} {
		if !strings.Contains(said, want) {
			t.Errorf("the warning does not carry %q, so it cannot be acted on: %q", want, said)
		}
	}
}
