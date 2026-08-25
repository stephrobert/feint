package machine

// What a runtime mode can actually deliver, declared rather than assumed.
//
// This exists because the difference between the modes was recorded only in
// docs/limits.md — that is, nowhere at the moment it matters. An operator
// running `--vm incus` and reading a README that says "a security group closes a
// port for real" has no way, at runtime, to learn that subnet isolation is the
// one thing that mode cannot give them. They find out when a test that should
// fail passes.
//
// The emulator's whole argument is that it does not claim what it has not
// measured. A capability list is that argument applied to itself: /_feint/health
// says what this process can prove, and a conformance suite can gate on it
// instead of hardcoding a mode name.

// Capabilities is what a machine runtime delivers. Every field is a claim the
// conformance suites can check, not a feature flag.
type Capabilities struct {
	// Machines: a created server becomes a machine that actually runs.
	Machines bool `json:"machines"`
	// Addresses: the address the API publishes is the address the machine
	// carries, on a bridge holding the block the client asked for.
	Addresses bool `json:"addresses"`
	// Firewall: a security group's rules are enforced on the machine's
	// interface, and a change applies without restarting it.
	Firewall bool `json:"firewall"`
	// FirewallPublicOnly: the rules are enforced even on a machine that joins
	// no emulated network — a server whose only interface is a routed NIC
	// carrying the public addresses its API publishes (#202).
	//
	// Declared apart from Firewall because the two were measured apart (#337).
	// On Incus, the same group that closes a port for real on a machine with
	// an emulated network under it — public address included, since a flexible
	// IP is routed through the filtered NIC — attaches to nothing on a routed
	// NIC: every security option is an invalid device option there, on 7.2 and
	// 7.3 alike. A suite probing the ports of a public-only machine gates on
	// this claim, never on capabilities.firewall.
	FirewallPublicOnly bool `json:"firewall_public_only"`
	// Isolation: two networks of two different VPCs cannot reach each other.
	//
	// This is the one that separates the modes. Managed bridges on one host are
	// routed directly between each other — the runtime documents it — so the
	// bridge mode cannot deliver it, and two measured attempts to work around it
	// did not hold once the interfaces carried a security group of their own. An
	// OVN network is a logical network with its own router, so the separation is
	// the topology's rather than a rule's.
	Isolation bool `json:"isolation"`
	// OwnKernel: the machine boots its own kernel, so a test touching sysctl,
	// kernel modules or the boot path measures something.
	OwnKernel bool `json:"own_kernel"`
	// Balancing: a load balancer distributes real connections across its
	// backends, for clients inside the network it sits in — and across backends
	// of that same network.
	//
	// Both bounds name the thing measured and neither is a hedge
	// (#315, #457, internal/core/machine/balancer.go).
	//
	// The address. One of the network's own block — which is what an *internal*
	// load balancer's address is — balanced 6/6 connections across two machines
	// at t0, t+60s and t+180s. An address outside it, delegated through the
	// uplink, answered for two minutes and went dark for ever, because the
	// runtime announces such an address once at creation and never again. So
	// this claim covers the internal form and says nothing about the
	// internet-facing one, whose public address stays a TEST-NET address routed
	// nowhere on purpose.
	//
	// The backends. Same block, and the second half was measured on 2026-08-25
	// rather than assumed: the runtime refuses a backend outside the balancer's
	// own subnet, peering the two networks does not relax it, and the placement
	// that would serve the ordinary two-tier shape — a public balancer in front
	// of private machines — is refused on its listen address instead. So this
	// claim says nothing about a balancer whose machines are on another subnet:
	// EnsureBalancer refuses that shape by name and docs/limits.md carries the
	// measurements.
	//
	// A true here is therefore a claim about a shape, not about every balancer
	// the API will accept. What withdraws it entirely is the host refusing a
	// write this driver had accepted — balancerRefused, the twin of the rule
	// #454 wrote for the firewall.
	Balancing bool `json:"balancing"`
	// PrivateFromHost: an address inside an emulated subnet answers from the
	// host that runs the emulator, not only from the other machines.
	//
	// This is the second claim that separates the modes, and it is isolation's
	// mirror image. A managed bridge is an interface of the host, so the host
	// reaches every machine on it directly. An OVN network sits behind its own
	// router with SNAT towards the uplink: a connection from the host to an
	// internal address goes in unmapped and the reply comes back source-NATed
	// to the router's uplink address, so the handshake never completes —
	// measured, sshd up and answering its neighbours while the host read the
	// port as closed. Only an externally routed public address (l2proxy)
	// answers the host there, which is why the Scaleway ssh chain holds in
	// both modes and a chain reading a subnet-internal address holds in one.
	PrivateFromHost bool `json:"private_from_host"`
}

// Capable is implemented by a driver that declares what it delivers. It is an
// optional interface, like Firewaller and Pruner: a driver that does not
// implement it is read through CapabilitiesOf below, which assumes nothing.
type Capable interface {
	Capabilities() Capabilities
}

// CapabilitiesOf reports what a driver delivers, and claims nothing for a driver
// that does not say.
//
// The zero value is the safe answer: an undeclared capability reads as absent,
// so a suite gating on one skips rather than asserting something nobody
// promised. A driver that gains a capability declares it; nothing here guesses
// from a type name.
func CapabilitiesOf(d Driver) Capabilities {
	if c := Declared(d); c != nil {
		return *c
	}
	return Capabilities{}
}

// Declared returns what a driver says it delivers, or nil when it says nothing.
//
// CapabilitiesOf cannot express that difference and does not need to: a suite
// gating on isolation wants a bool, and absent is the safe answer. A reader
// does need it. On the wire, a driver that declares every capability false and
// a driver that declares nothing are the same object of five falses, so a page
// showing "no" for both would state, on behalf of a driver that never spoke,
// something nobody promised — which is the exact half-truth "une capacité non
// déclarée vaut absente" exists to prevent, inverted.
//
// So /_feint/health answers null rather than an object when nothing was
// declared, and the page prints "not declared".
// TestAnUndeclaredDriverIsNotTheSameAsOneThatDeclaresNothing in
// internal/core/emulator fails without this.
func Declared(d Driver) *Capabilities {
	c, ok := d.(Capable)
	if !ok {
		return nil
	}
	caps := c.Capabilities()
	return &caps
}

// Capabilities implements Capable for the no-op driver: it runs nothing, and
// says so. The control plane still answers, which is the documented degraded
// mode and what CI runs — but no claim about a machine survives it.
func (Noop) Capabilities() Capabilities { return Capabilities{} }

// Capabilities implements Capable for the Incus driver.
//
// Isolation follows OVN and nothing else. Addresses and the firewall hold in
// every mode: both were measured on bridges, and the OVN mode carries them with
// three documented behavioural differences rather than losing them.
// PrivateFromHost is isolation's trade, not an accident: the same router that
// separates two VPC's subnets by construction NATs the host away from their
// insides, so the two claims cannot both be true of one Incus mode.
//
// Balancing follows OVN too, and for a plainer reason than isolation's: a
// managed bridge has no load balancer primitive at all, so there is nothing to
// measure there rather than something that half works.
func (d *Incus) Capabilities() Capabilities {
	caps := d.declaredCapabilities()
	// And what the host *refused* wins over both (#454). A rule set the daemon
	// rejected is the host saying, in the plainest way it has, that this
	// process does not enforce what its API describes; going on publishing
	// firewall=true afterwards is the lying 200 the whole project exists to
	// refuse, one layer down. See firewallRefused for why it is one-way and why
	// only a refusal by the host counts.
	// TestAFirewallWriteTheHostRefusesWithdrawsTheCapability fails without this.
	if d.firewallDenied.Load() {
		caps.Firewall = false
		caps.FirewallPublicOnly = false
	}
	// And the same rule for the balancer (#457). A load balancer the daemon
	// rejected after this driver had accepted it is the host saying this process
	// does not distribute what its API describes; balancing=true afterwards is
	// the same lying 200, one plane over.
	// TestABalancerWriteTheHostRefusesWithdrawsTheCapability fails without this.
	if d.balancerDenied.Load() {
		caps.Balancing = false
	}
	return caps
}

// declaredCapabilities is what the flags promise, narrowed by what the host
// answered at startup.
func (d *Incus) declaredCapabilities() Capabilities {
	// What the host answered wins over what the flag promised (#181). Verify
	// narrows this set once at startup; before that, and in a test that builds a
	// driver directly, the declaration below is what there is.
	if d.verified != nil {
		return *d.verified
	}
	return Capabilities{
		Machines:  true,
		Addresses: true,
		Firewall:  true,
		// False in every Incus mode, and measured rather than pending: a
		// routed NIC accepts no security option at all (#337, Incus 7.2 and
		// 7.3), so ApplyFirewall refuses a rule set on a public-only machine
		// instead of pretending. The claim goes true the day a mechanism
		// exists and is measured, not before.
		FirewallPublicOnly: false,
		Isolation:          d.OVN,
		Balancing:          d.OVN,
		OwnKernel:          d.VM,
		PrivateFromHost:    !d.OVN,
	}
}
