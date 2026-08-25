package outscale

import (
	"context"
	"strings"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// This is where an Outscale security group stops being documentation.
//
// The pack served the whole product and reconciled none of it: not one file
// here touched the runtime's firewall half, so a Vm wearing a group whose only
// rule opens tcp/22 answered on every port the group forbids — measured in
// #475 with a listener proved up from inside the machine first. The Scaleway
// pack had the sequence all along; the mechanics now live in machine.Binding
// (SyncRuleSet, ApplyRuleSets, DropRuleSet), and what this file holds is only
// what is Outscale's: its rule vocabulary, and who wears what.
//
// The semantics are the measured ones. A group is an allow-list in both
// directions — traffic no rule matches is dropped — and a fresh group carries
// its permissive outbound explicitly, as the allow-all OutboundRules entry the
// create stores (securitygroups.go quotes the recording). So the spec's
// defaults are drop/drop and the stored rules are the whole story, including
// the default group's "everything from myself" inbound rule, which expands
// below like any other member reference.

// enforcer returns the runtime's firewall half, or nil when it has none.
func (p *Pack) enforcer() machine.Firewaller {
	fw, _ := p.env.Machines.(machine.Firewaller)
	return fw
}

// firewallSpecOf translates a group and its rules for the runtime.
//
// fresh, when set, is the resource a transition is still working on: the boot
// that triggers a re-expansion runs before its own commit, so the store's copy
// of that Vm does not yet carry the address the expansion is being run FOR.
// Reading the store alone here silently missed the very machine that booted.
// TestAMemberSourcedRuleExpandsToTheMembersAddresses fails without it.
func (p *Pack) firewallSpecOf(group *resource.Resource, fresh *resource.Resource) machine.FirewallSpec {
	spec := machine.FirewallSpec{
		Name:           machine.FirewallName("osc", group.ID),
		DefaultIngress: "drop",
		DefaultEgress:  "drop",
	}
	spec.Rules = append(spec.Rules, p.firewallRulesOf(group, "InboundRules", "ingress", fresh)...)
	spec.Rules = append(spec.Rules, p.firewallRulesOf(group, "OutboundRules", "egress", fresh)...)
	return spec
}

// firewallRulesOf converts one side of a group. A rule that names members
// rather than blocks is expanded here into the addresses those members'
// machines answer on, because bridge-backed runtimes have no group selector —
// firewall.go in the machine package says so — and the expansion is refreshed
// whenever a machine starts or publishes an address (syncFirewallAfterBoot).
func (p *Pack) firewallRulesOf(group *resource.Resource, side, direction string, fresh *resource.Resource) []machine.FirewallRule {
	raw, _ := group.Attrs[side].([]any)
	out := make([]machine.FirewallRule, 0, len(raw))
	for _, entry := range raw {
		rule, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		base := machine.FirewallRule{
			Direction: direction,
			Action:    "allow",
			Protocol:  protocolOf(rule["IpProtocol"]),
		}
		if from := int(numOf(rule["FromPortRange"])); from > 0 {
			base.PortFrom = from
		}
		if to := int(numOf(rule["ToPortRange"])); to > 0 {
			base.PortTo = to
		}

		blocks := make([]string, 0, 2)
		for _, r := range stringsOf(rule["IpRanges"]) {
			if r != "" {
				blocks = append(blocks, r)
			}
		}
		if members, ok := rule["SecurityGroupsMembers"].([]any); ok {
			for _, m := range members {
				member, _ := m.(map[string]any)
				blocks = append(blocks, p.memberBlocks(stringOf(member["SecurityGroupId"]), fresh)...)
			}
		}
		// A rule with neither ranges nor members means "from anywhere", which
		// an empty Source already says. A member list that expands to nothing
		// emits nothing: no machine of that group has an address yet, so there
		// is nothing to allow, and the next boot re-expands it.
		if len(blocks) == 0 {
			if _, membersOnly := rule["SecurityGroupsMembers"]; !membersOnly {
				out = append(out, base)
			}
			continue
		}
		for _, block := range blocks {
			converted := base
			if direction == "egress" {
				converted.Destination = block
			} else {
				converted.Source = block
			}
			out = append(out, converted)
		}
	}
	return out
}

// protocolOf maps the stored IpProtocol onto the runtime's spelling: "-1" is
// Outscale's "every protocol", which the runtime writes as an empty one.
func protocolOf(v any) string {
	proto := strings.ToLower(stringOf(v))
	if proto == "-1" {
		return ""
	}
	return proto
}

// memberBlocks is what "traffic from the members of that group" means on a
// runtime with no group selector: one /32 per address a member machine
// answers on — its private address, and every public one linked to it.
func (p *Pack) memberBlocks(groupID string, fresh *resource.Resource) []string {
	if groupID == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, vm := range p.vmsWearing(groupID) {
		// The transition's own copy wins over the store's: the store has not
		// been committed yet when a boot re-expands the groups naming it.
		if fresh != nil && vm.ID == fresh.ID {
			vm = fresh
		}
		if ip := stringOf(vm.Attrs["PrivateIp"]); ip != "" {
			out = append(out, ip+"/32")
		}
		for _, ip := range p.publicBootAddresses(vm.ID) {
			out = append(out, ip+"/32")
		}
	}
	return out
}

// vmsWearing lists the Vms carrying a group — asked for, or inherited as
// their Net's default — terminated ones excepted.
func (p *Pack) vmsWearing(groupID string) []*resource.Resource {
	all := p.env.Store.List(kindVM, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, vm := range all {
		if vm.State == stateTerminated {
			continue
		}
		for _, ref := range p.effectiveSecurityGroups(vm) {
			summary, _ := ref.(map[string]any)
			if stringOf(summary["SecurityGroupId"]) == groupID {
				out = append(out, vm)
				break
			}
		}
	}
	return out
}

// syncSecurityGroup pushes a group's rule set and replays it onto every Vm
// wearing it. Called after any change to the group or to its rules.
func (p *Pack) syncSecurityGroup(ctx context.Context, group *resource.Resource) {
	fw := p.enforcer()
	if fw == nil {
		return
	}
	p.syncSecurityGroupSeen(ctx, group, nil)
}

// syncSecurityGroupSeen is syncSecurityGroup with the transition's own copy of
// a booting Vm, which the expansion must see — firewallSpecOf says why.
func (p *Pack) syncSecurityGroupSeen(ctx context.Context, group, fresh *resource.Resource) {
	fw := p.enforcer()
	if fw == nil {
		return
	}
	b := p.binding()
	if !b.SyncRuleSet(ctx, fw, p.firewallSpecOf(group, fresh)) {
		return
	}
	for _, vm := range p.vmsWearing(group.ID) {
		if fresh != nil && vm.ID == fresh.ID {
			vm = fresh
		}
		p.applyVMRuleSets(ctx, vm)
	}
}

// applyVMRuleSets attaches everything one Vm wears to its machine, in one
// call, because the runtime replaces the attachment list rather than merging
// it. Every set is written first, so an attach cannot name a set the runtime
// has never seen.
func (p *Pack) applyVMRuleSets(ctx context.Context, vm *resource.Resource) {
	fw := p.enforcer()
	if fw == nil {
		return
	}
	name := vm.Runtime[p.binding().RuntimeKey]
	if name == "" {
		return
	}
	b := p.binding()
	specs := make([]machine.FirewallSpec, 0, 2)
	for _, ref := range p.effectiveSecurityGroups(vm) {
		summary, _ := ref.(map[string]any)
		group, found := p.env.Store.Get(Name, kindSecurityGroup, stringOf(summary["SecurityGroupId"]))
		if !found {
			continue
		}
		spec := p.firewallSpecOf(group, vm)
		if !b.SyncRuleSet(ctx, fw, spec) {
			continue
		}
		specs = append(specs, spec)
	}
	b.ApplyRuleSets(ctx, fw, name, specs...)
}

// syncFirewallAfterBoot runs once a machine exists or first publishes an
// address: its own sets attach, and every group whose rules point at a group
// this Vm wears is re-expanded, because this Vm's addresses are now part of
// what those groups mean. Without the second half, the three-tier statement a
// stack writes — tier 2 accepts tier 1 and nobody else — never contains the
// machines it talks about.
func (p *Pack) syncFirewallAfterBoot(ctx context.Context, vm *resource.Resource) {
	if p.enforcer() == nil {
		return
	}
	p.applyVMRuleSets(ctx, vm)
	p.syncGroupsReferencingSeen(ctx, p.wornGroupIDs(vm), vm)
}

// wornGroupIDs is the identifiers of the groups one Vm wears.
func (p *Pack) wornGroupIDs(vm *resource.Resource) []string {
	refs := p.effectiveSecurityGroups(vm)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		summary, _ := ref.(map[string]any)
		if id := stringOf(summary["SecurityGroupId"]); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// syncGroupsReferencing re-syncs every group one of whose rules names one of
// the given groups as a member source.
func (p *Pack) syncGroupsReferencing(ctx context.Context, groupIDs []string) {
	p.syncGroupsReferencingSeen(ctx, groupIDs, nil)
}

func (p *Pack) syncGroupsReferencingSeen(ctx context.Context, groupIDs []string, fresh *resource.Resource) {
	if len(groupIDs) == 0 {
		return
	}
	named := make(map[string]bool, len(groupIDs))
	for _, id := range groupIDs {
		named[id] = true
	}
	for _, group := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		if groupReferencesAny(group, named) {
			p.syncSecurityGroupSeen(ctx, group, fresh)
		}
	}
}

// groupReferencesAny reports whether any rule of the group names one of the
// given groups in its members.
func groupReferencesAny(group *resource.Resource, named map[string]bool) bool {
	for _, side := range []string{"InboundRules", "OutboundRules"} {
		rules, _ := group.Attrs[side].([]any)
		for _, entry := range rules {
			rule, _ := entry.(map[string]any)
			members, _ := rule["SecurityGroupsMembers"].([]any)
			for _, m := range members {
				member, _ := m.(map[string]any)
				if named[stringOf(member["SecurityGroupId"])] {
					return true
				}
			}
		}
	}
	return false
}

// removeFirewall drops the runtime rule set of a group being deleted.
func (p *Pack) removeFirewall(ctx context.Context, group *resource.Resource) {
	p.binding().DropRuleSet(ctx, p.enforcer(), machine.FirewallName("osc", group.ID))
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
