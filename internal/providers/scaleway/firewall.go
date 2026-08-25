package scaleway

import (
	"context"
	"strings"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// This is where a security group stops being documentation.
//
// The emulator serves the whole product either way; what changes here is that a
// runtime able to enforce rules is handed them, so a closed port refuses the
// connection instead of merely being described as closed. When the runtime
// cannot enforce anything, nothing breaks and nothing is claimed: the rules stay
// metadata, and docs/limits.md says so.
//
// Reconciliation is not optional. A client authorises a rule after the server is
// running and expects it to take effect, so every mutation of a group replays it
// onto the machines that carry it.

// enforcer returns the runtime's firewall half, or nil when it has none.
func (p *Pack) enforcer() machine.Firewaller {
	fw, _ := p.env.Machines.(machine.Firewaller)
	return fw
}

// syncSecurityGroup pushes a group and replays it onto every server using it.
// Called after any change to the group or to its rules.
//
// The mechanics — a permissive set attaches nothing, the deny-dominant
// defaults, the Warn-or-Error report — are machine.Binding's, shared with the
// two other packs since #475: written here alone, the same sequence was one
// Outscale and Exoscale never wrote, and their machines ran with zero ACLs
// while the API described a closed port. What stays in this file is only the
// Scaleway vocabulary.
func (p *Pack) syncSecurityGroup(ctx context.Context, group *resource.Resource) {
	fw := p.enforcer()
	if fw == nil {
		return
	}
	b := p.binding()
	spec := p.firewallSpecOf(group)
	if !b.SyncRuleSet(ctx, fw, spec) {
		return
	}
	for _, server := range p.serverResourcesUsing(group) {
		b.ApplyRuleSets(ctx, fw, server.Runtime[runtimeMachineKey], spec)
	}
}

// applyServerFirewall binds the group a server carries to its machine. Called
// once the machine exists, since a rule set attaches to interfaces.
func (p *Pack) applyServerFirewall(ctx context.Context, server *resource.Resource) {
	fw := p.enforcer()
	if fw == nil {
		return
	}
	summary, _ := server.Attrs["security_group"].(map[string]any)
	groupID, _ := summary["id"].(string)
	if groupID == "" {
		return
	}
	group, found := p.env.Store.Get(Name, kindSecurityGroup, groupID)
	if !found {
		return
	}

	b := p.binding()
	spec := p.firewallSpecOf(group)
	if !b.SyncRuleSet(ctx, fw, spec) {
		return
	}
	b.ApplyRuleSets(ctx, fw, server.Runtime[runtimeMachineKey], spec)
}

// firewallSpecOf translates a group and its rules.
//
// The default policies travel with the set: Scaleway states them per direction,
// and they are what a rule does not override. Note the mapping of accept onto
// allow, of inbound onto ingress, and that a rule's ip_range is a source going
// in and a destination going out.
func (p *Pack) firewallSpecOf(group *resource.Resource) machine.FirewallSpec {
	inbound, _ := group.Attrs["inbound_default_policy"].(string)
	outbound, _ := group.Attrs["outbound_default_policy"].(string)

	spec := machine.FirewallSpec{
		Name:           machine.FirewallName("scw", group.ID),
		DefaultIngress: toRuntimeAction(inbound),
		DefaultEgress:  toRuntimeAction(outbound),
	}
	// A stateless group is a real Scaleway setting, and the runtime has the
	// matching action: allow tracks connections, allow-stateless does not. A
	// stateless group whose rules were translated to plain allow would let
	// return traffic through, which is the whole difference the flag names.
	stateful, ok := group.Attrs["stateful"].(bool)
	allow := "allow"
	if ok && !stateful {
		allow = "allow-stateless"
	}

	// Isolation first: the runtime orders rules by action, not by position, so a
	// reject wins over any allow the group adds. Two private networks that
	// upstream keeps apart must not reach each other whatever the group says,
	// because that separation is the subnet's, not the group's. A runtime whose
	// networks are separate by construction gets none of this: the blocks would
	// name subnets the machine cannot reach anyway.
	if !p.nativeIsolation() {
		for _, block := range p.foreignBlocksFor(group) {
			spec.Rules = append(spec.Rules,
				machine.FirewallRule{Direction: "egress", Action: "reject", Destination: block},
				machine.FirewallRule{Direction: "ingress", Action: "reject", Source: block},
			)
		}
	}

	for _, rule := range p.rulesOf(group.ID) {
		converted, ok := toFirewallRule(rule)
		if !ok {
			continue
		}
		if converted.Action == "allow" {
			converted.Action = allow
		}
		spec.Rules = append(spec.Rules, converted)
	}

	// A permissive default policy is also written into the rule set itself —
	// WithPermissiveCatchAll says why, and carries the pack's own allow verb so
	// a stateless group's openness stays stateless.
	return spec.WithPermissiveCatchAll(allow)
}

// nativeIsolation reports whether the runtime keeps its networks apart by
// construction, in which case reject rules against foreign subnets are dead
// weight and reachability is granted by peering instead (see isolateNetworks).
func (p *Pack) nativeIsolation() bool {
	peerer, ok := p.env.Machines.(machine.Peerer)
	return ok && peerer.NativeIsolation()
}

// foreignBlocksFor lists the emulated subnets a server carrying this group must
// not reach: another project, or another VPC, or a VPC that does not route
// between its own private networks.
//
// The group is the carrier rather than the subject: a rule set attaches to
// interfaces, and this is the only rule set a server's interfaces carry.
func (p *Pack) foreignBlocksFor(group *resource.Resource) []string {
	all := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})
	if len(all) < 2 {
		return nil
	}

	// The networks the servers of this group actually sit on.
	own := map[string]bool{}
	for _, server := range p.serverResourcesUsing(group) {
		for _, nic := range p.privateNICsOf(server.ID) {
			own[nic.Runtime[runtimePrivateNetworkKey]] = true
		}
	}
	if len(own) == 0 {
		return nil
	}

	blocks := make([]string, 0, len(all))
	for _, candidate := range all {
		if own[candidate.ID] {
			continue
		}
		reachable := false
		for id := range own {
			if from, found := p.env.Store.Get(Name, kindPrivateNetwork, id); found && p.reachableFrom(from, candidate) {
				reachable = true
				break
			}
		}
		if reachable {
			continue
		}
		if block, _ := candidate.Attrs["subnet"].(string); block != "" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// toFirewallRule converts one stored rule. A rule the runtime cannot express is
// dropped rather than approximated, and the caller logs nothing: the API still
// serves it, which is the honest state.
func toFirewallRule(res *resource.Resource) (machine.FirewallRule, bool) {
	direction, _ := res.Attrs["direction"].(string)
	action, _ := res.Attrs["action"].(string)
	protocol, _ := res.Attrs["protocol"].(string)
	ipRange, _ := res.Attrs["ip_range"].(string)

	out := machine.FirewallRule{
		Action:   toRuntimeAction(action),
		Protocol: strings.ToLower(protocol),
		PortFrom: portValue(res.Attrs["dest_port_from"]),
		PortTo:   portValue(res.Attrs["dest_port_to"]),
	}
	switch direction {
	case "outbound":
		out.Direction = "egress"
		out.Destination = ipRange
	default:
		out.Direction = "ingress"
		out.Source = ipRange
	}
	if out.Action == "" {
		return machine.FirewallRule{}, false
	}
	return out, true
}

// toRuntimeAction maps Scaleway's vocabulary onto the runtime's. Scaleway drops
// silently, which is what "drop" means here too; there is no reject upstream.
func toRuntimeAction(action string) string {
	switch action {
	case policyAccept:
		return "allow"
	case policyDrop:
		return "drop"
	default:
		return ""
	}
}

// portValue reads a port back from Attrs, which may hold a uint32 fresh from a
// request or a float64 restored from a JSON snapshot.
func portValue(v any) int {
	switch n := v.(type) {
	case uint32:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// serverResourcesUsing returns the servers attached to a group, where
// serversUsing returns only their names for the API view.
func (p *Pack) serverResourcesUsing(group *resource.Resource) []*resource.Resource {
	all := p.env.Store.List(kindServer, p.tenant(group.Tenant.Zone))
	out := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		summary, _ := res.Attrs["security_group"].(map[string]any)
		if summary != nil && summary["id"] == group.ID {
			out = append(out, res)
		}
	}
	return out
}

// removeFirewall drops the runtime rule set of a group being deleted. Failure is
// logged rather than fatal: the group is already gone from the control plane,
// and refusing the delete afterwards would leave the client with a resource it
// cannot remove.
func (p *Pack) removeFirewall(ctx context.Context, group *resource.Resource) {
	p.binding().DropRuleSet(ctx, p.enforcer(), machine.FirewallName("scw", group.ID))
}

// EnforcesFirewall implements emulator.FirewallEnforcer: this pack reconciles a
// security group onto the machines that carry it, so a runtime able to enforce
// rules is handed them.
//
// It answers about the pack and not about the process, which is why it is a
// constant rather than a look at p.env.Machines. `/_feint/health` publishes the
// driver's capabilities beside this, and a consumer needs both: what the runtime
// can do, and who asks it to. Reading the first alone is what told a user the
// firewall was delivered for three packs when only this one delivered it (#180).
//
// The day this pack stops handing rules over, this line is what has to change,
// and TestEnforcementNamesOnlyThePacksThatWireIt is what notices if it does not.
func (p *Pack) EnforcesFirewall() bool { return true }
