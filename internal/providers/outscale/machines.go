package outscale

import (
	"context"
	"log/slog"

	"github.com/stephrobert/feint/internal/core/cloudinit"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Backing an emulated VM with a real machine is what separates "the API answered
// running" from "something is running".
//
// The sequence lives in machine.Binding, shared with every pack. What is left
// here is only what is Outscale's: its login, its image catalogue, and the names
// it publishes things under. If a line of this file could be written the same
// way for another provider, it belongs in the binding instead.

// binding is this pack's half of the shared machine lifecycle. The prefix is
// what tells an Outscale machine from a Scaleway one on a shared host, for the
// sweep and for the event filter alike.
func (p *Pack) binding() machine.Binding {
	return machine.Binding{
		Driver:   p.env.Machines,
		Provider: Name,
		Prefix:   "feint-osc-",
		User:     DefaultUser,
		// Runtime, never Attrs, so the machine name cannot leak into a response.
		RuntimeKey:   "machine",
		AddressKey:   "address",
		RunningState: stateRunning,
		// Outscale declares no error state for a Vm; stopped is the true one.
		FailedState: stateStopped,
		// The operator's identifier declarations (FEINT_BOOT_IMAGES), consulted
		// by the binding only when the catalogue above resolved nothing.
		Declared: p.env.BootImages,
		Log:      p.env.Log,
	}
}

// DefaultUser is the login Outscale provisions on its images. Their own
// documentation and Terraform examples use it on their OMIs, and getting it
// wrong produces a machine that boots, holds the right key, and refuses every
// login.
const DefaultUser = "outscale"

// logger returns the environment logger, or the default one when a caller built
// an Env by hand without setting it.
func (p *Pack) logger() *slog.Logger {
	if p.env.Log != nil {
		return p.env.Log
	}
	return slog.Default()
}

// imageFor resolves an emulated OMI onto what the machine driver boots, login
// included — one resolution, because the right distribution with the wrong
// login is still a machine nobody can enter.
//
// An unknown identifier resolves to nothing rather than falling back: the
// control plane has accepted it — docs/limits.md declares identifiers
// unchecked, deliberately — and the shared binding refuses the boot out loud
// instead of starting an OS the client never named (#83).
//
// TestOutscaleImageResolutionIsExact fails against the fallback version.
func imageFor(imageID string) (machine.Image, bool) {
	image, known := runtimeImages[imageID]
	return image, known
}

// bootRefusalReason distinguishes, for the refusal log, the two ways an OMI
// resolves to nothing. An identifier nobody ever created is a typo the control
// plane accepted. An image the client registered through CreateImage is the
// more embarrassing case: ReadImages serves it, yet this emulator keeps
// records, not disk contents, so there are no bytes to boot — and booting the
// source's base image instead would silently drop whatever the client baked
// into it, which is the golden-image scenario, the exact place the difference
// matters. Both end in the same refusal; the log must not describe them the
// same way.
//
// TestARegisteredImageRefusesToBootAndSaysWhy fails without the distinction.
func (p *Pack) bootRefusalReason(imageID string) string {
	if _, registered := p.env.Store.Get(Name, kindImage, imageID); registered {
		return "registered by CreateImage, but this emulator keeps records, not disk contents (docs/limits.md)"
	}
	return "the identifier is in no catalogue"
}

// powerOn starts the backing machine and moves the resource to running.
//
// The state is the binding's to set, and it distinguishes two cases that used to
// look alike here. With no runtime, an emulated VM still reaches running: that
// is the documented degraded mode and every client waits for it. With a runtime
// that failed to start the machine, it reaches stopped instead — because a Vm
// reported running while nothing exists is the defect this project is built to
// avoid, not a rounding of it.
func (p *Pack) powerOn(ctx context.Context, res *resource.Resource) {
	imageID, _ := res.Attrs["ImageId"].(string)
	keypair, _ := res.Attrs["KeypairName"].(string)
	userData, _ := res.Attrs["UserData"].(string)
	img, known := imageFor(imageID)
	reason := ""
	if !known {
		reason = p.bootRefusalReason(imageID)
	}

	p.binding().PowerOn(ctx, res, machine.Boot{
		Image:          img.Ref,
		User:           img.User,
		Requested:      imageID,
		Reason:         reason,
		Hostname:       res.ID,
		AuthorizedKeys: p.authorizedKeys(keypair),
		// The Subnet the client asked for, carried onto the machine. Without
		// this the address published as PrivateIp is a number in a store and
		// nothing answers on it — which is the defect this project exists to
		// avoid, not a detail of the API shape.
		Attachments: p.attachmentOf(res),
		// Decoded here, stored encoded: Outscale documents UserData as
		// Base64-encoded, so that is what a read must give back, and what
		// cloud-init must never receive.
		CloudInit: cloudinit.Decode(userData),
		// The public address linked to this Vm, on the launch: a route edited
		// onto a live OVN NIC re-plugs it and costs the guest its lease.
		PublicAddresses: p.publicBootAddresses(res.ID),
		Labels:          map[string]string{"feint.vm": res.ID},
	})
	p.rememberAddress(res)
	// The boot installed the host half of the route; this hands the guest its
	// address, and repairs a machine that already existed. Idempotent.
	for _, address := range p.publicBootAddresses(res.ID) {
		p.routeLinkedIP(ctx, address, res)
	}
}

// rememberAddress keeps the private address on the resource, not only on the
// runtime binding.
//
// PowerOff clears Runtime[address] — correctly, since nothing answers there any
// more — and a Vm placed in a Subnet was unaffected because its address lives in
// Attrs. A Vm created without one had it nowhere else, so stopping it emptied
// PrivateIp: one field, two behaviours, and Terraform reading private_ip saw
// null after a stop. Outscale keeps the private address of a stopped Vm; it is
// released when the machine is terminated, with the resource.
//
// TestAStoppedVmKeepsItsPrivateAddress fails without this.
func (p *Pack) rememberAddress(res *resource.Resource) {
	if _, already := res.Attrs["PrivateIp"]; already {
		return
	}
	if address := p.addressOf(res); address != "" {
		res.Attrs["PrivateIp"] = address
	}
}

// powerOff stops the backing machine, keeping its filesystem.
func (p *Pack) powerOff(ctx context.Context, res *resource.Resource) {
	res.State = stateStopped
	p.binding().PowerOff(ctx, res)
}

// removeMachine destroys the backing machine.
func (p *Pack) removeMachine(ctx context.Context, res *resource.Resource) {
	p.binding().Destroy(ctx, res)
}

// refreshMachine republishes the address of a running VM.
func (p *Pack) refreshMachine(ctx context.Context, res *resource.Resource) bool {
	changed := p.binding().RefreshIfRunning(ctx, res)
	if changed {
		// A virtual machine gets its address tens of seconds after it starts,
		// so this is where it first becomes known for one.
		p.rememberAddress(res)
	}
	return changed
}

// addressOf is what a Vm publishes as PrivateIp. Outscale's Vm schema declares
// the field and a real one carries a value, so the emulator fills it in.
func (p *Pack) addressOf(res *resource.Resource) string {
	return p.binding().AddressOf(res)
}

// attachmentOf rebuilds the placement a Vm was created with, so a machine
// starting after a stop lands on the same network with the same address.
//
// Rebuilt from the stored fields rather than kept beside them: the address is
// published in PrivateIp and the network is derived from the Subnet, so there is
// one source for both and a snapshot restore cannot lose half of it.
func (p *Pack) attachmentOf(res *resource.Resource) []machine.Attachment {
	subnetID := stringOf(res.Attrs["SubnetId"])
	address := stringOf(res.Attrs["PrivateIp"])
	// A Vm created with no Subnet is in the public Cloud, and Outscale gives it
	// a private address there: the schema declares PrivateIp and a real one
	// carries a value. So it asks for the emulator's own network rather than
	// nothing, and publishes the address it receives.
	//
	// This is the distinction #202 nearly lost. The fallback network was not
	// wrong in itself; it was wrong when the address it handed out was published
	// by no API, which was the Scaleway case and not this one. Removing it
	// outright left a running Vm with no PrivateIp at all, and the conformance
	// suite said so in as many words: "the machine is running and the API
	// publishes no PrivateIp".
	//
	// TestAVmOutsideANetStillCarriesAPrivateAddress fails without this.
	if subnetID == "" {
		return []machine.Attachment{{Network: machine.DefaultMachineNetwork}}
	}
	if address == "" {
		return nil
	}
	subnet, found := p.env.Store.Get(Name, kindSubnet, subnetID)
	if !found || subnet.Runtime[runtimeNetworkKey] == "" {
		return nil
	}
	prefix, err := prefixOf(subnet, "IpRange")
	if err != nil {
		return nil
	}
	return []machine.Attachment{{
		Network:   subnet.Runtime[runtimeNetworkKey],
		Address:   address,
		PrefixLen: prefix.Bits(),
	}}
}
