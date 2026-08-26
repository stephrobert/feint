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

	spec := p.firewallSpecOf(group(policyDrop, policyAccept, true), nil)
	if got := catchAlls(spec); got["ingress"] != "" || got["egress"] != "allow" {
		t.Errorf("drop/accept: catch-alls = %v, want egress allow only", got)
	}

	spec = p.firewallSpecOf(group(policyAccept, policyAccept, false), nil)
	if got := catchAlls(spec); got["ingress"] != "allow-stateless" || got["egress"] != "allow-stateless" {
		t.Errorf("stateless accept/accept: catch-alls = %v, want allow-stateless both ways", got)
	}

	spec = p.firewallSpecOf(group(policyDrop, policyDrop, true), nil)
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

// TestBridgeModeRejectsForeignSubnetsThroughTheGroup holds this pack's wiring
// of the bridge-mode defence: on a runtime whose networks are born joined, the
// subnets a server must not reach ride its group's rule set as rejects,
// because a NIC carrying a rule set no longer obeys the network-level
// isolation. The mechanism moved to the shared skeleton with #509; what this
// asserts is that the pack still declares its foreign blocks to it — losing
// the GroupSync.ForeignBlocks field would fail here, and nothing else
// white-box held it.
func TestBridgeModeRejectsForeignSubnetsThroughTheGroup(t *testing.T) {
	rec := machine.NewRecorder()
	rec.Joined = true // the bridge shape: networks reach each other unless rejected
	env := emulator.DefaultEnv()
	env.Machines = rec
	p := New(env)

	tenant := resource.Tenant{Provider: Name, Project: defaultProject, Zone: "fr-par-1"}
	now := env.Now()
	pn := func(id, block string) *resource.Resource {
		res := resource.New(id, kindPrivateNetwork, tenant, "available", now)
		res.Attrs = map[string]any{"subnet": block}
		env.Store.Put(res)
		return res
	}
	own := pn("11111111-1111-4111-8111-111111111111", "172.16.4.0/22")
	pn("22222222-2222-4222-8222-222222222222", "172.16.8.0/22")

	group := resource.New("33333333-3333-4333-8333-333333333333", kindSecurityGroup, tenant, "available", now)
	group.Attrs = map[string]any{
		"inbound_default_policy":  policyDrop,
		"outbound_default_policy": policyAccept,
		"stateful":                true,
	}
	env.Store.Put(group)

	server := resource.New("44444444-4444-4444-8444-444444444444", kindServer, tenant, "running", now)
	server.Attrs = map[string]any{"security_group": map[string]any{"id": group.ID}}
	server.Runtime = map[string]string{runtimeMachineKey: "feint-scw-" + server.ID}
	env.Store.Put(server)

	nic := resource.New("55555555-5555-4555-8555-555555555555", kindPrivateNIC, tenant, "available", now)
	nic.Runtime = map[string]string{runtimeServerKey: server.ID, runtimePrivateNetworkKey: own.ID}
	env.Store.Put(nic)

	p.syncSecurityGroup(t.Context(), group)

	setName := machine.FirewallName("scw", group.ID)
	var spec *machine.FirewallSpec
	for _, e := range rec.Events() {
		if e.Kind == "EnsureFirewall" && e.Resource == setName {
			s := e.Args.(machine.FirewallSpec)
			spec = &s
		}
	}
	if spec == nil {
		t.Fatalf("the runtime never received the rule set %s", setName)
	}
	rejected := false
	for _, rule := range spec.Rules {
		if rule.Action == "reject" && (rule.Source == "172.16.8.0/22" || rule.Destination == "172.16.8.0/22") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("the foreign subnet is not rejected by the group's set in bridge mode; rules: %+v", spec.Rules)
	}
}
