package scaleway

import (
	"context"
	"log/slog"
	"strings"

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

// defaultImage stands in for the default Scaleway image.
const defaultImage = "ubuntu:22.04"

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
		Log:         p.env.Log,
	}
}

// imageFor maps a Scaleway image label onto the base image that stands in for
// it, the same way local AWS emulators map an AMI onto a runtime image. The
// value is runtime-agnostic ("ubuntu:22.04"): the driver translates it, which is
// what lets a pack name an operating system without knowing what runs it.
//
// An unknown label falls back to the default rather than failing: the emulator
// has no image catalogue and refusing would only block a workflow.
func imageFor(label string) string {
	switch {
	case strings.Contains(label, "noble"), strings.Contains(label, "24_04"), strings.Contains(label, "2404"):
		return "ubuntu:24.04"
	case strings.Contains(label, "jammy"), strings.Contains(label, "22_04"), strings.Contains(label, "2204"):
		return "ubuntu:22.04"
	case strings.Contains(label, "bookworm"), strings.Contains(label, "debian12"):
		return "debian:12"
	case strings.Contains(label, "trixie"), strings.Contains(label, "debian13"):
		return "debian:13"
	case strings.Contains(label, "alpine"):
		return "alpine:3.21"
	default:
		return defaultImage
	}
}

// startMachine powers the server on. It returns the address to publish, empty
// when nothing is actually running.
func (p *Pack) startMachine(ctx context.Context, res *resource.Resource) {
	label, _ := res.Attrs["image_label"].(string)
	hostname, _ := res.Attrs["hostname"].(string)

	// A client that stored its own cloud-init gets it verbatim, the way Scaleway
	// hands the "cloud-init" user data key to cloud-init at boot. The
	// consequence is stated in docs/limits.md: with a custom cloud-init, the
	// project's SSH keys are only installed if the script installs them.
	p.binding().PowerOn(ctx, res, machine.Boot{
		Image:          imageFor(label),
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
