package machine

import (
	"context"
	"errors"
)

// The reconciliation of a rule set onto machines, shared by every pack.
//
// The translation of a security group into a FirewallSpec is each provider's
// own vocabulary and stays in its pack. Everything after the translation — a
// permissive set attaches nothing, the defaults ride the binding, a refusal
// the runtime declares is a Warn where anything else is an Error — is the same
// behaviour three times over, and it was written once: only Scaleway handed
// its groups to the runtime, and an Outscale Vm and three Exoscale machines
// ran with zero ACLs on the host while their API described a closed port
// (#475). A sequence written per pack is a sequence two packs never wrote.

// EnforcesNothing reports whether a rule set restricts any traffic at all:
// permissive in both directions, with no rule that denies anything.
//
// A group that enforces nothing attaches nothing, on every runtime. This is
// not an optimisation, and each runtime family contributed its own measured
// reason:
//
//   - OVN: the pipeline evaluates the sender's egress rules before the
//     receiver's ingress default (the priority constants in acl_ovn.go at
//     v7.2.0: NIC ingress default 100, NIC egress default 111, rule-level
//     allow 300), so any allow carried by the sender's rule set opens a port
//     the receiver's default-deny closes — measured with the Scaleway suite's
//     own probe machine.
//   - routed NICs (#337): a default group — pure accept, filtering nothing
//     upstream — rides onto a server whose one interface accepts no rule set
//     at all. Attaching the empty policy there was an ERROR log the control
//     plane answered over as if the group were enforced.
//
// TestAPermissiveGroupBindsNothingOnEveryRuntime fails without the
// unconditional half.
func (s FirewallSpec) EnforcesNothing() bool {
	if s.DefaultIngress != "allow" || s.DefaultEgress != "allow" {
		return false
	}
	for _, rule := range s.Rules {
		if rule.Action == "drop" || rule.Action == "reject" {
			return false
		}
	}
	return true
}

// WithPermissiveCatchAll writes a permissive default policy into the rule set
// itself, as a catch-all in last position of the runtime's precedence: allow
// loses to any drop or reject the set states, which is exactly what a default
// is. The binding's default actions say the same thing on bridged NICs, but an
// OVN NIC cannot take those keys without being re-plugged (and losing its
// address), so the rule set is the one place the policy can live everywhere.
//
// allow is the pack's own permissive verb — "allow" for a stateful group,
// "allow-stateless" where the provider declares statelessness — because
// silently turning a stateless group's openness back into a stateful one is a
// difference a client can measure.
func (s FirewallSpec) WithPermissiveCatchAll(allow string) FirewallSpec {
	if s.DefaultIngress == "allow" {
		s.Rules = append(s.Rules, FirewallRule{Direction: "ingress", Action: allow})
	}
	if s.DefaultEgress == "allow" {
		s.Rules = append(s.Rules, FirewallRule{Direction: "egress", Action: allow})
	}
	return s
}

// SyncRuleSet writes one rule set to the runtime, and reports whether it can
// now be applied. A failure is logged and never fails the control plane: the
// API still serves the group, which is the honest degraded state, and this log
// is the only place an operator learns the rules exist nowhere.
func (b Binding) SyncRuleSet(ctx context.Context, fw firewaller, spec FirewallSpec) bool {
	if fw == nil {
		return false
	}
	if err := fw.EnsureFirewall(ctx, spec); err != nil {
		b.logger().Error("could not write the firewall rules",
			"provider", b.Provider, "firewall", spec.Name, "error", err)
		return false
	}
	return true
}

// ApplyRuleSets attaches rule sets to one machine's interfaces — every set the
// machine's server carries, in one call, because the runtime replaces the
// attachment list rather than merging it. Sets that enforce nothing are left
// out (EnforcesNothing says why), and a machine whose every set is permissive
// is explicitly detached rather than skipped, so a group emptied of its rules
// really releases the machine.
//
// The combined default is deny-dominant: one set that drops what no rule
// matches keeps dropping it whatever a more permissive neighbour says, which
// is the semantics every provider here documents for stacked groups.
//
// unfiltered names the machine's networks this provider's groups do not cover
// (#574). It travels beside the sets rather than being applied here, because
// the decision is per interface and only the driver knows which interface sits
// on which network.
func (b Binding) ApplyRuleSets(ctx context.Context, fw firewaller, machine string, unfiltered []string, specs ...FirewallSpec) {
	if fw == nil || machine == "" {
		return
	}
	binding := FirewallBinding{DefaultIngress: "allow", DefaultEgress: "allow", Unfiltered: unfiltered}
	for _, spec := range specs {
		if spec.EnforcesNothing() {
			continue
		}
		binding.Names = append(binding.Names, spec.Name)
		if spec.DefaultIngress != "allow" {
			binding.DefaultIngress = "drop"
		}
		if spec.DefaultEgress != "allow" {
			binding.DefaultEgress = "drop"
		}
	}
	if len(binding.Names) == 0 {
		// The scope is a property of the machine, not of the sets it happens
		// to wear, so the detach carries it too. Nothing in the driver turns
		// on it here — an empty name list already leaves every interface bare
		// — but a binding that dropped it would describe one machine two ways
		// depending on how many groups it wore, and that difference would
		// reach the recorder the packs are compared through.
		binding = FirewallBinding{Unfiltered: unfiltered}
	}
	if err := fw.ApplyFirewall(ctx, machine, binding); err != nil {
		b.reportFirewall(err, "machine", machine)
	}
}

// reportFirewall logs a failed application at the level it deserves. A rule
// set the runtime has no mechanism for — a routed NIC (#337) — is a declared
// limit, not an operational failure: /_feint/health publishes it as
// capabilities.firewall_public_only=false and docs/limits.md carries the
// measurement, so the warning names the declaration instead of crying wolf.
// Anything else stays an error, because nothing declared it.
//
// The sentence used to say "this machine's public-only interface", and #548
// measured that as false on the shape it fires on most: a Scaleway server
// created with its flexible IP has a routed eth0 *and* a private eth1 on a
// managed network carrying the rule set, so the machine is not public-only and
// an operator reading the line concluded the wrong machine was concerned. What
// the runtime cannot filter is the *interface*, whatever else the machine
// carries; the error the driver raises names it and the addresses it delivers.
//
// TestAnUnenforceableGroupIsReportedAsTheDeclaredLimit fails without the
// distinction, and TestTheUnenforceableWarningDoesNotCallTheMachinePublicOnly
// fails without the subject.
func (b Binding) reportFirewall(err error, keyvals ...any) {
	keyvals = append(keyvals, "provider", b.Provider, "error", err)
	if errors.Is(err, ErrFirewallUnenforceable) {
		b.logger().Warn("the security group is not enforced on this machine's routed interface, "+
			"which carries a published address and takes no rule set — the runtime declares it "+
			"(capabilities.firewall_public_only=false, docs/limits.md); the error names the "+
			"interface and the address that answers uncovered",
			keyvals...)
		return
	}
	b.logger().Error("could not apply the firewall to a machine", keyvals...)
}

// DropRuleSet removes a rule set whose group is going. Failure is logged
// rather than fatal: the group is already gone from the control plane, and
// refusing the delete afterwards would leave the client with a resource it
// cannot remove.
func (b Binding) DropRuleSet(ctx context.Context, fw firewaller, name string) {
	if fw == nil {
		return
	}
	if err := fw.RemoveFirewall(ctx, name); err != nil {
		b.logger().Error("could not remove the firewall rules",
			"provider", b.Provider, "firewall", name, "error", err)
	}
}
