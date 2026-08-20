package scaleway

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The default policies must live inside the rule set, not only in the NIC
// binding: an OVN NIC cannot take the binding's default-action keys without a
// re-plug, so the catch-all rule is the one place a permissive policy reaches
// every runtime. White-box on purpose — the spec is what the runtime enforces,
// and no HTTP response shows it.
func TestFirewallSpecWritesPermissiveDefaultsAsCatchAlls(t *testing.T) {
	group := func(inbound, outbound string, stateful bool) *resource.Resource {
		return &resource.Resource{
			ID:     "11111111-2222-3333-4444-555555555555",
			Kind:   kindSecurityGroup,
			Tenant: resource.Tenant{Provider: Name, Project: defaultProject, Zone: "fr-par-1"},
			Attrs: map[string]any{
				"inbound_default_policy":  inbound,
				"outbound_default_policy": outbound,
				"stateful":                stateful,
			},
		}
	}
	catchAlls := func(spec machine.FirewallSpec) map[string]string {
		out := map[string]string{}
		for _, rule := range spec.Rules {
			if rule.Source == "" && rule.Destination == "" && rule.Protocol == "" {
				out[rule.Direction] = rule.Action
			}
		}
		return out
	}
	p := New(emulator.DefaultEnv())

	spec := p.firewallSpecOf(group(policyDrop, policyAccept, true))
	if got := catchAlls(spec); got["ingress"] != "" || got["egress"] != "allow" {
		t.Errorf("drop/accept: catch-alls = %v, want egress allow only", got)
	}

	spec = p.firewallSpecOf(group(policyAccept, policyAccept, false))
	if got := catchAlls(spec); got["ingress"] != "allow-stateless" || got["egress"] != "allow-stateless" {
		t.Errorf("stateless accept/accept: catch-alls = %v, want allow-stateless both ways", got)
	}

	spec = p.firewallSpecOf(group(policyDrop, policyDrop, true))
	if got := catchAlls(spec); len(got) != 0 {
		t.Errorf("drop/drop: catch-alls = %v, want none, or the group would open what it closes", got)
	}
}

// TestAPermissiveGroupBindsNothingOnEveryRuntime holds the unconditional half
// of bindingOf (#337). The empty binding used to be OVN's alone, for the
// pipeline-priority reason its comment keeps; but the default security group —
// pure accept, filtering nothing upstream — rides every `scw instance server
// create`, including onto a server whose only interface is a routed NIC that
// accepts no rule set at all. On the bridge runtime that attach was an ERROR
// log the control plane answered over as if the group were enforced. The only
// faithful translation of "filters nothing" is absence, on every runtime.
func TestAPermissiveGroupBindsNothingOnEveryRuntime(t *testing.T) {
	p := New(emulator.DefaultEnv())
	if p.nativeIsolation() {
		t.Fatal("the default env must not isolate natively, or this test measures the OVN branch")
	}

	permissive := machine.FirewallSpec{
		Name:           "sg-permissive",
		DefaultIngress: "allow",
		DefaultEgress:  "allow",
		Rules: []machine.FirewallRule{
			{Direction: "ingress", Action: "allow"},
			{Direction: "egress", Action: "allow"},
		},
	}
	if got := p.bindingOf(permissive); len(got.Names) != 0 {
		t.Fatalf("a group that filters nothing must attach nothing, got %v", got.Names)
	}

	// The accepting half: a group that restricts something still attaches.
	restrictive := permissive
	restrictive.DefaultIngress = "drop"
	if got := p.bindingOf(restrictive); len(got.Names) != 1 || got.DefaultIngress != "drop" {
		t.Fatalf("a restricting group must still bind, got %+v", got)
	}
}

// TestAnUnenforceableGroupIsReportedAsTheDeclaredLimit separates the two
// meanings one ERROR line used to conflate (#337). A rule set the runtime has
// no mechanism for — machine.ErrFirewallUnenforceable, the routed NIC — is a
// declared limit, published as capabilities.firewall_public_only=false, and is
// reported as a warning that names the declaration. A failure nothing declared
// stays an error.
func TestAnUnenforceableGroupIsReportedAsTheDeclaredLimit(t *testing.T) {
	var buf bytes.Buffer
	env := emulator.DefaultEnv()
	env.Log = slog.New(slog.NewTextHandler(&buf, nil))
	p := New(env)

	p.reportFirewall(fmt.Errorf("apply firewall to m/eth0: %w", machine.ErrFirewallUnenforceable),
		"server", "srv-1")
	declared := buf.String()
	if !strings.Contains(declared, "level=WARN") {
		t.Fatalf("a declared limit must be a warning, got %q", declared)
	}
	if !strings.Contains(declared, "firewall_public_only") {
		t.Fatalf("the warning must name the declaring capability, got %q", declared)
	}

	buf.Reset()
	p.reportFirewall(errors.New("the daemon went away"), "server", "srv-1")
	if failure := buf.String(); !strings.Contains(failure, "level=ERROR") {
		t.Fatalf("an undeclared failure must stay an error, got %q", failure)
	}
}
