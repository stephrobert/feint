package machine

import (
	"context"
	"log/slog"
	"strings"

	"github.com/stephrobert/feint/internal/core/cloudinit"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Binding is how a pack backs an emulated server with a real machine.
//
// Every pack needs the same sequence, and every pack was writing it again. Two
// copies existed before this, one per provider, a hundred and fifty lines each,
// and only four things differed: the name prefix, the login the provider's
// images carry, which image to boot, and which attribute the address is
// published under. Everything else — rendering cloud-init when the caller gave
// none, recording the runtime name out of reach of the API, publishing the
// address, degrading quietly when no runtime is configured, logging what could
// not be done rather than failing the control plane — was the same code twice.
//
// That is the shape this exists to remove. A defect fixed in one pack has to be
// fixed in all of them, and two copies is exactly how it is not.
//
// It stays neutral: it knows a pack has a prefix, a user and a driver. It knows
// nothing about any provider, and the wire shapes stay where they belong, in
// the packs, because those must differ — a client must not see the difference
// from its own cloud, which is the whole point.
type Binding struct {
	// Driver is what actually runs machines. Nil means metadata only, which is
	// the default: starting machines is a side effect on the operator's host.
	Driver Driver
	// Provider labels everything this binding creates, so a sweep can find its
	// own work and an operator's machines are never touched.
	Provider string
	// Prefix names the machines, e.g. "feint-scw-". It is what tells one
	// pack's machines from another's on a shared host, and what the event
	// filter and the sweep recognise.
	Prefix string
	// User is the login the provider provisions on its images. Each cloud picks
	// its own — root on Scaleway, outscale on Outscale — and getting it wrong
	// produces a machine that boots, holds the right key, and refuses every
	// login.
	User string
	// RuntimeKey is where the backing machine's name is kept on the resource. It
	// belongs in Runtime and never in Attrs, so it cannot leak into an API
	// response, and the binding writes it rather than every pack remembering to.
	RuntimeKey string
	// AddressKey is where the machine's address is kept, in Runtime.
	//
	// Runtime and not Attrs, which was a mistake worth stating. The binding used
	// to write the address straight into an API field, which forced every pack
	// to publish it whether its API has such a field or not — and Scaleway's
	// does not: private_ip is "deprecated and always null when routed_ip_enabled
	// is True", in their SDK's own words, and routed is this emulator's default.
	// A client reading an address there would find one where the real API gives
	// null and sends it to IPAM instead.
	//
	// So the binding records what the machine answers on, and each pack decides
	// whether its API exposes that and under which name. Outscale publishes it
	// as PrivateIp, Exoscale as public-ip, Scaleway nowhere — its address is
	// read through ipam/v1 or through the flexible IP, which is what a real
	// client does.
	AddressKey string
	// RunningState is what this provider calls a machine that is up. All three
	// happen to say "running", and the field exists so a pack whose API says
	// something else does not have to reimplement the check.
	RunningState string
	// FailedState is what this provider calls a machine that was asked to start
	// and did not. It exists because reporting RunningState in that case is the
	// emulator lying about the one thing it is here to get right: an instance
	// answered "running" while incus had killed the launch, and the API kept
	// saying so on every read afterwards.
	//
	// Their vocabularies differ and the honest word is theirs. Exoscale declares
	// "error" in its instance-state enum; Scaleway and Outscale declare no error
	// state for a machine at all, so a start that failed is "stopped" there —
	// which is true, and inventing a word their clients would not parse is worse.
	FailedState string
	// Declared maps an opaque image identifier onto the operating system the
	// operator says it is — FEINT_BOOT_IMAGES, parsed once at startup by
	// ParseDeclaredImages. Consulted only when the pack's own catalogue resolved
	// nothing, so a catalogue entry always wins over a declaration.
	//
	// It is the door through the boot refusal below, and it is a declaration
	// rather than a guess: #392's generic substitution was refused because a
	// stack that asked for an AlmaLinux and got an Ubuntu boots and then fails
	// at the first dnf. Here the operator names the OS, signs the mapping, and
	// the log records that the declaration — not the emulator — chose the image.
	// The keys are opaque strings; this stays as provider-neutral as the rest.
	Declared map[string]Image
	// Log is where a runtime failure is reported. A machine that will not start
	// must never break the control plane, which makes this the only place an
	// operator can learn why nothing is running.
	Log *slog.Logger
}

// Name is the runtime name for a resource id.
func (b Binding) Name(id string) string {
	return b.Prefix + strings.ReplaceAll(id, "/", "-")
}

// Image is what a pack's catalogue resolves an image identifier to: the image
// the runtime boots and the login that image provisions. One value on purpose,
// never two lookups — an identifier resolved to the right distribution with
// the wrong login still hands the user a machine nobody can enter, which is
// the same half-success in a different place.
//
// The zero value means the identifier resolved to nothing, and Start refuses
// to boot it rather than substituting: see the guard there.
type Image struct {
	// Ref is runtime-agnostic ("ubuntu:24.04"): the driver translates it,
	// which is what lets a pack name an operating system without knowing what
	// runs it.
	Ref string
	// User is the login the image provisions. Empty keeps the binding's
	// provider-wide login, for the clouds where the login belongs to the cloud
	// rather than to the image.
	User string
}

// Boot is what a pack asks for when it powers a server on.
type Boot struct {
	// ID is the emulated resource, used for the machine name and the labels.
	ID string
	// Image is runtime-agnostic ("ubuntu:24.04"): the driver translates it,
	// which is what lets a pack name an operating system without knowing what
	// runs it. Empty means the identifier the client sent resolved to nothing,
	// and a real runtime refuses to boot it rather than substituting.
	Image string
	// Requested is the identifier the client sent, verbatim, so the refusal
	// log can name it. Image is what that identifier resolved to; this is what
	// to say when it resolved to nothing.
	Requested string
	// Reason, when Image is empty, is the pack's diagnosis of why the
	// identifier resolved to nothing. The two known cases end in the same
	// refusal and must not read the same in the log: an identifier nobody ever
	// created is a typo the control plane accepted, while an image the
	// emulator itself registered and serves is a record with no disk contents
	// behind it. Empty falls back to the generic wording.
	Reason string
	// Hostname the guest takes. Empty falls back to the resource id.
	Hostname string
	// User overrides the binding's login for this boot, empty to keep it.
	//
	// Two of the three providers give one login for the whole cloud. Exoscale
	// does not: its template schema carries a default-user field, so the login
	// is a property of the image rather than of the provider. Forcing all three
	// into one shape would mean inventing a login for Exoscale, which is exactly
	// what produces a machine that boots, holds the right key, and refuses every
	// login.
	User string
	// AuthorizedKeys are installed for User.
	AuthorizedKeys []string
	// CloudInit, when the client supplied its own, is handed over verbatim the
	// way every one of these clouds passes user data to cloud-init at boot. It
	// replaces the generated config rather than merging with it: merging two
	// cloud-configs means parsing and re-emitting YAML, and a silently altered
	// script is worse than one the emulator did not touch.
	CloudInit string
	// Attachments are the interfaces the machine boots with. Attaching a NIC to
	// a stopped server and then powering it on is the ordinary Terraform order,
	// and without carrying them here the machine comes up on the runtime's
	// default bridge alone while the API publishes an address on a network it
	// never joined.
	Attachments []Attachment
	// PublicAddresses are the emulated public addresses the pack has already
	// promised for this machine — a flexible IP attached before the boot, an
	// ephemeral address the flag asked for. Carried here so the driver can
	// install the routes before the first boot instead of editing a live NIC:
	// the edit is what cost a guest its DHCP lease (see Spec.PublicAddresses).
	PublicAddresses []string
	// Labels are added to the ones the binding sets itself.
	Labels map[string]string
}

// Started is what a boot produced.
type Started struct {
	// Machine is the runtime name, for the pack to keep out of reach of the API.
	Machine string
	// Address is what the machine answers on, empty when nothing started.
	Address string
}

// Start powers a machine on. It never fails the caller: with no runtime the
// server stays metadata and the control plane keeps working, because an
// emulator that refused to create a server when Incus is down would be worse
// than one that only tracks state.
func (b Binding) Start(ctx context.Context, boot Boot) Started {
	name := b.Name(boot.ID)
	if b.Driver == nil {
		return Started{}
	}
	// An identifier no catalogue holds resolves to no image at all, and a real
	// runtime must not paper over it: ask for Alpine, boot Ubuntu, and every
	// signal says success — the exact defect this project exists to avoid
	// (#83). The refusal lives here, in the shared layer, so no pack can
	// substitute silently and a fourth one could not either. Noop is exempt on
	// purpose: it boots nothing, so there is nothing to substitute, and the
	// control plane must stay usable without a runtime — hardcoded production
	// identifiers included, as docs/limits.md promises.
	//
	// The operator's declaration is the one way past it (#465): consulted here,
	// in the shared layer, so the three packs get the same door and a fourth
	// could not forget it — and only after the pack's own catalogue resolved
	// nothing, so no declaration can shadow a catalogue entry.
	//
	// TestAnUnknownImageFailsTheBootInsteadOfSubstituting and
	// TestADeclaredIdentifierBootsTheImageTheOperatorNamed fail without this.
	if _, metadataOnly := b.Driver.(Noop); boot.Image == "" && !metadataOnly {
		declared, ok := b.Declared[boot.Requested]
		if !ok || boot.Requested == "" {
			b.refuseUnknownImage(boot)
			return Started{}
		}
		b.logger().Info("booting the image the operator declared for this identifier",
			"provider", b.Provider, "resource", boot.ID, "image", boot.Requested,
			"declared", declared.Ref, "via", "FEINT_BOOT_IMAGES")
		boot.Image = declared.Ref
		// The login rides the image here as everywhere else: on the cloud where
		// the login belongs to the template rather than to the provider, a
		// declaration that dropped it would boot a machine nobody can enter.
		if boot.User == "" {
			boot.User = declared.User
		}
	}

	// Build what the ref derives when the station lacks it (#465): the recipe
	// is per family, the version travels through, and the first boot pays the
	// build once, announced. A build that fails refuses the boot instead of
	// falling back — for a derived ref the upstream image is the very source
	// the build could not fetch, so the fallback would re-fail with a raw
	// driver error where this refusal names the ref and the source. The state
	// the caller publishes is FailedState: the machine did not start, and
	// saying so is the one thing this project cannot compromise on.
	//
	// TestAFailedImageBuildRefusesTheBootAndNamesTheSource fails without this.
	if boot.Image != "" {
		if _, err := EnsureImage(ctx, b.Driver, boot.Image, b.logger()); err != nil {
			spec, _ := SpecFor(boot.Image)
			b.logger().Error("refusing to boot: the image this reference derives could not be built",
				"provider", b.Provider, "resource", boot.ID, "image", boot.Image,
				"requested", boot.Requested, "source", spec.Source, "error", err,
				"fix", "`feint images --only "+spec.Name+"` reproduces the build by hand; "+
					"a version the upstream image server no longer publishes cannot be built — name one it does")
			return Started{}
		}
	}
	user := b.User
	if boot.User != "" {
		user = boot.User
	}

	userData := boot.CloudInit
	if userData == "" {
		hostname := boot.Hostname
		if hostname == "" {
			hostname = boot.ID
		}
		// Rendered from the shared templates: writing one by hand is how the
		// first attempt produced a machine that booted, held the right key, and
		// refused every login.
		rendered, err := cloudinit.Render(cloudinit.Spec{
			Distribution:   boot.Image,
			Hostname:       hostname,
			User:           user,
			AuthorizedKeys: boot.AuthorizedKeys,
			InstallSSHD:    true,
		})
		if err != nil {
			b.logger().Error("could not render cloud-init",
				"provider", b.Provider, "resource", boot.ID, "error", err)
			return Started{}
		}
		userData = rendered
	}

	// A client cloud-config that declares a package step cannot complete on a
	// machine booting with no emulated network under it: such a machine
	// carries only what its provider's API publishes — a routed public
	// address with no NAT and no resolver, or no interface at all (#202) — so
	// `packages:` dies on "Temporary failure resolving" and cloud-init
	// finishes in `status: error`, in a journal inside the guest that nobody
	// opens (#507). Measured both ways on 2026-08-26: the same cloud-config
	// ends `error` on a routed machine and `done` — package installed,
	// listening — on a machine whose network has NAT. Said here, in the shared
	// layer, so every pack's operator reads it in the emulator's log instead
	// of diagnosing a healthy-looking machine; the boot itself proceeds,
	// because everything else about the machine works.
	// TestAPackageStepWithNoRouteOutIsSaidOutLoud fails without this.
	if _, metadataOnly := b.Driver.(Noop); !metadataOnly &&
		boot.CloudInit != "" && len(boot.Attachments) == 0 && declaresPackageStep(boot.CloudInit) {
		b.logger().Warn("this machine's user data declares a package step it cannot complete",
			"provider", b.Provider, "resource", boot.ID, "machine", name,
			"consequence", "the machine boots with no emulated network under it, so it has no route to a package repository and no resolver; cloud-init will finish in `status: error` on package_update_upgrade_install (docs/limits.md, #507)")
	}

	labels := map[string]string{LabelKey: b.Provider}
	for k, v := range boot.Labels {
		labels[k] = v
	}

	m, err := b.Driver.Start(ctx, Spec{
		Name:            name,
		Image:           boot.Image,
		Labels:          labels,
		User:            user,
		AuthorizedKeys:  boot.AuthorizedKeys,
		CloudInit:       userData,
		Attachments:     boot.Attachments,
		PublicAddresses: boot.PublicAddresses,
	})
	if err != nil {
		// Deliberately not fatal, and never swallowed: this log is the only way
		// to learn why nothing started.
		b.logger().Error("could not start the backing machine",
			"provider", b.Provider, "resource", boot.ID,
			"machine", name, "image", boot.Image, "error", err)
		return Started{}
	}
	return Started{Machine: name, Address: m.IP}
}

// refuseUnknownImage is the actionable half of the boot refusal: it names the
// identifier the client sent, why nothing here can run it, and the two gestures
// that lead through — asking the providers' public listings what the identifier
// is (`feint images resolve`, no account needed), then declaring the answer
// (FEINT_BOOT_IMAGES). A refusal without a way through gets worked around by
// copying the emulator, which teaches nobody anything; a guess would teach
// worse, a machine that boots and then fails at its first package install —
// the reason #392's generic substitution was refused.
//
// TestTheBootRefusalNamesTheGesturesThatUnblock fails without the gestures.
func (b Binding) refuseUnknownImage(boot Boot) {
	reason := boot.Reason
	if reason == "" {
		reason = "the identifier is in no catalogue"
	}
	id := boot.Requested
	if id == "" {
		id = "<empty>"
	}
	b.logger().Error("refusing to boot: nothing says which operating system this image identifier names",
		"provider", b.Provider, "resource", boot.ID, "image", id, "reason", reason,
		"consequence", "the machine stays "+b.FailedState+"; guessing an OS would boot a machine that fails at its first package install",
		"ask", "`feint images resolve "+id+"` looks it up in the providers' public listings, no account needed",
		"fix", "declare what it is, then restart: FEINT_BOOT_IMAGES='"+id+"=<family>:<version>' with a family among "+strings.Join(Families(), ", "))
}

// ours refuses a backing-machine name the emulator could not have produced.
//
// The name lives in Resource.Runtime, and a restored state carries it verbatim:
// PUT /_feint/state and `feint snapshot load` both go through store.Restore, and
// snapshot.go documents that the format is meant to outlive its instance and be
// loaded into a different one. So this name is an input from outside, not a value
// of ours, however much it looks like one.
//
// An audit made the point concretely: a crafted snapshot naming
// "production-database" was enough to have the driver run `incus stop --force`
// then `delete --force` on it, at the next entirely legitimate DELETE. safeName
// accepted the string, because a regex answers "is this well formed", never "is
// this ours".
//
// The prefix is what the emulator itself writes through Name, so requiring it
// answers the second question without a runtime call. A name that fails falls
// back to the derived one rather than being refused: the resource may well have a
// machine, and the derived name is the only one it could ever have had.
func (b Binding) ours(id, machine string) string {
	if b.Prefix != "" && strings.HasPrefix(machine, b.Prefix) {
		return machine
	}
	b.logger().Warn("ignoring a backing machine name the emulator could not have created",
		"provider", b.Provider, "resource", id, "machine", machine, "prefix", b.Prefix)
	return b.Name(id)
}

// Stop powers a machine off, keeping its filesystem. The caller withdraws the
// address it published: an address that answers nothing is the defect this
// project exists to avoid.
func (b Binding) Stop(ctx context.Context, id, machine string) {
	if b.Driver == nil || machine == "" {
		return
	}
	machine = b.ours(id, machine)
	if err := b.Driver.Stop(ctx, machine); err != nil {
		b.logger().Error("could not stop the backing machine",
			"provider", b.Provider, "resource", id, "machine", machine, "error", err)
	}
}

// Remove destroys the machine, so a leftover cannot outlive the resource that
// justified it. The name is derived when the caller has none, which is the case
// for a resource that never started.
func (b Binding) Remove(ctx context.Context, id, machine string) {
	if b.Driver == nil {
		return
	}
	if machine == "" {
		machine = b.Name(id)
	} else {
		machine = b.ours(id, machine)
	}
	if err := b.Driver.Remove(ctx, machine); err != nil {
		b.logger().Error("could not remove the backing machine",
			"provider", b.Provider, "resource", id, "machine", machine, "error", err)
	}
}

// Address asks the runtime what a machine answers on.
//
// A container has its address immediately; a virtual machine gets one tens of
// seconds later, once it has booted and DHCP has answered. Start therefore never
// waits, and a pack calls this on the read a client is making anyway.
func (b Binding) Address(ctx context.Context, id, machine string) (string, bool) {
	if b.Driver == nil || machine == "" {
		return "", false
	}
	// Read-only, but still checked: inspecting an arbitrary instance of the host
	// would publish its address as if it were the emulated server's.
	machine = b.ours(id, machine)
	m, ok, err := b.Driver.Inspect(ctx, machine)
	if err != nil {
		b.logger().Error("could not inspect the backing machine",
			"provider", b.Provider, "resource", id, "machine", machine, "error", err)
		return "", false
	}
	if !ok || m.IP == "" {
		return "", false
	}
	return m.IP, true
}

func (b Binding) logger() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
}

// The four calls below are what a pack actually makes. They take the resource
// because the bookkeeping around a machine — where its name is kept, where its
// address is published, when to withdraw it — was the part every pack rewrote,
// and rewrote almost identically. What a pack still owns is the state name and
// how to build the Boot, because those are its own vocabulary.

// PowerOn starts the machine and records what came back: the runtime name out of
// reach of the API, and the address where the pack publishes it.
func (b Binding) PowerOn(ctx context.Context, res *resource.Resource, boot Boot) bool {
	boot.ID = res.ID

	// No runtime configured is not a failure. It is the documented degraded
	// mode: the control plane is the whole emulation, and a client driving the
	// API sees a machine that runs. This is what keeps the conformance suites
	// runnable in CI, where no runtime exists.
	if b.Driver == nil {
		res.State = b.RunningState
		return true
	}

	started := b.Start(ctx, boot)
	if started.Machine == "" {
		// A runtime was configured and it did not deliver. Saying "running"
		// here is the one answer that cannot be defended: a client would wait
		// for an address that never comes, on a machine that does not exist.
		res.State = b.FailedState
		return false
	}
	if res.Runtime == nil {
		res.Runtime = map[string]string{}
	}
	res.Runtime[b.RuntimeKey] = started.Machine
	if started.Address != "" {
		res.Runtime[b.AddressKey] = started.Address
	}
	res.State = b.RunningState
	return true
}

// PowerOff stops the machine and withdraws the address.
//
// Withdrawing is not tidiness: an address the API publishes and nothing answers
// on is the defect this whole project exists to avoid, and it is the one a
// stopped machine produces if nobody removes it.
func (b Binding) PowerOff(ctx context.Context, res *resource.Resource) {
	delete(res.Runtime, b.AddressKey)
	b.Stop(ctx, res.ID, res.Runtime[b.RuntimeKey])
}

// Destroy removes the machine, so a leftover cannot outlive the resource that
// justified it.
func (b Binding) Destroy(ctx context.Context, res *resource.Resource) {
	b.Remove(ctx, res.ID, res.Runtime[b.RuntimeKey])
}

// Refresh publishes the address of a machine that has one and has not published
// it yet, and reports whether anything changed so the caller only writes to the
// store when it did.
//
// A container has its address immediately; a virtual machine gets one tens of
// seconds later. So a start never waits, and a pack calls this on the read a
// client is making anyway.
func (b Binding) Refresh(ctx context.Context, res *resource.Resource) bool {
	if res.Runtime[b.AddressKey] != "" {
		return false
	}
	address, found := b.Address(ctx, res.ID, res.Runtime[b.RuntimeKey])
	if !found {
		return false
	}
	if res.Runtime == nil {
		res.Runtime = map[string]string{}
	}
	res.Runtime[b.AddressKey] = address
	return true
}

// RefreshIfRunning fills the address in when the resource is running and has
// none yet. It was written out in all three packs, running-state comparison
// included, which is exactly the line the binding exists to hold.
func (b Binding) RefreshIfRunning(ctx context.Context, res *resource.Resource) bool {
	return res.State == b.RunningState && b.Refresh(ctx, res)
}

// AddressOf is what the machine answers on, empty when nothing is running. A
// pack calls it to fill the field its own API declares for the address — or
// does not call it at all, when its API declares none.
func (b Binding) AddressOf(res *resource.Resource) string {
	return res.Runtime[b.AddressKey]
}

// declaresPackageStep reports whether a client's user data asks cloud-init for
// a package install or update — the one step that needs a route out of the
// machine (#507).
//
// Deliberately a line scan, not a YAML parse: the value is untrusted client
// input, this answer only gates a warning, and a parser here would be a second
// place that interprets cloud-config (the first being cloud-init itself).
// Top-level keys only, so a `packages:` nested under `write_files` content
// does not trigger it; anything that is not a #cloud-config document — a shell
// script, a MIME archive — answers false, because the packages semantics
// belong to cloud-config alone.
func declaresPackageStep(userData string) bool {
	if !strings.HasPrefix(strings.TrimSpace(userData), "#cloud-config") {
		return false
	}
	for line := range strings.SplitSeq(userData, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		// The key is compared raw, indentation included: an indented
		// "packages:" belongs to something nested — a write_files body, a
		// list item — and must not count as the module's own directive.
		switch key {
		case "packages":
			return true
		case "package_update", "package_upgrade":
			if strings.TrimSpace(value) == "true" {
				return true
			}
		}
	}
	return false
}
