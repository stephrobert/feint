package exoscale

import (
	"context"
	"log/slog"

	"github.com/stephrobert/feint/internal/core/cloudinit"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Exoscale instances are backed by real machines, on the same shared lifecycle
// as the other two packs.
//
// Before this the pack published a fixed "203.0.113.10" on every instance it
// created: two instances reported the same address, and nothing answered on
// either. That is the defect this project exists to avoid, written into the
// emulator by hand.
//
// The sequence lives in machine.Binding. What is here is only Exoscale's: which
// template boots what, and the login each template declares.

// binding is this pack's half of the shared machine lifecycle.
func (p *Pack) binding() machine.Binding {
	return machine.Binding{
		Driver:   p.env.Machines,
		Provider: Name,
		Prefix:   "feint-exo-",
		// No provider-wide login: Exoscale's template schema carries a
		// default-user, so the login travels with the boot instead.
		RuntimeKey:   "machine",
		AddressKey:   "address",
		RunningState: "running",
		// Declared in their instance-state enum.
		FailedState: "error",
		// The operator's identifier declarations (FEINT_BOOT_IMAGES), consulted
		// by the binding only when no template resolved. The optional @login of
		// an entry matters most here, where the login belongs to the template.
		Declared: p.env.BootImages,
		Log:      p.env.Log,
	}
}

// logger returns the environment logger, or the default one when a caller
// built an Env by hand without setting it.
func (p *Pack) logger() *slog.Logger {
	if p.env.Log != nil {
		return p.env.Log
	}
	return slog.Default()
}

// catalogueDate is when the emulated catalogue claims to have been built. A
// fixed date rather than the current one, so two runs answer the same bytes and
// a golden test stays golden — and set at all, because the official CLI renders
// an absent created-at as 0001-01-01, which reads as a broken cloud.
const catalogueDate = "2025-01-01T00:00:00Z"

// templates is the emulated catalogue, in the shape Exoscale's own template
// schema declares — including default-user, which is where the login of a
// machine comes from here rather than from a constant.
// The seven fields after created-at were measured missing by the shape diff of
// 2026-08-10: the real template carries a build, a checksum, a description, a
// maintainer, a size in bytes and a version, and answers
// application-consistent-snapshot-enabled. The values are the emulator's own
// claims, like the rest of this catalogue; the size matches the 10 GiB disk
// floor a create is checked against.
//
// No "zones" key here: which zone a template is available in follows the
// pack's datum (#278), stamped at construction (stampedWithZone, catalog.go).
// Everything served reads Pack.templates; this base table stays for what is
// zone-independent — templateFor resolves default-user from it.
var templates = []map[string]any{
	{
		"id": "11111111-1111-4111-8111-111111111111", "name": "Linux Ubuntu 24.04 LTS 64-bit",
		"family": "ubuntu", "default-user": "ubuntu", "visibility": "public",
		"boot-mode": "uefi", "ssh-key-enabled": true, "password-enabled": false,
		"created-at":  catalogueDate,
		"description": "Linux Ubuntu 24.04 LTS 64-bit", "version": "24.04",
		"build": "feint", "checksum": "00000000000000000000000000000001",
		"maintainer": "feint", "size": 10737418240,
		"application-consistent-snapshot-enabled": false,
	},
	{
		"id": "22222222-2222-4222-8222-222222222222", "name": "Linux Debian 12 64-bit",
		"family": "debian", "default-user": "debian", "visibility": "public",
		"boot-mode": "uefi", "ssh-key-enabled": true, "password-enabled": false,
		"created-at":  catalogueDate,
		"description": "Linux Debian 12 64-bit", "version": "12",
		"build": "feint", "checksum": "00000000000000000000000000000002",
		"maintainer": "feint", "size": 10737418240,
		"application-consistent-snapshot-enabled": false,
	},
}

// runtimeTemplates maps a template onto what the driver boots, and is emulator
// business rather than API surface — which is why it is not in the catalogue
// above. Putting it there is how an OsFamily field the API does not define ended
// up in an Outscale response.
var runtimeTemplates = map[string]string{
	"11111111-1111-4111-8111-111111111111": "ubuntu:24.04",
	"22222222-2222-4222-8222-222222222222": "debian:12",
}

// templateFor resolves a template onto what to boot and the login that
// template declares — one resolution, because Exoscale's login is a property
// of the template rather than of the cloud, and the right distribution with
// the wrong login is still a machine nobody can enter.
//
// An unknown identifier resolves to nothing rather than falling back: the
// control plane has accepted it — docs/limits.md declares identifiers
// unchecked, deliberately — and the shared binding refuses the boot out loud
// instead of starting the default template under the client's template id
// (#83).
//
// TestExoscaleTemplateResolutionIsExact fails against the fallback version.
func templateFor(id string) (machine.Image, bool) {
	ref, known := runtimeTemplates[id]
	if !known {
		return machine.Image{}, false
	}
	img := machine.Image{Ref: ref}
	for _, t := range templates {
		if t["id"] == id {
			img.User, _ = t["default-user"].(string)
			break
		}
	}
	return img, true
}

// start powers an instance on. The state is set whether or not a machine
// started: with no runtime the instance still reaches running, which is what a
// client waits for, and the log says nothing is behind it.
func (p *Pack) start(ctx context.Context, res *resource.Resource) {
	templateID := ""
	if t, ok := res.Attrs["template"].(map[string]any); ok {
		templateID, _ = t["id"].(string)
	}
	img, _ := templateFor(templateID)
	name, _ := res.Attrs["name"].(string)
	userData, _ := res.Attrs["user-data"].(string)

	p.binding().PowerOn(ctx, res, machine.Boot{
		Image:     img.Ref,
		Requested: templateID,
		Hostname:  name,
		User:      img.User,
		// The key the client registered, so the machine it is attached to can
		// actually be opened. Nothing was passed here: the instance booted with
		// empty cloud-init — no user provisioned, no sshd on a minimal image —
		// while the pack published an address on it, which binding.go itself
		// calls "an address the API publishes and nothing answers on".
		//
		// It also makes Boot.User real. CLAUDE.md celebrates that field as
		// existing because Exoscale declares its default user per template, and
		// it was dead code on the only pack that motivated it, since Render
		// returns "" when there are no keys.
		//
		// TestAnExoscaleKeyReachesTheMachine fails without this.
		AuthorizedKeys: p.authorizedKeys(res),
		// Decoded here, stored encoded: Exoscale documents user-data as base64,
		// so that is what a read gives back and what cloud-init must not see.
		CloudInit: cloudinit.Decode(userData),
		// The elastic IPs already attached, on the launch: a route edited onto
		// a live OVN NIC re-plugs it and costs the guest its lease.
		PublicAddresses: p.elasticBootAddresses(res),
		Labels:          map[string]string{"feint.instance": res.ID},
	})
	// The boot installed the host half of every route; this hands the guest
	// its addresses, and repairs a machine that already existed. Idempotent.
	for _, address := range p.elasticBootAddresses(res) {
		p.routeElasticIP(ctx, address, res)
	}
	// The private networks the instance already joined come back as extra
	// interfaces, after the boot rather than on it: Exoscale's eth0 is the
	// public interface — the address this pack publishes as public-ip is the
	// primary interface's — so a membership must never become the primary the
	// way a Boot.Attachment would. Attach is idempotent by network, so a
	// machine that kept its devices across a stop is repaired, not doubled.
	p.reattachPrivateNetworks(ctx, res)
	// The groups this instance wears reach its interfaces, and the groups
	// that name those groups as sources are re-expanded now that this machine
	// has addresses (#475). After the networks, so the expansion sees every
	// interface.
	p.syncFirewallAfterBoot(ctx, res)
}

// reattachPrivateNetworks puts a freshly booted machine back on every private
// network its memberships name. Attaching to a stopped instance and then
// starting it is the ordinary order, and without this the machine came up on
// the default network alone while the API published a membership it had never
// taken.
func (p *Pack) reattachPrivateNetworks(ctx context.Context, res *resource.Resource) {
	for _, m := range instanceAttachmentsOf(res) {
		pn, found := p.env.Store.Get(Name, kindPrivateNetwork, m.NetworkID)
		if !found {
			continue
		}
		p.attachMachineToPrivateNetwork(ctx, res, pn, m.IP)
	}
}

// authorizedKeys is the material of the key an instance names, or nothing.
//
// The key is addressed by name — Exoscale's identifier for it — and its material
// lives in Runtime because no route may return it.
func (p *Pack) authorizedKeys(res *resource.Resource) []string {
	refs, _ := res.Attrs["ssh-keys"].([]any)
	out := make([]string, 0, len(refs))
	for _, entry := range refs {
		ref, _ := entry.(map[string]any)
		name, _ := ref["name"].(string)
		if name == "" {
			continue
		}
		key, found := p.env.Store.Get(Name, kindSSHKey, name)
		if !found {
			// A name that answers to nothing: the create accepted it, and the
			// machine gets no key rather than an invented one.
			continue
		}
		if material := key.Runtime["public-key"]; material != "" {
			out = append(out, material)
		}
	}
	return out
}

// destroy removes the backing machine, so a leftover cannot outlive the instance
// that justified it. The elastic routes go first, because on OVN the uplink
// route outlives the machine.
func (p *Pack) destroy(ctx context.Context, res *resource.Resource) {
	for _, address := range p.elasticBootAddresses(res) {
		p.unrouteElasticIP(ctx, address, res)
	}
	p.binding().Destroy(ctx, res)
}

// refresh publishes the address of a running instance, on the read a client is
// making anyway.
func (p *Pack) refresh(ctx context.Context, res *resource.Resource) bool {
	return p.binding().RefreshIfRunning(ctx, res)
}

// addressOf is what an instance publishes as public-ip, a field Exoscale's own
// instance schema declares.
func (p *Pack) addressOf(res *resource.Resource) string {
	return p.binding().AddressOf(res)
}
