package scaleway

import (
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

// The permissive-set-binds-nothing rule and the Warn-or-Error report moved to
// the shared layer with #475 — machine.Binding.ApplyRuleSets — and their tests
// went with them: TestAPermissiveGroupBindsNothingOnEveryRuntime and
// TestAnUnenforceableGroupIsReportedAsTheDeclaredLimit now live in
// internal/core/machine/firewall_binding_test.go, holding the behaviour once
// for the three packs.
