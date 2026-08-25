package exoscale

import (
	"context"
	"strings"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// This is where an Exoscale security group stops being documentation.
//
// The pack served the whole product and reconciled none of it: three machines
// of the register's best stack ran with zero ACLs on the host while the API
// described groups whose rules open exactly two ports (#475). The mechanics —
// a permissive set attaches nothing, deny-dominant defaults, Warn-or-Error —
// live in machine.Binding, shared with the two other packs; this file holds
// only the Exoscale vocabulary.
//
// The semantics are upstream's own, from their security-group overview: "all
// incoming traffic is forbidden" until a rule allows it, "all outgoing traffic
// is allowed" — and "as soon as you define an outbound rule, outbound traffic
// is only allowed for the defined outbound rules". Rules are allow-only.

// enforcer returns the runtime's firewall half, or nil when it has none.
func (p *Pack) enforcer() machine.Firewaller {
	fw, _ := p.env.Machines.(machine.Firewaller)
	return fw
}

// firewallSpecOf translates a group and its rules for the runtime.
func (p *Pack) firewallSpecOf(group *resource.Resource) machine.FirewallSpec {
	spec := machine.FirewallSpec{
		Name:           machine.FirewallName("exo", group.ID),
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	}
	rules, _ := group.Attrs["rules"].([]any)
	for _, entry := range rules {
		rule, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		// One egress rule is what flips the egress default, upstream's own
		// sentence — quoted in the file header.
		if direction := ruleDirection(rule); direction == "egress" {
			spec.DefaultEgress = "drop"
		}
		spec.Rules = append(spec.Rules, p.firewallRulesOf(rule)...)
	}
	// The permissive egress of a group with no egress rule is written into the
	// set itself — WithPermissiveCatchAll says why an OVN NIC needs it there.
	return spec.WithPermissiveCatchAll("allow")
}

func ruleDirection(rule map[string]any) string {
	direction := strings.ToLower(stringAttr(rule["flow-direction"]))
	if direction != "egress" {
		return "ingress"
	}
	return direction
}

// firewallRulesOf converts one stored rule, expanded when it names a group
// rather than a block: bridge-backed runtimes have no group selector, so
// "traffic from that group" becomes one /32 per address its member machines
// answer on, refreshed whenever a machine starts (syncFirewallAfterBoot).
//
// A rule carrying an icmp type/code is dropped rather than approximated — the
// runtime's rule shape has no field for them, and family-wide ICMP would be
// broader than what the API describes; the log says so, because visibly
// absent is the honest state.
func (p *Pack) firewallRulesOf(rule map[string]any) []machine.FirewallRule {
	if _, precise := rule["icmp"]; precise {
		p.logger().Warn("an ICMP rule with a type and code is not expressible by this runtime "+
			"and was left out of the rule set; the API still describes it",
			"rule", stringAttr(rule["id"]))
		return nil
	}
	base := machine.FirewallRule{
		Direction: ruleDirection(rule),
		Action:    "allow",
		Protocol:  strings.ToLower(stringAttr(rule["protocol"])),
	}
	if from := int(numOf(rule["start-port"])); from > 0 {
		base.PortFrom = from
	}
	if to := int(numOf(rule["end-port"])); to > 0 {
		base.PortTo = to
	}

	blocks := make([]string, 0, 2)
	if network := stringAttr(rule["network"]); network != "" {
		blocks = append(blocks, network)
	}
	memberRef := false
	if target, ok := rule["security-group"].(map[string]any); ok {
		memberRef = true
		blocks = append(blocks, p.memberBlocks(stringAttr(target["id"]))...)
	}
	// Neither a network nor a group means "from anywhere", which an empty
	// source already says. A group that expands to nothing emits nothing: no
	// member has an address yet, and the next boot re-expands it.
	if len(blocks) == 0 {
		if memberRef {
			return nil
		}
		return []machine.FirewallRule{base}
	}
	out := make([]machine.FirewallRule, 0, len(blocks))
	for _, block := range blocks {
		converted := base
		if converted.Direction == "egress" {
			converted.Destination = block
		} else {
			converted.Source = block
		}
		out = append(out, converted)
	}
	return out
}

// numOf reads a number that may be an int fresh from a handler or a float64
// restored from a JSON snapshot.
func numOf(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	default:
		return 0
	}
}

func stringAttr(v any) string {
	s, _ := v.(string)
	return s
}

// memberBlocks is what "traffic from the members of that group" means on a
// runtime with no group selector: one /32 per address a member machine
// answers on — its public address and its private-network leases — plus the
// group's external sources, which upstream defines as extending exactly this
// membership.
func (p *Pack) memberBlocks(groupID string) []string {
	if groupID == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, inst := range p.instancesWearing(groupID) {
		if ip, _ := inst.Attrs["public-ip"].(string); ip != "" {
			out = append(out, ip+"/32")
		}
		for _, att := range instanceAttachmentsOf(inst) {
			if att.IP != "" {
				out = append(out, att.IP+"/32")
			}
		}
	}
	if group, found := p.env.Store.Get(Name, kindSecurityGroup, groupID); found {
		out = append(out, stringList(group.Attrs["external-sources"])...)
	}
	return out
}

// instancesWearing lists the instances carrying a group — named at creation,
// attached later, or inherited from their pool.
func (p *Pack) instancesWearing(groupID string) []*resource.Resource {
	all := p.env.Store.List(kindInstance, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, inst := range all {
		for _, id := range stringList(inst.Attrs[attrSecurityGroupIDs]) {
			if id == groupID {
				out = append(out, inst)
				break
			}
		}
	}
	return out
}

// syncSecurityGroup pushes a group's rule set and replays it onto every
// instance wearing it. Called after any change to the group or to its rules.
func (p *Pack) syncSecurityGroup(ctx context.Context, group *resource.Resource) {
	fw := p.enforcer()
	if fw == nil {
		return
	}
	b := p.binding()
	if !b.SyncRuleSet(ctx, fw, p.firewallSpecOf(group)) {
		return
	}
	for _, inst := range p.instancesWearing(group.ID) {
		p.applyInstanceRuleSets(ctx, inst)
	}
}

// applyInstanceRuleSets attaches everything one instance wears to its machine,
// in one call, because the runtime replaces the attachment list rather than
// merging it. Every set is written first, so an attach cannot name a set the
// runtime has never seen.
func (p *Pack) applyInstanceRuleSets(ctx context.Context, inst *resource.Resource) {
	fw := p.enforcer()
	if fw == nil {
		return
	}
	name := inst.Runtime[p.binding().RuntimeKey]
	if name == "" {
		return
	}
	b := p.binding()
	specs := make([]machine.FirewallSpec, 0, 2)
	for _, id := range stringList(inst.Attrs[attrSecurityGroupIDs]) {
		group, found := p.env.Store.Get(Name, kindSecurityGroup, id)
		if !found {
			continue
		}
		spec := p.firewallSpecOf(group)
		if !b.SyncRuleSet(ctx, fw, spec) {
			continue
		}
		specs = append(specs, spec)
	}
	b.ApplyRuleSets(ctx, fw, name, specs...)
}

// syncFirewallAfterBoot runs once a machine exists: its own sets attach, and
// every group whose rules point at a group this instance wears is re-expanded,
// because this machine's addresses are now part of what those groups mean.
// Without the second half, "the application tier accepts the web tier and
// nobody else" — the sentence examples/stacks/exoscale writes — never contains
// the web machines it talks about.
func (p *Pack) syncFirewallAfterBoot(ctx context.Context, inst *resource.Resource) {
	if p.enforcer() == nil {
		return
	}
	p.applyInstanceRuleSets(ctx, inst)
	p.syncGroupsReferencing(ctx, stringList(inst.Attrs[attrSecurityGroupIDs]))
}

// syncGroupsReferencing re-syncs every group one of whose rules names one of
// the given groups as a member source.
func (p *Pack) syncGroupsReferencing(ctx context.Context, groupIDs []string) {
	if len(groupIDs) == 0 || p.enforcer() == nil {
		return
	}
	named := make(map[string]bool, len(groupIDs))
	for _, id := range groupIDs {
		named[id] = true
	}
	for _, group := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		if groupReferencesAny(group, named) {
			p.syncSecurityGroup(ctx, group)
		}
	}
}

// groupReferencesAny reports whether any rule of the group names one of the
// given groups as its source.
func groupReferencesAny(group *resource.Resource, named map[string]bool) bool {
	rules, _ := group.Attrs["rules"].([]any)
	for _, entry := range rules {
		rule, _ := entry.(map[string]any)
		if target, ok := rule["security-group"].(map[string]any); ok {
			if named[stringAttr(target["id"])] {
				return true
			}
		}
	}
	return false
}

// removeFirewall drops the runtime rule set of a group being deleted.
func (p *Pack) removeFirewall(ctx context.Context, group *resource.Resource) {
	p.binding().DropRuleSet(ctx, p.enforcer(), machine.FirewallName("exo", group.ID))
}

// EnforcesFirewall implements emulator.FirewallEnforcer: this pack reconciles
// a security group onto the machines that wear it, so a runtime able to
// enforce rules is handed them (#475).
//
// It answers about the pack and not about the process — the reasoning the
// Scaleway pack's declaration carries in full. The day this pack stops handing
// rules over, this line is what has to change, and
// internal/cli's TestEveryPackThatWiresTheFirewallSaysSo is what notices if
// it does not.
func (p *Pack) EnforcesFirewall() bool { return true }
