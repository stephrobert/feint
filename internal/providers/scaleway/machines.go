package scaleway

import (
	"context"
	"log/slog"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Backing an emulated server with a real machine is what separates "the API
// answered running" from "something is running". With the Incus driver enabled,
// poweron starts an instance, poweroff stops it, terminate destroys it, and the
// server's private IP is the instance's real address, so a client can reach it.
//
// The sequence lives in machine.Binding, shared with every other pack. What
// stays here is only what is Scaleway's: its login, how an image label maps onto
// something bootable, which attribute the address is published under, and the
// NICs a server owns. The wire shapes differ between providers and must; the
// behaviour does not and must not.

// runtimeMachineKey is where the backing machine name is kept. Runtime, never
// Attrs, so it cannot leak into an API response. Named here rather than only in
// the binding because the firewall, the flexible IPs and the private NICs all
// need the machine a server runs on.
const runtimeMachineKey = "machine"

// DefaultUser is the login Scaleway provisions on its images.
//
// Each cloud picks its own: AWS uses ec2-user or ubuntu depending on the AMI,
// Outscale uses outscale, Exoscale uses whatever the template declares. Scaleway
// logs you in as root, which its own CLI states outright (`scw instance server
// ssh` documents username=root).
const DefaultUser = "root"

// binding is this pack's half of the shared machine lifecycle.
func (p *Pack) binding() machine.Binding {
	return machine.Binding{
		Driver:     p.env.Machines,
		Provider:   Name,
		Prefix:     "feint-scw-",
		User:       DefaultUser,
		RuntimeKey: runtimeMachineKey,
		// Recorded, never published. private_ip is "deprecated and always null
		// when routed_ip_enabled is True" in the SDK's own words, and routed is
		// this emulator's default: a client reads the address through ipam/v1 or
		// through the flexible IP it attached, never off the server.
		AddressKey:   "address",
		RunningState: "running",
		// Scaleway declares no error state for a server either.
		FailedState: "stopped",
		// The operator's identifier declarations (FEINT_BOOT_IMAGES), consulted
		// by the binding only when the catalogue resolved nothing.
		Declared: p.env.BootImages,
		Log:      p.env.Log,
	}
}

// imageFor resolves a Scaleway image label onto what stands in for it — image
// and login in one value, because the right distribution with the wrong login
// is still a machine nobody can enter.
//
// Exact lookup, deliberately. This used to match by substring with a fallback,
// which booted a silent Ubuntu 22.04 for ubuntu_focal, centos, rocky — every
// label the table does not list — while the API kept reporting the label the
// client sent (#83). An unknown label now resolves to nothing, and the shared
// binding refuses the boot instead of substituting.
//
// TestScalewayImageResolutionIsExact fails against the substring version.
func imageFor(label string) (machine.Image, bool) {
	entry, known := marketplaceImages[label]
	if !known {
		return machine.Image{}, false
	}
	return entry.Boot, true
}

// requestedImageOf names, for the refusal log, what the client asked to boot:
// the catalogue label when there is one, otherwise what the create stored. A
// foreign UUID is named as itself; behind unknownImageID the stored name is
// the client's own label when the create carried one, and the display default
// — which the client never said — is never reported as if it had.
func requestedImageOf(res *resource.Resource, label string) string {
	if label != "" {
		return label
	}
	image, _ := res.Attrs["image"].(map[string]any)
	if image == nil {
		return ""
	}
	id, _ := image["id"].(string)
	if id != "" && id != unknownImageID {
		return id
	}
	if name, _ := image["name"].(string); name != "" && name != defaultImageLabel {
		return name
	}
	return id
}

// startMachine powers the server on. It returns the address to publish, empty
// when nothing is actually running.
func (p *Pack) startMachine(ctx context.Context, res *resource.Resource) {
	label, _ := res.Attrs["image_label"].(string)
	hostname, _ := res.Attrs["hostname"].(string)
	img, _ := imageFor(label)

	// A client that stored its own cloud-init gets it verbatim, the way Scaleway
	// hands the "cloud-init" user data key to cloud-init at boot. The
	// consequence is stated in docs/limits.md: with a custom cloud-init, the
	// project's SSH keys are only installed if the script installs them.
	p.binding().PowerOn(ctx, res, machine.Boot{
		Image:          img.Ref,
		User:           img.User,
		Requested:      requestedImageOf(res, label),
		Hostname:       hostname,
		AuthorizedKeys: p.authorizedKeys(res.Tenant.Project),
		CloudInit:      userDataOf(res, CloudInitKey),
		// The NICs the server already owns travel with the boot. Attaching a NIC
		// to a stopped server then powering it on is the ordinary Terraform
		// order, and without this the machine came up on the runtime's default
		// bridge alone while the API published an address on a private network
		// it had never joined.
		Attachments: p.attachmentsOf(res),
		// So do the public addresses it was promised — flexible IPs attached
		// before the boot, and the dynamic one when the flag asked for it. On
		// the launch, not routed afterwards: editing a live OVN NIC re-plugs it
		// and the guest loses its DHCP lease (#116).
		PublicAddresses: p.publicAddressesOf(res),
		Labels: map[string]string{
			"feint.server": res.ID,
			"feint.zone":   res.Tenant.Zone,
		},
	})
}

// attachmentsOf turns the NICs a server already owns into the attachments its
// machine boots with. Ordered by creation so eth1 is the first NIC the client
// created, which is what a client reading interface names expects.
func (p *Pack) attachmentsOf(server *resource.Resource) []machine.Attachment {
	nics := p.privateNICsOf(server.ID)
	out := make([]machine.Attachment, 0, len(nics))
	for _, nic := range nics {
		pn, found := p.env.Store.Get(Name, kindPrivateNetwork, nic.Runtime[runtimePrivateNetworkKey])
		if !found || pn.Runtime[runtimeNetworkKey] == "" {
			continue
		}
		prefix, err := prefixOf(pn)
		if err != nil {
			continue
		}
		out = append(out, machine.Attachment{
			Network:   pn.Runtime[runtimeNetworkKey],
			Address:   p.addressOfNIC(nic.ID),
			PrefixLen: prefix.Bits(),
		})
	}
	return out
}

// stopMachine powers the server off, keeping its filesystem.
func (p *Pack) stopMachine(ctx context.Context, res *resource.Resource) {
	p.binding().PowerOff(ctx, res)
}

// removeMachine destroys the backing machine. Called on delete and terminate,
// where a leftover container would outlive the server that justified it.
func (p *Pack) removeMachine(ctx context.Context, res *resource.Resource) {
	p.binding().Destroy(ctx, res)
}

// refreshMachine republishes the address of a running server.
func (p *Pack) refreshMachine(ctx context.Context, res *resource.Resource) bool {
	return p.binding().RefreshIfRunning(ctx, res)
}

// logger returns the environment logger, or the default one when a caller built
// an Env by hand without setting it.
func (p *Pack) logger() *slog.Logger {
	if p.env.Log != nil {
		return p.env.Log
	}
	return slog.Default()
}
