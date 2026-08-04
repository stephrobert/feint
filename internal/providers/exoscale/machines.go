package exoscale

import (
	"context"

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
		Log:         p.env.Log,
	}
}

// catalogueDate is when the emulated catalogue claims to have been built. A
// fixed date rather than the current one, so two runs answer the same bytes and
// a golden test stays golden — and set at all, because the official CLI renders
// an absent created-at as 0001-01-01, which reads as a broken cloud.
const catalogueDate = "2025-01-01T00:00:00Z"

// templates is the emulated catalogue, in the shape Exoscale's own template
// schema declares — including default-user, which is where the login of a
// machine comes from here rather than from a constant.
var templates = []map[string]any{
	{
		"id": "11111111-1111-4111-8111-111111111111", "name": "Linux Ubuntu 24.04 LTS 64-bit",
		"family": "ubuntu", "default-user": "ubuntu", "visibility": "public",
		"boot-mode": "uefi", "ssh-key-enabled": true, "password-enabled": false,
		"created-at": catalogueDate, "zones": allZones(),
	},
	{
		"id": "22222222-2222-4222-8222-222222222222", "name": "Linux Debian 12 64-bit",
		"family": "debian", "default-user": "debian", "visibility": "public",
		"boot-mode": "uefi", "ssh-key-enabled": true, "password-enabled": false,
		"created-at": catalogueDate, "zones": allZones(),
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

const defaultTemplateID = "11111111-1111-4111-8111-111111111111"

// templateFor returns what to boot and the login that template declares. An
// unknown identifier falls back rather than failing: the catalogue is small and
// fixed, and refusing to boot over a template name would block a workflow the
// control plane already accepted.
func templateFor(id string) (image, user string) {
	image, known := runtimeTemplates[id]
	if !known {
		id, image = defaultTemplateID, runtimeTemplates[defaultTemplateID]
	}
	for _, t := range templates {
		if t["id"] == id {
			user, _ = t["default-user"].(string)
			break
		}
	}
	return image, user
}

// start powers an instance on. The state is set whether or not a machine
// started: with no runtime the instance still reaches running, which is what a
// client waits for, and the log says nothing is behind it.
func (p *Pack) start(ctx context.Context, res *resource.Resource) {
	templateID := ""
	if t, ok := res.Attrs["template"].(map[string]any); ok {
		templateID, _ = t["id"].(string)
	}
	image, user := templateFor(templateID)
	name, _ := res.Attrs["name"].(string)
	userData, _ := res.Attrs["user-data"].(string)

	p.binding().PowerOn(ctx, res, machine.Boot{
		Image:    image,
		Hostname: name,
		User:     user,
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
		Labels:    map[string]string{"feint.instance": res.ID},
	})
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
// that justified it.
func (p *Pack) destroy(ctx context.Context, res *resource.Resource) {
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
