package machine

import (
	"context"
	"net/netip"

	"github.com/stephrobert/feint/internal/core/resource"
)

// A machine's interface shape is declared once, not choreographed three times
// (#510).
//
// The three providers produce three interface shapes, and those shapes are
// right — they reproduce three real upstream models: interfaces that ride the
// launch, a managed primary fixed at create, a public primary whose private
// memberships always join after boot. What was wrong is that each pack wrote
// the *recipe* by hand — route the promised public addresses, join the private
// networks, resync the firewall, in that order — three spellings, three
// orders, and the ordering constraints alive only in per-pack comments. The
// order route → attach → firewall is a property of the runtime, not of a
// provider, and a comment is not a control.
//
// So the pack declares its Plan and the Reconciler executes the one order.
// What stays in the pack is everything provider-shaped: which stored fields
// become which attachment, which addresses were promised, and the wire dialect
// around it all. No provider name enters this file; internal/cli's
// TestTheCoreNamesNoProvider remains the judge.

// Plan is one machine's declared interface shape.
type Plan struct {
	// Boot are the interfaces that ride the launch; the first is the primary.
	// Carried on the Start rather than attached afterwards because editing a
	// live OVN NIC re-plugs it and the guest loses its lease — Boot.Attachments
	// says the rest.
	Boot []Attachment
	// Memberships are the networks joined after boot, one Attach each, and
	// never promoted to the primary the way a Boot attachment would be: on the
	// provider that declares them, the primary is the public interface.
	Memberships []Attachment
	// Publics are the emulated public addresses promised to the machine — the
	// launch installs their host half, the post-boot replay hands the guest
	// its routes and repairs a machine that already existed.
	Publics []string
	// RouteVia names the network public routes ride, empty to let the driver
	// pick. One pack routes on the network its server lives on and says so;
	// the two others let the driver pick, and changing that would change what
	// the runtime is asked without an upstream difference demanding it.
	RouteVia string
}

// Reconciler executes a declared Plan in the one order — addresses, then
// memberships, the firewall last, so the expansion sees every interface. It is
// the second orchestrator of the driver contract, beside GroupSync, whose
// firewall step it consumes.
type Reconciler struct {
	// Groups is the firewall step (#509); its Binding is the spine everything
	// below rides on.
	Groups GroupSync
	// PlanOf builds the machine's declared interface shape from the resource —
	// the pack's own field walks, nothing else.
	PlanOf func(res *resource.Resource) Plan
	// Settle, optional, is the pack's bookkeeping between the start and the
	// replay: what the boot produced, recorded in the pack's own attributes
	// before the firewall expansion reads them. One pack keeps the machine's
	// private address in an API field the others do not declare; forcing that
	// into the shared sequence would invent a field, so it is a hook.
	Settle func(res *resource.Resource)
	// PublicBlock is the emulated public range this pack may route. It guards
	// every address on its way to the driver, routing and unrouting alike: a
	// stored address is untrusted input — PUT /_feint/state and snapshot load
	// restore it verbatim — and routing an arbitrary value would send the
	// host's traffic for that address into a container. On the Reconciler and
	// not on the Plan, because the unroute of a machine already gone has no
	// resource left to plan from.
	PublicBlock netip.Prefix
}

func (r Reconciler) binding() Binding { return r.Groups.Binding }

// plan asks the pack for the machine's declared interface shape, and reports
// the pack rather than dereferencing a nil field (#543).
//
// PlanOf is required and has no honest empty meaning: a pack with no networks
// at all still declares that, as `Plan{}`, in one line. So this refuses where
// GroupSync degrades, and the asymmetry is deliberate. A missing firewall
// translation costs the rules; a missing plan costs the boot's first two steps
// — the promised addresses and the memberships — and a machine started without
// them is a machine on no network that the API would describe as running. That
// is the lie #484 named. Refusing publishes FailedState through the pack's own
// vocabulary instead.
//
// Unlike GroupSync's omission this one is not mode-dependent: measured on
// 2026-08-27, a nil PlanOf panics under machine.Noop exactly as it does under
// an enforcing runtime, because nothing gates the call. A pack that forgets it
// therefore fails its own first unit test, which is why the sentence matters
// more than the timing here.
//
// internal/cli's TestABootWithNoDeclaredPlanIsRefusedRatherThanPanicking fails
// without this.
func (r Reconciler) plan(res *resource.Resource) (Plan, bool) {
	if r.PlanOf == nil {
		r.binding().logger().Error("this pack declares no interface plan, so nothing can be started for it",
			"provider", r.binding().Provider, "resource", res.ID,
			"error", "the pack builds a machine.Reconciler without PlanOf: every pack declares the "+
				"shape of its machines' interfaces, and a pack with none declares that as an empty Plan")
		return Plan{}, false
	}
	return r.PlanOf(res), true
}

// router is the runtime's routing half, nil when it has none — the assertion
// every pack wrote for itself, now inside the layer.
func (r Reconciler) router() Router {
	router, _ := r.binding().driver.(Router)
	return router
}

// PowerOn starts the machine on its declared plan and replays the post-boot
// order: the promised addresses, the memberships, the firewall last. It
// reports what PowerOn reported; a machine that did not start is not replayed
// onto, and the state the pack publishes is the one the effect produced.
func (r Reconciler) PowerOn(ctx context.Context, res *resource.Resource, boot Boot) bool {
	plan, declared := r.plan(res)
	if !declared {
		return false
	}
	boot.Attachments = plan.Boot
	boot.PublicAddresses = plan.Publics
	if !r.binding().PowerOn(ctx, res, boot) {
		return false
	}
	if r.Settle != nil {
		r.Settle(res)
	}
	// The launch installed the host half of every public route; this hands
	// the guest its addresses, and repairs a machine that already existed.
	// Idempotent, like everything in the replay.
	for _, address := range plan.Publics {
		r.route(ctx, res, plan, address)
	}
	// The memberships come back as extra interfaces, after the boot rather
	// than on it. Attach is idempotent by network, so a machine that kept its
	// devices across a stop is repaired, not doubled. A refusal is already in
	// the log and must not stop the replay: the remaining memberships and the
	// firewall still describe what the store says.
	for _, m := range plan.Memberships {
		_ = r.attach(ctx, res, m)
	}
	// The firewall last, so the expansion sees every address and every
	// interface the two loops above delivered. This line is the order the
	// packs kept in comments; TestTheReplayRoutesThenJoinsThenAppliesTheFirewall
	// is what holds it now.
	r.Groups.AfterBoot(ctx, res)
	return true
}

// Reboot takes the machine down and brings it back up on its declared plan.
//
// It exists because the sequence was written three times and one copy forgot
// half of it. Outscale's RebootVms and Exoscale's reboot-instance both said "a
// reboot is a stop then a start"; Scaleway's action filed reboot with poweron
// and called the start alone, under a comment stating that the reboot case was
// handled — the exact shape CLAUDE.md names, a comment standing where a control
// belongs. The runtime refuses to relaunch a name it has already served, so the
// measured outcome on 2026-08-27 was an action answered `success`, an API
// reporting `running`, and a machine with the same container pid, an uptime
// still climbing and a transient marker unit still alive (#547).
//
// Worse than the usage cost was the testing one: any assertion of the form
// "the service survives a restart" wired through that action could not fail.
//
// What it publishes is what the effect produced, never what the request asked
// for: PowerOn writes FailedState when the runtime did not deliver, so a reboot
// the runtime refuses leaves a server a client can read as down, in that
// provider's own vocabulary. That is #484's form, kept rather than reinvented.
//
// TestARebootStopsTheMachineBeforeStartingIt fails without this, and so does
// internal/providers' TestSameIntentSameRuntimeSequenceAcrossPacks, where the
// reboot intent is now one of the compared ones: the three packs must ask the
// runtime for the same gestures, and before this they did not.
func (r Reconciler) Reboot(ctx context.Context, res *resource.Resource, boot Boot) bool {
	// A machine that is not up has nothing to take down, and asking the runtime
	// to stop a stopped instance logs a failure for an ordinary case. Which
	// word means "up" is the pack's, held once here rather than compared to a
	// literal in three places.
	if res.State == r.binding().RunningState {
		r.binding().PowerOff(ctx, res)
	}
	return r.PowerOn(ctx, res, boot)
}

// ReplayAddresses re-routes every promised address of a machine that just
// proved reachable — the late-address door: a virtual machine's agent answers
// long after poweron returned, and the guest half of its routes can only land
// then. Idempotent for a machine already served.
func (r Reconciler) ReplayAddresses(ctx context.Context, res *resource.Resource) {
	plan, declared := r.plan(res)
	if !declared {
		return
	}
	for _, address := range plan.Publics {
		r.route(ctx, res, plan, address)
	}
}

// Route makes one public address reach the machine, the hot half: an address
// attached to a running machine. The block guard and the router assertion are
// the layer's; whether several holders may record the address is the pack's
// control plane, and the runtime carries it on at most one machine either way
// (Binding.RouteAddress says why).
func (r Reconciler) Route(ctx context.Context, res *resource.Resource, address string) {
	plan, declared := r.plan(res)
	if !declared {
		return
	}
	r.route(ctx, res, plan, address)
}

func (r Reconciler) route(ctx context.Context, res *resource.Resource, plan Plan, address string) {
	router := r.router()
	if router == nil {
		return
	}
	name := res.Runtime[r.binding().RuntimeKey]
	if name == "" || address == "" {
		return
	}
	if !r.emulated(address) {
		r.binding().logger().Warn("refusing to route an address outside the emulated public block",
			"provider", r.binding().Provider, "address", address, "resource", res.ID)
		return
	}
	if err := r.binding().RouteAddress(ctx, router, AddressSpec{
		Machine: name,
		Address: address,
		Network: plan.RouteVia,
	}); err != nil {
		r.binding().logger().Error("could not route the public address to the machine",
			"provider", r.binding().Provider, "address", address, "resource", res.ID, "error", err)
	}
}

// Unroute takes a route back. The machine may already be gone — the driver
// treats that as nothing left to undo, and on OVN it still withdraws the
// uplink route, which outlives the machine; that is why this takes the machine
// name rather than a resource.
func (r Reconciler) Unroute(ctx context.Context, machine, address string) {
	router := r.router()
	if router == nil || !r.emulated(address) {
		return
	}
	if err := r.binding().UnrouteAddress(ctx, router, machine, address); err != nil {
		r.binding().logger().Error("could not stop routing the public address",
			"provider", r.binding().Provider, "address", address, "error", err)
	}
}

// PublicAddressOf is what the machine answers on, when that address is one
// this pack could have handed out as a public one — and nothing otherwise.
//
// The binding records one address per machine and gives it no kind: it is
// whatever the runtime answered, read off the first interface in name order.
// Each pack then republishes it under a field whose *name asserts a kind* —
// Exoscale as `public-ip`, Outscale as `PrivateIp` — and neither asked whether
// the recorded address was of that kind. Measured on 2026-08-27 under
// `--vm incus-ovn`: an Exoscale instance created with `public-ip-assignment:
// "none"` and joined to a private network published `"public-ip":
// "10.44.9.10"`, its private-network address, while the machine itself was
// right — one NIC, one address, nothing public (#541). `exo compute instance
// show` prints that as the instance's IP, so an isolated instance reads as
// publicly addressed.
//
// The only fact that settles the kind is the emulated public block, and the
// layer already holds it for the three packs — PublicBlock, the guard every
// address passes on its way to the driver. So the layer answers the question
// too, once, instead of each pack writing half of it: a pack that publishes
// an address as public asks here, and a pack that publishes one as private
// asks PrivateAddressOf. Binding.AddressOf is out of PackSurface for that
// reason — the layer no longer hands a pack an address with no kind on it.
//
// A pack that declares no PublicBlock gets nothing here, which is the safe
// direction: no block means nothing can be shown to be public.
//
// internal/providers/exoscale's TestAnInstanceWithNoPublicIPPublishesNone
// fails without this, and its TestAnInstanceIsGivenAPublicAddressAtCreation
// holds the accepting half.
func (r Reconciler) PublicAddressOf(res *resource.Resource) string {
	address := r.binding().AddressOf(res)
	if !r.emulated(address) {
		return ""
	}
	return address
}

// PrivateAddressOf is the mirror: what the machine answers on, when that
// address is *not* one this pack hands out as public.
//
// It exists because the defect PublicAddressOf closes is symmetric and the
// other half was live too. An Outscale Vm publishes the recorded address as
// PrivateIp, and its plan carries promised public addresses onto the launch:
// the guest then holds two global addresses on one interface, and Inspect
// answers with whichever the runtime lists first. A restart that came back
// with the public one would have published 198.51.100.x as PrivateIp and as
// PublicIp at once. Nothing measured that happening — the pin in
// rememberAddress hides it after the first boot — which is exactly why the
// control belongs in the layer rather than in a pack's memory.
//
// internal/providers/outscale's TestAVmPublishesNoPublicAddressAsItsPrivateOne
// fails without this.
func (r Reconciler) PrivateAddressOf(res *resource.Resource) string {
	address := r.binding().AddressOf(res)
	if address == "" || r.emulated(address) {
		return ""
	}
	return address
}

// emulated reports whether an address is one this pack can have handed out:
// inside PublicBlock. Well-formed is not authorised; this is the authorisation
// half, held once for the three packs.
func (r Reconciler) emulated(address string) bool {
	if !r.PublicBlock.IsValid() {
		return false
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	return r.PublicBlock.Contains(addr)
}

// Join puts a running machine on one more network — the hot half of a
// membership: a cloud attaches a NIC to a server that is running, and that
// cannot fold into a boot-time plan. The interface this attach creates carries
// the machine's rule sets like every other one, so the firewall step runs
// last, attach refused or not: the sets and the store moved even when the
// runtime did not, and the resync is what keeps them one truth.
//
// The attach error is returned for the pack that surfaces it on its resource;
// degrading quietly stays the caller's choice, as everywhere.
func (r Reconciler) Join(ctx context.Context, res *resource.Resource, att Attachment) error {
	err := r.attach(ctx, res, att)
	r.Groups.AfterBoot(ctx, res)
	return err
}

func (r Reconciler) attach(ctx context.Context, res *resource.Resource, att Attachment) error {
	driver := r.binding().driver
	if driver == nil {
		return nil
	}
	name := res.Runtime[r.binding().RuntimeKey]
	if name == "" || att.Network == "" {
		// No machine, or no backing network: the membership is still true in
		// the store, and the interface arrives with the next boot's plan.
		return nil
	}
	if err := driver.Attach(ctx, name, att); err != nil {
		r.binding().logger().Error("could not attach the machine to the network",
			"provider", r.binding().Provider, "resource", res.ID, "network", att.Network,
			"address", att.Address, "error", err)
		return err
	}
	return nil
}

// Leave is the exact undo of Join's attach half. It degrades the same way:
// with no runtime, or a machine that never started, there is nothing to take
// off and the membership has already gone from the store. No firewall resync
// follows, deliberately: no pack ran one here, its callers sit on delete paths
// whose carrier is going next, and adding one belongs to a measured defect,
// not to a move.
func (r Reconciler) Leave(ctx context.Context, res *resource.Resource, network string) {
	driver := r.binding().driver
	if driver == nil {
		return
	}
	name := res.Runtime[r.binding().RuntimeKey]
	if name == "" || network == "" {
		return
	}
	if err := driver.Detach(ctx, name, network); err != nil {
		r.binding().logger().Error("could not detach the machine from the network",
			"provider", r.binding().Provider, "resource", res.ID, "network", network, "error", err)
	}
}

// BackingNetwork is what an emulated subnet needs from the runtime, in the
// pack's terms; EnsureBackingNetwork turns it into the NetworkSpec every pack
// used to build by hand.
type BackingNetwork struct {
	// Key is the Runtime key the network name is recorded under.
	Key string
	// CIDR is the block, canonicalised here so no pack forgets to.
	CIDR netip.Prefix
	// Gateway reserves the block's first host address as the runtime's own,
	// for the packs whose allocator accounts for it. False lets the runtime
	// pick, which the pack must then not publish.
	Gateway bool
	// NAT gives machines outbound access through the host. Deliberately per
	// pack: one provider's subnet reaches the internet only once a gateway
	// service is attached, and switching NAT on for it would make the
	// emulator more permissive than the cloud it imitates.
	NAT bool
	// Marker is the pack's own label key naming the backed resource, so
	// orphans can be swept; the provider label rides beside it under LabelKey,
	// spelled once here instead of three ways.
	Marker string
}

// EnsureBackingNetwork asks the driver for a real network carrying the block,
// and records its name on the resource — after the driver accepted it, which
// is the one ordering that never records a network the runtime refused. Two
// packs wrote the key first and would have kept, on failure, a Runtime entry
// naming a network that does not exist, which the delete path then tries to
// remove.
func (b Binding) EnsureBackingNetwork(ctx context.Context, res *resource.Resource, spec BackingNetwork) error {
	if b.driver == nil {
		return nil
	}
	name := NetworkName(NetworkPrefix, res.ID)
	network := NetworkSpec{
		Name:   name,
		CIDR:   spec.CIDR.Masked().String(),
		NAT:    spec.NAT,
		Labels: map[string]string{LabelKey: b.Provider},
	}
	if spec.Marker != "" {
		network.Labels[spec.Marker] = res.ID
	}
	if spec.Gateway {
		network.Gateway = spec.CIDR.Masked().Addr().Next().String()
	}
	if err := b.driver.EnsureNetwork(ctx, network); err != nil {
		return err
	}
	if res.Runtime == nil {
		res.Runtime = map[string]string{}
	}
	res.Runtime[spec.Key] = name
	return nil
}

// RemoveBackingNetwork removes the backing network and forgets its name on
// success. The error comes back to the pack, which decides its dialect:
// refusing the delete so nothing is reported gone while the runtime still
// holds its block, or logging and proceeding — both exist today and both are
// client-visible surface, so the layer does not pick for them.
func (b Binding) RemoveBackingNetwork(ctx context.Context, res *resource.Resource, key string) error {
	name := res.Runtime[key]
	if b.driver == nil || name == "" {
		return nil
	}
	if err := b.driver.RemoveNetwork(ctx, name); err != nil {
		return err
	}
	delete(res.Runtime, key)
	return nil
}
