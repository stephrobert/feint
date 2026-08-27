package machine

import (
	"context"
	"errors"
	"slices"
)

// ErrFirewallUnenforceable reports an interface the runtime has no mechanism
// to enforce a rule set on — as opposed to an interface where enforcement was
// attempted and failed. A routed NIC is the measured case (#337): every
// security option is an invalid device option there, on Incus 7.2 and 7.3
// alike, so a rule set bound to such a machine can only be refused.
//
// Typed so a pack can tell the two apart: a declared limit is reported against
// the matching capability (Capabilities.FirewallPublicOnly, false for the
// Incus driver) and docs/limits.md, where a real failure stays an error. What
// no caller may do with it is answer as if the rules were enforced — the
// half-applied ERROR-then-200 is the exact divergence #337 removed.
var ErrFirewallUnenforceable = errors.New("the runtime has no mechanism to enforce a rule set on this interface")

// Firewalls are what makes an emulated security group more than documentation.
//
// Every local cloud emulator stores rules and filters nothing. MiniStack says so
// plainly ("security group rules are stored but never filter traffic"), and
// floci publishes host ports through socat sidecars while admitting "the source
// CIDR value itself is not enforced". A test that asserts a port is closed
// therefore proves nothing anywhere.
//
// The contract here is deliberately narrow: describe a rule set, hand it to the
// runtime, attach it to a machine. What the runtime does with it is its own
// business, and the emulator reports what it could not do rather than pretending.

// FirewallRule is one rule of an emulated security group.
//
// The field names follow the direction they filter: Source matters on the way
// in, Destination on the way out. Both are CIDR blocks. A rule that names a
// group of machines rather than a block has to be expanded by the caller, since
// bridge-backed runtimes have no selector for it.
type FirewallRule struct {
	// Direction is "ingress" or "egress".
	Direction string
	// Action is "allow", "drop" or "reject". Drop is silent, reject answers.
	Action string
	// Protocol is "tcp", "udp", an ICMP spelling, or empty for any.
	//
	// ICMP does not name an address family here, and a driver must not read one
	// into it: "icmp" and "icmp4" are both the family-agnostic spelling, because
	// "icmp4" is the runtime's wire name for the IPv4 protocol rather than a
	// claim any pack makes — Scaleway's only value is "ICMP". A driver picks the
	// family from Source and Destination, and the explicit v6 spellings
	// ("icmp6", "icmpv6", "ipv6-icmp") are the ones that fix it. Reading the
	// name alone is #454: an ICMP rule sourced from an IPv6 block was written as
	// the IPv4 protocol and the daemon refused the group's whole rule set.
	Protocol string
	// Source is the block traffic comes from, for an ingress rule.
	Source string
	// Destination is the block traffic goes to, for an egress rule.
	Destination string
	// PortFrom and PortTo bound a TCP or UDP range. Zero means every port.
	PortFrom, PortTo int
}

// FirewallSpec is a named rule set, the runtime-side shape of a security group.
type FirewallSpec struct {
	// Name identifies the rule set; it must be unique and shell-safe.
	Name string
	// DefaultIngress and DefaultEgress are what happens to traffic no rule
	// matches: "allow" or "drop". A security group carries exactly this, which
	// is why it is not derived from the rules.
	DefaultIngress, DefaultEgress string
	// Rules are evaluated by the runtime, which orders them by action rather
	// than by position: an emulator that relied on list order would report an
	// order the runtime does not honour.
	Rules []FirewallRule
}

// FirewallBinding is what a machine's interfaces enforce: the rule sets, and
// what happens to traffic none of them matches.
//
// The defaults are carried explicitly because a security group states them, and
// because runtimes disagree: an Incus NIC denies by default once any rule set is
// attached, which is right for a restrictive group and wrong for a permissive
// one.
type FirewallBinding struct {
	Names                         []string
	DefaultIngress, DefaultEgress string
	// Unfiltered names the networks whose interfaces carry no rule set at all,
	// because the pack declared them outside what its security groups cover
	// (Attachment.Unfiltered, #574). An interface on one of them is bound as
	// if the machine wore no group: never the names above, never the default
	// actions.
	//
	// Carried per network rather than per device because a pack declares
	// networks and never sees a device name — the runtime picks those, and
	// which ethN a membership lands on depends on what the launch already
	// used.
	Unfiltered []string
}

// covers reports whether the rule sets of this binding reach an interface
// sitting on the named network.
//
// A device on no network — a routed NIC — is covered as far as this question
// goes; what happens to it is ApplyFirewall's own refusal (#337), which is a
// statement about the runtime rather than about the provider's model.
func (b FirewallBinding) covers(network string) bool {
	return network == "" || !slices.Contains(b.Unfiltered, network)
}

// firewaller is the optional half of a driver: a runtime that can enforce
// rules implements it, one that cannot does not, and the pack degrades to
// serving the rules as metadata.
//
// Kept separate from the driver on purpose. Enforcement is the one capability
// whose absence a user has to be told about, and a separate interface makes
// that absence a compile-time fact rather than a silent no-op.
type firewaller interface {
	// EnsureFirewall creates or replaces the rule set as a whole. Replacing
	// rather than patching is what makes a rule removed upstream disappear here
	// instead of lingering.
	EnsureFirewall(ctx context.Context, spec FirewallSpec) error
	// ApplyFirewall attaches rule sets to every interface of a machine the
	// binding covers, and detaches everything when the binding names none. An
	// interface on one of the binding's Unfiltered networks is treated as if
	// the machine wore no group at all.
	ApplyFirewall(ctx context.Context, machine string, binding FirewallBinding) error
	// RemoveFirewall deletes the rule set. It must succeed when nothing is
	// there, and it must fail when the set is still attached rather than
	// leaving machines with a dangling reference.
	RemoveFirewall(ctx context.Context, name string) error
}
