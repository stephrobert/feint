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

// groupSync is this pack's half of the shared firewall orchestration (#509):
// the skeleton — sync-then-apply, the wearer replay, the fresh copy, the
// after-boot re-expansion — lives in machine.GroupSync, written once for every
// pack; what is declared here is only what Exoscale knows. Referencing
// machine.GroupSync is also this pack's marker for the enforcement test: a
// pack wires the firewall when a non-test file of its own builds one of these.
func (p *Pack) groupSync() machine.GroupSync {
	return machine.GroupSync{
		Binding: p.binding(),
		SpecOf:  p.firewallSpecOf,
		Wearers: func(group *resource.Resource) []*resource.Resource {
			return p.instancesWearing(group.ID)
		},
		WornIDs: p.wornGroupIDs,
		Group: func(id string) (*resource.Resource, bool) {
			return p.env.Store.Get(Name, kindSecurityGroup, id)
		},
		Referrers: p.groupsReferencing,
		// Where this provider's groups apply, which on Exoscale is not every
		// interface (#574): the same plan the Reconciler executes, read here
		// for the one thing it says about scope — a private-network
		// membership is declared Unfiltered, because upstream states that
		// security group rules do not apply inside private networks. Wiring
		// the same function twice rather than a second walk of the same
		// attributes: a scope spelled apart from the plan is a scope that
		// stops describing the interfaces the plan creates.
		PlanOf: p.machinePlan,
	}
}

// wornGroupIDs is the identifiers of the groups one instance wears.
func (p *Pack) wornGroupIDs(res *resource.Resource) []string {
	return stringList(res.Attrs[attrSecurityGroupIDs])
}

// firewallSpecOf translates a group and its rules for the runtime.
//
// fresh, when set, is the resource a transition is still working on: the boot
// that triggers a re-expansion runs before its own commit, so the store's copy
// of that instance does not yet carry the addresses the expansion is being run
// FOR. This pack read the store alone until the skeleton threaded fresh for
// every pack at once — whether its gap was reachable was never established
// (the audit's open question on #509), and now the question no longer exists.
func (p *Pack) firewallSpecOf(group, fresh *resource.Resource) machine.FirewallSpec {
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
		spec.Rules = append(spec.Rules, p.firewallRulesOf(rule, fresh)...)
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
// answer on, refreshed whenever a machine starts (GroupSync.AfterBoot).
//
// A rule carrying an icmp type/code is dropped rather than approximated — the
// runtime's rule shape has no field for them, and family-wide ICMP would be
// broader than what the API describes; the log says so, because visibly
// absent is the honest state.
func (p *Pack) firewallRulesOf(rule map[string]any, fresh *resource.Resource) []machine.FirewallRule {
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
	if from := int(resource.Number(rule["start-port"])); from > 0 {
		base.PortFrom = from
	}
	if to := int(resource.Number(rule["end-port"])); to > 0 {
		base.PortTo = to
	}

	blocks := make([]string, 0, 2)
	if network := stringAttr(rule["network"]); network != "" {
		blocks = append(blocks, network)
	}
	memberRef := false
	if target, ok := rule["security-group"].(map[string]any); ok {
		memberRef = true
		blocks = append(blocks, p.memberBlocks(stringAttr(target["id"]), fresh)...)
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

func stringAttr(v any) string {
	s, _ := v.(string)
	return s
}

// memberBlocks is what "traffic from the members of that group" means on a
// runtime with no group selector: one /32 per address a member machine
// answers on — its public address and its private-network leases — plus the
// group's external sources, which upstream defines as extending exactly this
// membership. The transition's own copy wins over the store's: the store has
// not been committed yet when a boot re-expands the groups naming it.
func (p *Pack) memberBlocks(groupID string, fresh *resource.Resource) []string {
	if groupID == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, inst := range p.instancesWearing(groupID) {
		if fresh != nil && inst.ID == fresh.ID {
			inst = fresh
		}
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
// The skeleton is machine.GroupSync's, shared with the two other packs.
func (p *Pack) syncSecurityGroup(ctx context.Context, group *resource.Resource) {
	p.groupSync().SyncGroup(ctx, group, nil)
}

// groupsReferencing lists every group one of whose rules names one of the
// given groups as its source — the enumeration is Exoscale's (its rule
// attributes), the re-sync it feeds is the shared skeleton's.
func (p *Pack) groupsReferencing(named map[string]bool) []*resource.Resource {
	var out []*resource.Resource
	for _, group := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		if groupReferencesAny(group, named) {
			out = append(out, group)
		}
	}
	return out
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
	p.groupSync().Drop(ctx, machine.FirewallName("exo", group.ID))
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
