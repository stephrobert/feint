package emulator

import "sort"

// Which packs hand work to a capability the driver declares (#180).
//
// `/_feint/health` publishes the machine driver's capabilities, and this
// repository tells a consumer to key on them rather than on a mode name. That
// advice was sound and the answer was not: `Capabilities` describes what the
// *driver* can do, and it is one set for the whole process, while handing rules
// to that driver is something each *pack* does or does not do.
//
// Measured on 2026-08-15: `(*Incus).Capabilities()` declared `firewall: true`
// in every mode, and `internal/providers/scaleway/firewall.go` was the only
// file in any pack that touched `machine.Firewaller`. So an Outscale or
// Exoscale security group was created, echoed back, and reconciled onto
// nothing, while the same process answered `firewall: true`. A user following
// the project's own advice probed a port a deny-default group should close,
// found it open, and had been told the firewall was delivered. A 200 that lies.
//
// Documentation alone could not close that: the claim is read from an endpoint,
// by a program, at the moment it matters. So the endpoint says it.
//
// The shape is a list per capability rather than a per-pack capability set, and
// deliberately: only the capabilities that are provably per-pack belong here,
// and adding one later is additive. A pack that implements nothing appears
// nowhere, which is this project's standing rule — an undeclared capability
// counts as absent, so a check skips instead of asserting what nobody promised.
//
// TestEnforcementNamesOnlyThePacksThatWireIt fails without this.

// FirewallEnforcer is implemented by a pack that hands its security-group rules
// to the machine runtime. Implementing it is a claim the pack's own suite must
// be able to prove; not implementing it is the honest default.
type FirewallEnforcer interface {
	// EnforcesFirewall reports whether this pack reconciles its rules onto the
	// machines that carry them. It answers about the pack, not about the
	// driver: a pack that wires the handoff says true even where the process
	// runs with no runtime at all, because what the user needs to know is
	// whether pointing a runtime at it would change anything.
	EnforcesFirewall() bool
}

// enforcement answers, per capability, the packs that hand work to it. The
// value is what `/_feint/health` publishes under `enforced`.
//
// Absent from a list means one of two things and the caller cannot tell them
// apart, which is intended: the pack does not wire it, or the pack has not said.
// Both are "do not rely on it here".
func enforcement(packs []Pack) map[string][]string {
	out := map[string][]string{}

	// Not `var firewall []string`: a nil slice marshals to `null`, and a
	// consumer iterating the list would break on the very process where no pack
	// wires anything. Found by TestEnforcementPublishesAnEmptyListRatherThanNoKey
	// on its first run.
	firewall := []string{}
	for _, p := range packs {
		fe, ok := p.(FirewallEnforcer)
		if ok && fe.EnforcesFirewall() {
			firewall = append(firewall, p.Name())
		}
	}
	sort.Strings(firewall)
	// The key exists even when empty: a consumer distinguishing "no pack wires
	// it" from "this build does not publish the field" needs the key to be
	// there, and an absent key would read as the older payload.
	out["firewall"] = firewall

	return out
}
