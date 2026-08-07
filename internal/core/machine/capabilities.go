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
func (d *Incus) Capabilities() Capabilities {
	return Capabilities{
		Machines:  true,
		Addresses: true,
		Firewall:  true,
		Isolation: d.OVN,
		OwnKernel: d.VM,
	}
}
