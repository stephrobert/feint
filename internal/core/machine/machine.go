// Package machine turns an emulated server into something that actually runs.
//
// A control-plane-only emulator answers "state: running" and nothing happens.
// That is enough for Terraform, and not enough for anyone who wants to ssh in,
// run a provisioner, or test a userdata script. So a powered-on server can be
// backed by a real machine, and the emulated network it sits on can be a real
// network, with the address the API published.
//
// The driver is deliberately small: start, stop, remove, inspect, plus the two
// calls that give a subnet a backing network. Everything a provider pack needs,
// nothing that ties the core to a particular runtime.
//
// Incus is the runtime this contract is built for. Docker was supported and was
// removed: see docs/limits.md for the measurements behind that decision.
package machine

import "context"

// NetworkSpec describes the backing network an emulated subnet needs.
//
// This is what separates a declared addressing plan from a real one. Given a
// network per emulated subnet, a machine gets an address that belongs to the
// block the client asked for, and two subnets are isolated because they are two
// networks, not because a rule says so.
type NetworkSpec struct {
	// Name is the runtime-side network name; it must be unique, shell-safe and
	// at most MaxNetworkNameLen long, because it becomes a host interface name.
	// NetworkName derives one from a resource ID.
	Name string
	// CIDR is the block, in canonical form: "10.0.0.0/24".
	CIDR string
	// Gateway is the address the runtime answers on inside the block. Empty
	// lets the runtime pick one, which no pack should rely on: the gateway is
	// an address the emulated subnet must account for.
	Gateway string
	// NAT gives machines on the network outbound access through the host. A
	// subnet with a route to an internet gateway wants it; an isolated one
	// does not.
	NAT bool
	// Labels are attached to the network so orphans can be found and cleaned.
	Labels map[string]string
}

// Attachment places a machine on a network at a chosen address.
type Attachment struct {
	// Network names a network passed to EnsureNetwork.
	Network string
	// Address is the fixed address to assign. Empty lets the runtime's DHCP
	// answer, which is only acceptable when the pack does not publish an
	// address of its own.
	Address string
	// PrefixLen is the mask of the block Address belongs to. It is needed
	// because reserving an address on the bridge is not the same as the guest
	// carrying it: a NIC added to a running machine has no DHCP client on it,
	// so the driver configures the interface inside the guest, and that needs a
	// mask. Zero means "leave the guest alone".
	PrefixLen int
	// Secondary are the extra addresses the interface carries beside Address,
	// which Outscale calls secondary private IPs and every cloud has some form
	// of. They share Address's mask.
	//
	// Reconciled rather than appended: the list is what the interface must end
	// up carrying, so an address dropped from it is removed from the guest. A
	// pack that only ever added would leave an unlinked address on the machine
	// while its API said it was gone, which is the shape of defect #202 exists
	// to prevent — an address nothing publishes.
	Secondary []string
	// Unfiltered declares that this provider's security groups do not cover
	// the interface this attachment creates, so no rule set is ever bound to
	// it.
	//
	// Which interfaces a security group covers is a provider fact, like an
	// address key or a login, and it is not the same fact on the three clouds
	// (#574). Exoscale states it plainly — "Security group rules do not apply
	// to traffic inside private networks" — while Scaleway and Outscale do
	// filter their private interfaces, and their network suites assert it.
	//
	// So the zero value is "covered": a pack that says nothing keeps every
	// interface filtered, which is the direction that cannot silently open a
	// machine. A pack that sets it declares a divergence it would otherwise
	// have shipped, and it is read once, by GroupSync, on its way to the
	// binding the driver applies.
	//
	// internal/core/machine's TestNoRuleSetIsBoundToAnUnfilteredInterface and
	// internal/providers/exoscale's
	// TestALateNetworkAttachLeavesThePrivateInterfaceUnfiltered fail without
	// it.
	Unfiltered bool
}

// Spec describes the machine a provider pack wants.
type Spec struct {
	// Name is the machine name; it must be unique and shell-safe.
	Name string
	// Image is the OCI or runtime image standing in for the cloud image.
	Image string
	// Command keeps the machine alive when the image would exit immediately.
	Command []string
	// Labels are attached to the machine so orphans can be found and cleaned.
	Labels map[string]string
	// Env is passed to the machine, e.g. rendered userdata.
	Env []string
	// User is the login the cloud provisions by default: root on Scaleway,
	// outscale on Outscale, the template's own user on Exoscale.
	User string
	// AuthorizedKeys are the public keys to install for User.
	AuthorizedKeys []string
	// CloudInit is a rendered #cloud-config. Drivers that support it use it
	// verbatim; the others fall back to User and AuthorizedKeys.
	CloudInit string
	// Attachments place the machine on emulated networks. The first one is the
	// primary interface. Empty puts the machine on the driver's own default
	// network, never on the operator's: a machine on a network the emulator did
	// not create cannot lawfully carry a routed public address, because every
	// route the driver writes is refused outside its own networks (mustOwn).
	Attachments []Attachment
	// PublicAddresses are the emulated public addresses this machine must
	// answer on from its first boot, each routed to its primary interface.
	//
	// They ride the launch rather than a later edit because editing route keys
	// on a live OVN NIC re-plugs the device, and the re-plug costs the guest its
	// DHCP lease with nothing left to renew it — measured on Incus 7.2: the
	// machine then holds no address at all, which is worse than an address
	// nothing routes. An address attached after boot still goes through
	// Router.RouteAddress, and pays the bounce documented in docs/limits.md.
	PublicAddresses []string
}

// Machine is a running (or stopped) backing machine.
type Machine struct {
	ID      string
	Name    string
	IP      string
	Running bool
}

// Driver runs machines and the networks they sit on. Implementations must be
// safe for concurrent use.
type Driver interface {
	// Name identifies the driver in logs and in the health endpoint.
	Name() string
	// Available reports whether the driver can actually run anything. The
	// emulator degrades to metadata-only rather than failing when it cannot.
	Available(ctx context.Context) bool
	// Start creates and starts the machine, or starts it again if it exists.
	Start(ctx context.Context, spec Spec) (Machine, error)
	// Stop stops the machine without destroying it.
	Stop(ctx context.Context, name string) error
	// Remove destroys the machine. It must succeed when nothing is there.
	Remove(ctx context.Context, name string) error
	// Inspect returns the current state; ok is false when the machine is gone.
	Inspect(ctx context.Context, name string) (m Machine, ok bool, err error)
	// EnsureNetwork creates the backing network, and succeeds when it already
	// exists: a pack calls it on every attachment, not once per subnet.
	EnsureNetwork(ctx context.Context, spec NetworkSpec) error
	// Attach puts an existing machine on a network at the given address. A
	// cloud attaches an interface to a running server, so this cannot be
	// limited to what Spec.Attachments carries at start time.
	Attach(ctx context.Context, name string, att Attachment) error
	// Detach takes the machine off the network again. It is the counterpart of
	// Attach, and it is required rather than optional for the reason #426
	// measured: without it a pack can only forget an interface in its store,
	// the device stays on the container, and RemoveNetwork then refuses with
	// "The network is currently in use" for a machine the control plane says
	// is no longer attached. It must succeed when the device, the machine or
	// the network is already gone, because a delete path runs twice more often
	// than anyone expects.
	Detach(ctx context.Context, name, network string) error
	// RemoveNetwork destroys it. It must succeed when nothing is there, and it
	// must fail when machines are still attached rather than cut them off.
	RemoveNetwork(ctx context.Context, name string) error
}

// Waiter is the optional half of a Driver that can tell when a machine is
// ready. Start never waits, because an API call must not block for the tens of
// seconds a virtual machine takes to boot; a caller that needs to reach the
// machine asks here instead.
type Waiter interface {
	WaitRunning(ctx context.Context, name string) (Machine, error)
}

// Noop is the metadata-only driver: servers change state, nothing runs.
//
// It is the default, and it is what CI uses: conformance suites must not depend
// on a machine runtime being present.
type Noop struct{}

// Name implements Driver.
func (Noop) Name() string { return "none" }

// Available implements Driver.
func (Noop) Available(context.Context) bool { return true }

// Start implements Driver.
func (Noop) Start(_ context.Context, spec Spec) (Machine, error) {
	return Machine{ID: "", Name: spec.Name, Running: true}, nil
}

// Stop implements Driver.
func (Noop) Stop(context.Context, string) error { return nil }

// Remove implements Driver.
func (Noop) Remove(context.Context, string) error { return nil }

// Inspect implements Driver.
func (Noop) Inspect(_ context.Context, name string) (Machine, bool, error) {
	return Machine{Name: name}, false, nil
}

// EnsureNetwork implements Driver.
func (Noop) EnsureNetwork(context.Context, NetworkSpec) error { return nil }

// Attach implements Driver.
func (Noop) Attach(context.Context, string, Attachment) error { return nil }

// Detach implements Driver.
func (Noop) Detach(context.Context, string, string) error { return nil }

// RemoveNetwork implements Driver.
func (Noop) RemoveNetwork(context.Context, string) error { return nil }
