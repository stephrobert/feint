package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Incus backs emulated servers with Incus instances: system containers, or real
// KVM virtual machines when VM is set.
//
// This is what makes the emulator useful beyond the control plane. A shared
// kernel cannot test a kernel module, a sysctl, a systemd unit that touches the
// boot path, or anything a hardened image does at startup. `incus launch --vm`
// gives a genuine machine with its own kernel, booted by QEMU/KVM.
//
// It is also the reason the emulated network can be real. Incus managed bridges
// take the block and the gateway the pack computed, and a NIC takes a fixed
// address inside it, so the address the API published is the address the machine
// carries. See docs/limits.md for what the runtime this replaced could not do.
//
// The trade is time: a container is up in under a second, a VM takes tens of
// seconds to boot and to get an address. The driver therefore never blocks on an
// address; the pack refreshes it on the next read.
type Incus struct {
	// Binary is the incus executable.
	Binary string
	// Timeout caps every CLI call. Launching a VM is slower than a container,
	// and pulling an image on a cold host slower still, hence a generous
	// default.
	Timeout time.Duration
	// VM selects real virtual machines over system containers.
	VM bool
	// Remote is the image server, "images" by default.
	Remote string
	// OVN backs emulated subnets with OVN networks instead of managed bridges,
	// which makes two subnets separate by construction instead of separated by
	// reject rules. It requires ovn-central and ovn-host on the host; see
	// incus_ovn.go for what changes.
	OVN bool
	// Uplink is the bridge OVN networks reach the outside through, created on
	// first use. Empty means DefaultUplinkName.
	Uplink string
	// UplinkCIDR is the block the uplink carries. Empty means
	// DefaultUplinkCIDR.
	UplinkCIDR string

	// runner replaces the CLI call, and is only ever set by a test. It exists
	// because a handful of decisions in this driver are invisible to the
	// conformance suite until they hurt: the default-action keys that must not
	// reach an OVN NIC cost the guest its addresses, silently and only sometimes,
	// and a rule set missing from an inherited interface leaves the machine open
	// on an address nothing published. Those are argument-level facts, and this
	// is what lets a test read the arguments.
	//
	// It proves what the driver sends, never what the runtime accepts. That
	// second half is the conformance suite's job and cannot be faked.
	runner func(ctx context.Context, args ...string) ([]byte, error)

	// statPath replaces the filesystem lookup Verify uses to decide whether the
	// OVN northbound socket exists, and is only ever set by a test. Existence,
	// never a connection: the socket is root-owned (srwxr-x--- root root) and
	// incusd is what talks to it, so a process running as the operator would be
	// refused on a host where OVN works perfectly.
	statPath func(string) (os.FileInfo, error)

	// agentPoll overrides how often a virtual machine is asked whether its
	// agent answers. Only a test sets it, for the same reason runner exists:
	// waiting two seconds per probe would make the suite pay for a property it
	// can hold in milliseconds.
	agentPoll time.Duration

	// verified is what the host answered when Verify ran, and it is what
	// Capabilities publishes once it is set. Nil means nobody asked, which is
	// the case in a test that builds a driver directly: the declared set is then
	// the honest answer, because no host was consulted to contradict it.
	verified *Capabilities

	// attachMu serialises interface allocation. Attach reads the device list and
	// then adds a device under a name it believes free; two concurrent calls
	// without the lock pick the same name, and the loser's attachment silently
	// becomes the winner's.
	attachMu sync.Mutex
	// defaultNetMu serialises the creation of the default machine network: two
	// concurrent first boots would otherwise both see it absent and the loser's
	// `network create` would fail the launch it was only preparing.
	defaultNetMu sync.Mutex
	// uplinkMu serialises edits of the uplink's ipv4.routes, one value shared
	// by every routed public address.
	uplinkMu sync.Mutex
}

// NewIncus returns a driver launching system containers.
func NewIncus() *Incus {
	return &Incus{Binary: "incus", Timeout: 120 * time.Second, Remote: "images"}
}

// NewIncusVM returns a driver launching KVM virtual machines.
func NewIncusVM() *Incus {
	d := NewIncus()
	d.VM = true
	return d
}

// NewIncusOVN returns a driver launching system containers on OVN-backed
// networks. It needs ovn-central and ovn-host installed and wired to the
// local Open vSwitch; the first network creation says so when they are not.
func NewIncusOVN() *Incus {
	d := NewIncus()
	d.OVN = true
	return d
}

// Name implements Driver.
func (d *Incus) Name() string {
	switch {
	case d.OVN && d.VM:
		return "incus-ovn-vm"
	case d.OVN:
		return "incus-ovn"
	case d.VM:
		return "incus-vm"
	default:
		return "incus"
	}
}

// Available implements Driver: it queries the daemon, since an installed client
// with no reachable server is the common broken case.
func (d *Incus) Available(ctx context.Context) bool {
	_, err := d.run(ctx, "list", "--format", "json")
	return err == nil
}

// imageRef translates the logical image a pack asks for ("ubuntu:22.04") into an
// Incus image reference ("images:ubuntu/22.04"). Packs stay runtime-agnostic:
// they name an operating system, each driver knows how to obtain it.
func (d *Incus) imageRef(image string) string {
	if strings.Contains(image, "/") {
		return image // already an Incus reference, e.g. images:debian/13
	}
	remote := d.Remote
	if remote == "" {
		remote = "images"
	}
	name, version, found := strings.Cut(image, ":")
	if !found {
		version = "latest"
	}

	// The /cloud variant is mandatory, not a preference: it is the one that runs
	// cloud-init, so it is the only one that can receive the default account and
	// its keys, and it carries the Incus agent virtual machines need.
	//
	// It ships no ssh daemon. Measured on four upstream images, each looked at
	// twice, and all four answered ABSENT with nothing on port 22. That is why
	// resolveImage below prefers the emulator's own build when it exists: the
	// cloud-config still installs openssh-server, and an install at first boot is
	// what forces a machine to have outbound internet (#203).
	return fmt.Sprintf("%s:%s/%s/cloud", remote, name, version)
}

// resolveImage answers the image to boot: the emulator's own build when the host
// holds it, the upstream one otherwise.
//
// The preference is the whole point of #203. An image feint built carries an ssh
// daemon, so a machine from it answers on port 22 without reaching a package
// repository — and a machine that needs no outbound needs no NAT, and therefore
// no interface beyond the ones its provider's API publishes (#202).
//
// The fallback is deliberate and it is announced, never silent. Refusing to boot
// because `feint images` has not been run would turn a first contact into a
// failure; falling back without a word would reintroduce the boot-time install
// and hide the reason a machine suddenly needs the network. So it degrades, and
// says which and why.
//
// TestTheBuiltImageIsPreferredWhenTheHostHoldsIt fails without this.
func (d *Incus) resolveImage(ctx context.Context, image string) string {
	upstream := d.imageRef(image)
	// An explicit Incus reference is the caller naming an image; honour it.
	if strings.Contains(image, "/") {
		return upstream
	}
	name, version, found := strings.Cut(image, ":")
	if !found {
		return upstream
	}
	alias := ImagePrefix + "/" + name + "/" + version

	held, err := d.LocalImages(ctx)
	if err != nil {
		// Cannot tell: boot what has always worked rather than refuse.
		return upstream
	}
	if _, ok := held[alias]; ok {
		return alias
	}
	slog.Default().Warn("no image of ours for this system, booting the upstream one",
		"image", image, "wanted", alias, "using", upstream,
		"consequence", "the machine installs an ssh daemon at first boot and needs outbound network to do it",
		"fix", "feint images")
	return upstream
}

// DefaultMachineNetwork is the network a machine with no attachments boots on.
//
// It exists because "no attachment" used to mean the operator's default profile
// bridge, and that broke the addressing plane twice over. A route the driver
// writes is refused outside its own networks (mustOwn), so a public address on
// such a machine had nowhere lawful to live. And the firewall had to *override*
// the profile's NIC to cover it, which re-plugs the device after boot and costs
// the guest its DHCP lease with nothing left to renew it — measured on Incus
// 7.2: `incus list` showed RUNNING and no IPv4 at all (#116).
//
// The name satisfies ownedNetwork and fits MaxNetworkNameLen; the label makes
// mustOwn accept it and the sweep remove it.
const DefaultMachineNetwork = NetworkPrefix + "-default"

// DefaultMachineCIDR is the block that network carries. Deliberately obscure,
// next to the OVN uplink's block and for the same reason: a collision with a
// block already routed on the operator's host makes the create fail, and
// failing is better than capturing someone's traffic.
const DefaultMachineCIDR = "10.209.84.0/24"

// ensureDefaultNetwork creates the default machine network once, in whichever
// mode the driver runs: a managed bridge, or an OVN network behind the uplink.
// NAT is on because the machines on it expect outbound access — the rendered
// cloud-init installs an ssh daemon at first boot.
//
// A mode switch leaves the network behind as the wrong type — a bridge after
// `--vm incus`, when `--vm incus-ovn` now needs an OVN network — and
// EnsureNetwork rightly refuses to reuse it, which would fail every boot
// until somebody swept. So a wrong-typed default network is replaced here,
// under three conditions and all three: it carries the emulator's own label,
// it is empty, and it is this constant's name. An operator's network, or one
// with a machine still on it, is never touched.
// TestTheDefaultNetworkFollowsTheMode fails without the replacement, and
// without either refusal.
func (d *Incus) ensureDefaultNetwork(ctx context.Context) error {
	d.defaultNetMu.Lock()
	defer d.defaultNetMu.Unlock()

	wantType := "bridge"
	if d.OVN {
		wantType = "ovn"
	}
	if out, err := d.run(ctx, "query", "/1.0/networks/"+DefaultMachineNetwork); err == nil {
		var existing struct {
			Type   string            `json:"type"`
			Config map[string]string `json:"config"`
			UsedBy []string          `json:"used_by"`
		}
		if json.Unmarshal(out, &existing) == nil &&
			existing.Type != wantType &&
			existing.Config["user."+LabelKey] != "" &&
			len(existing.UsedBy) == 0 {
			if err := d.RemoveNetwork(ctx, DefaultMachineNetwork); err != nil {
				return err
			}
		}
	}
	return d.EnsureNetwork(ctx, NetworkSpec{
		Name:   DefaultMachineNetwork,
		CIDR:   DefaultMachineCIDR,
		NAT:    true,
		Labels: map[string]string{LabelKey: "feint"},
	})
}

// publicRouteKey is the device key that routes a public address to a NIC:
// nic_bridged applies ipv4.routes host-side, an OVN NIC carries the same
// intent as ipv4.routes.external (l2proxy answers ARP on the uplink).
func (d *Incus) publicRouteKey() string {
	if d.OVN {
		return "ipv4.routes.external"
	}
	return "ipv4.routes"
}

// Start implements Driver.
//
// The instance is initialised cold, its devices configured, then started —
// rather than launched in one step — because every device key must be in place
// before the first boot. Editing a route key on a live OVN NIC re-plugs the
// device and the guest loses its DHCP lease with nothing left to renew it; a
// cold `config device set` costs nothing on either NIC kind.
func (d *Incus) Start(ctx context.Context, spec Spec) (Machine, error) {
	if !safeName.MatchString(spec.Name) {
		return Machine{}, fmt.Errorf("invalid machine name %q", spec.Name)
	}

	if _, ok, err := d.Inspect(ctx, spec.Name); err != nil {
		return Machine{}, err
	} else if ok {
		if _, err := d.run(ctx, "start", spec.Name); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "already running") {
			return Machine{}, fmt.Errorf("start instance %s: %w", spec.Name, err)
		}
		return d.inspectOrFail(ctx, spec.Name)
	}

	// What the machine's first interface is, and it is the whole of #202.
	//
	// A machine carries the addresses its provider's API publishes and no
	// others. Measured against real accounts: a Scaleway server has one routed
	// public address and `private_ip: none`, an Exoscale instance has one
	// address. This emulator gave two or three, because a machine with no
	// emulated network to join was put on a managed bridge and took an address
	// there that no API describes.
	//
	// Three cases now, and no invented address in any of them:
	//
	//   an emulated network  the primary interface joins it, as before
	//   only public ones     a routed NIC carries them, with no network under it
	//   neither              no network device at all, which is what a real
	//                        cloud gives a server created with ip=none and no
	//                        private network
	//
	// The bridge was doing a second job nothing wrote down: NAT'd outbound, so
	// cloud-init could install an ssh daemon. #203 removed that job by building
	// images that already carry one, which is what makes this possible.
	attachments := spec.Attachments
	routed := len(attachments) == 0 && len(spec.PublicAddresses) > 0
	bare := len(attachments) == 0 && len(spec.PublicAddresses) == 0

	var primary Attachment
	if len(attachments) > 0 {
		primary = attachments[0]
		if !safeName.MatchString(primary.Network) {
			return Machine{}, fmt.Errorf("invalid network name %q", primary.Network)
		}
		// A pack may ask for the emulator's own network by name — Outscale does,
		// for a Vm in the public Cloud, where the address it receives is
		// published as PrivateIp. Nothing creates it implicitly any more, so the
		// request has to create it.
		if primary.Network == DefaultMachineNetwork {
			if err := d.ensureDefaultNetwork(ctx); err != nil {
				return Machine{}, err
			}
		}
	}

	args := []string{"init", d.resolveImage(ctx, spec.Image), spec.Name}
	if d.VM {
		args = append(args, "--vm")
	}
	// --network creates eth0 on the named network, as the instance's own
	// device, which is what lets the firewall `set` its keys later instead of
	// overriding a profile device — the override is a re-plug too.
	//
	// Skipped for a routed or a bare machine: there is no network to join, and
	// naming one would be the invented address this change removes.
	if !routed && !bare {
		args = append(args, "--network", primary.Network)
	}
	if bare {
		// No profile at all, and the root disk declared by hand.
		//
		// Without this the machine inherits the operator's default profile and
		// lands on their bridge: measured, an Exoscale instance came up on
		// incusbr0 (10.76.154.0/24) carrying an address the pack could not
		// publish. That is the hazard DefaultMachineNetwork was introduced to
		// close, and removing the fallback reopened it from the other side.
		//
		// The first attempt was `-d eth0,type=none`, on the belief that an
		// instance-level device masks the profile's device of the same name.
		// It does not: Incus *merges* them, so `network: incusbr0` from the
		// profile lands on a device declared `type: none` and the create fails
		// with "Invalid device option network". Every machine of the network
		// suite failed to start that way, and the suite reported it as a
		// machine not carrying its address — the symptom two steps from the
		// cause.
		//
		// One key=value per -d, because the flag takes exactly one and
		// accumulates: `-d root,type=disk,path=/` is read as a device type
		// called "disk,path=/".
		args = append(args, "--no-profiles",
			"-d", "root,type=disk",
			"-d", "root,path=/",
			"-d", "root,pool="+d.rootPool(ctx))
	}
	if routed {
		// The guest has to be told. A routed NIC hands the kernel a static
		// address and Incus's generated config says `dhcp` regardless, so
		// without this the interface comes up carrying nothing — measured.
		netcfg, err := routedNetworkConfig(spec.PublicAddresses)
		if err != nil {
			return Machine{}, err
		}
		args = append(args, "--config", "cloud-init.network-config="+netcfg)
	}
	// Labels become user.* config keys, the Incus equivalent of container labels.
	for k, v := range spec.Labels {
		args = append(args, "--config", "user."+k+"="+v)
	}
	// cloud-init is how a real cloud provisions the default account and its
	// keys, and Incus feeds it to containers and virtual machines alike. This is
	// what lets this driver produce a machine you can actually ssh into.
	if spec.CloudInit != "" {
		args = append(args, "--config", "cloud-init.user-data="+spec.CloudInit)
	}
	for _, e := range spec.Env {
		args = append(args, "--config", "environment."+e)
	}
	if _, err := d.run(ctx, args...); err != nil {
		return Machine{}, fmt.Errorf("create instance %s from %s: %w", spec.Name, d.resolveImage(ctx, spec.Image), err)
	}

	// Device keys, while the instance is cold. The address pin and the public
	// routes must both precede the first boot, or the NIC comes up on DHCP and
	// the published address becomes a lie.
	//
	// A failure past this point removes the instance before reporting: an
	// init'ed instance left behind answers the next poweron on the
	// already-exists path and boots without the keys this path was setting —
	// measured in OVN mode, where the half-made machine came up with no route
	// key, no DHCP lease and no ssh daemon, while the API said running.
	if routed {
		device, err := routedDevice(spec.Name, spec.PublicAddresses)
		if err != nil {
			return Machine{}, d.abandonStart(ctx, spec.Name, err)
		}
		if _, err := d.run(ctx, device...); err != nil {
			return Machine{}, d.abandonStart(ctx, spec.Name,
				fmt.Errorf("give %s a routed interface: %w", spec.Name, err))
		}
	}
	if primary.Address != "" {
		if _, err := d.run(ctx, "config", "device", "set", spec.Name, "eth0",
			"ipv4.address="+primary.Address); err != nil {
			return Machine{}, d.abandonStart(ctx, spec.Name,
				fmt.Errorf("pin %s on %s: %w", primary.Address, spec.Name, err))
		}
	}
	if len(spec.PublicAddresses) > 0 && !routed {
		routes := make([]string, 0, len(spec.PublicAddresses))
		for _, address := range spec.PublicAddresses {
			// The uplink must carry the /32 before the device may name it:
			// Incus validates ipv4.routes.external against the uplink's routes
			// and refuses the key outright otherwise — measured, "Uplink
			// network doesn't contain ... in its routes". Bridge mode has no
			// uplink and skips this inside setUplinkRoute's OVN guard.
			if d.OVN {
				if err := d.setUplinkRoute(ctx, address, true); err != nil {
					return Machine{}, d.abandonStart(ctx, spec.Name, err)
				}
			}
			routes = append(routes, address+"/32")
		}
		if _, err := d.run(ctx, "config", "device", "set", spec.Name, "eth0",
			d.publicRouteKey()+"="+strings.Join(routes, ",")); err != nil {
			return Machine{}, d.abandonStart(ctx, spec.Name, fmt.Errorf("route %s to %s: %w",
				strings.Join(spec.PublicAddresses, ", "), spec.Name, err))
		}
	}

	if _, err := d.run(ctx, "start", spec.Name); err != nil {
		return Machine{}, d.abandonStart(ctx, spec.Name,
			fmt.Errorf("start instance %s: %w", spec.Name, err))
	}
	if err := d.attachExtra(ctx, spec); err != nil {
		return Machine{}, err
	}
	return d.inspectOrFail(ctx, spec.Name)
}

// abandonStart removes an instance a failed Start leaves behind, and returns
// the failure that caused it. Best-effort: the original error is the one the
// caller must hear, and a delete that fails leaves exactly the state it found.
//
// TestAFailedStartLeavesNoHalfMadeInstance fails without this.
func (d *Incus) abandonStart(ctx context.Context, name string, cause error) error {
	_, _ = d.run(ctx, "delete", "--force", name)
	return cause
}

// attachExtra adds the interfaces beyond the first, which launch cannot carry:
// it takes one --network. A server with two private NICs is ordinary on
// Scaleway, so this is not an edge case.
func (d *Incus) attachExtra(ctx context.Context, spec Spec) error {
	if len(spec.Attachments) < 2 {
		return nil
	}
	for i, att := range spec.Attachments[1:] {
		if !safeName.MatchString(att.Network) {
			return fmt.Errorf("invalid network name %q", att.Network)
		}
		device := fmt.Sprintf("eth%d", i+1)
		args := []string{"config", "device", "add", spec.Name, device, "nic", "network=" + att.Network}
		if att.Address != "" {
			args = append(args, "ipv4.address="+att.Address)
		}
		if _, err := d.run(ctx, args...); err != nil {
			return fmt.Errorf("attach instance %s to network %s: %w", spec.Name, att.Network, err)
		}
	}
	return nil
}

// Attach implements Driver.
//
// Two steps, and the second is the one that is easy to miss. Adding the device
// reserves the address on the managed bridge, which is all `ipv4.address` does:
// a NIC added to a running machine has no DHCP client on it, so the guest keeps
// answering on nothing. The address the control plane published would then be a
// promise nobody keeps, which is the whole defect this emulator exists to avoid.
// So the interface is brought up inside the guest as well, and a failure there
// is reported rather than swallowed: the caller is about to publish the address,
// and its log is the only place an operator can learn the machine never took it.
//
// Idempotence is by effect, not by error text: a device already carrying the
// network is this attachment, and matching "already exists" instead would also
// swallow the case where a concurrent Attach stole the device name, leaving the
// machine off a network the control plane says it is on.
func (d *Incus) Attach(ctx context.Context, name string, att Attachment) error {
	if !safeName.MatchString(name) {
		return fmt.Errorf("invalid machine name %q", name)
	}
	if !safeName.MatchString(att.Network) {
		return fmt.Errorf("invalid network name %q", att.Network)
	}

	d.attachMu.Lock()
	defer d.attachMu.Unlock()

	// A virtual machine is ready for a device once its agent answers, and not
	// when `incus start` returns. Adding one while the firmware is still
	// enumerating the bus fails intermittently — measured twice on the same
	// code: once the device was added and only its address was missing, once
	// the add itself was refused with "PCI: slot 0 function 0 not available for
	// virtio-net-pci, in use by virtio-balloon-pci". Incus documents NIC
	// hotplug as supported for VMs, so the timing was the difference, not the
	// capability. A stopped machine is not waited for: attaching cold is the
	// ordinary Terraform order and needs no agent.
	//
	// TestAVirtualMachineWaitsBeforeAddingADevice fails without this.
	if err := d.waitForAgent(ctx, name); err != nil {
		return fmt.Errorf("wait for %s to be ready for a device: %w", name, err)
	}

	devices, err := d.instanceDevices(ctx, name)
	if err != nil {
		return fmt.Errorf("inspect %s before attaching: %w", name, err)
	}

	device := ""
	for devName, cfg := range devices.own {
		if cfg["type"] == "nic" && cfg["network"] == att.Network {
			device = devName
			break
		}
	}
	switch {
	case device == "":
		device = freeInterface(devices.expanded)
		args := []string{"config", "device", "add", name, device, "nic", "network=" + att.Network}
		if att.Address != "" {
			args = append(args, "ipv4.address="+att.Address)
		}
		if _, err := d.run(ctx, args...); err != nil {
			return fmt.Errorf("attach %s to network %s: %w", name, att.Network, err)
		}
	case att.Address != "" && devices.own[device]["ipv4.address"] != att.Address:
		// Re-attached at a different address: the reservation must follow, or
		// the bridge keeps handing the machine the address of a previous life.
		if _, err := d.run(ctx, "config", "device", "set", name, device, "ipv4.address="+att.Address); err != nil {
			return fmt.Errorf("move %s to %s on network %s: %w", name, att.Address, att.Network, err)
		}
	}

	if att.Address != "" && att.PrefixLen > 0 {
		// The device is attached by now, whatever happens next. Configuring it
		// inside the guest needs a running machine, and attaching to a stopped
		// one is the ordinary Terraform order — attach, then power on — where
		// the address is applied at boot instead. Reporting that as a failed
		// attachment sent operators looking for a problem that was not one.
		err := d.configureGuestAddress(ctx, name, device, fmt.Sprintf("%s/%d", att.Address, att.PrefixLen))
		if err != nil && !isNotRunning(err) {
			return err
		}
		// On OVN the peered subnets are only reachable through the network's
		// router, and a statically configured NIC knows nothing about them.
		if d.OVN {
			if err := d.installGuestPrivateRoutes(ctx, name, att.Network, device); err != nil {
				return err
			}
		}
	}
	return d.reconcileSecondary(ctx, name, device, att)
}

// reconcileSecondary makes the interface carry exactly att.Secondary beside its
// primary address: the ones that arrived are added, the ones that left are
// removed.
//
// Reconciled rather than appended, and that is the whole of it. A driver that
// only added would leave an address on the machine after its API said it was
// unlinked — an address nothing publishes, which is the defect #202 exists to
// prevent, reintroduced through a different door.
//
// The primary is never touched here. It is applied above and it belongs to the
// interface; removing it would take the machine off its own network.
//
// TestSecondaryAddressesAreReconciledNotAppended fails without this.
func (d *Incus) reconcileSecondary(ctx context.Context, name, device string, att Attachment) error {
	if att.PrefixLen == 0 {
		// No mask, so nothing can be configured inside the guest: the same
		// condition the primary address is applied under.
		return nil
	}
	iface, err := d.guestInterface(ctx, name, device)
	if err != nil {
		// Not running, most often, which is the ordinary attach-then-power-on
		// order. The addresses are stored and land at the next attach.
		if isNotRunning(err) {
			return nil
		}
		return err
	}

	carried, err := d.guestAddresses(ctx, name, iface)
	if err != nil {
		if isNotRunning(err) {
			return nil
		}
		return err
	}

	want := map[string]bool{}
	for _, address := range att.Secondary {
		want[address] = true
	}
	for _, address := range att.Secondary {
		if carried[address] {
			continue
		}
		cidr := fmt.Sprintf("%s/%d", address, att.PrefixLen)
		if err := d.configureGuestAddress(ctx, name, device, cidr); err != nil && !isNotRunning(err) {
			return fmt.Errorf("give %s to %s: %w", cidr, name, err)
		}
	}
	for address := range carried {
		if want[address] || address == att.Address {
			continue
		}
		cidr := fmt.Sprintf("%s/%d", address, att.PrefixLen)
		if _, err := d.run(ctx, "exec", name, "--",
			"ip", "address", "del", cidr, "dev", iface); err != nil && !isNotRunning(err) {
			return fmt.Errorf("take %s off %s: %w", cidr, name, err)
		}
	}
	return nil
}

// guestAddresses reads the IPv4 addresses an interface carries inside the guest.
//
// Read from the guest rather than from the device's config, because the config
// holds what the driver asked for and this needs what the machine has. The two
// disagreeing is exactly the case a reconcile is for.
func (d *Incus) guestAddresses(ctx context.Context, name, iface string) (map[string]bool, error) {
	out, err := d.run(ctx, "exec", name, "--", "ip", "-4", "-o", "addr", "show", "dev", iface)
	if err != nil {
		return nil, err
	}
	carried := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field != "inet" || i+1 >= len(fields) {
				continue
			}
			address, _, found := strings.Cut(fields[i+1], "/")
			if found && address != "" {
				carried[address] = true
			}
		}
	}
	return carried, nil
}

// configureGuestAddress gives the guest the address its NIC device reserves,
// and brings the interface up. Reserving an address on the network is not the
// same as the guest carrying it: a NIC added or re-added on a running machine
// has no DHCP client on it, so the driver configures it directly.
func (d *Incus) configureGuestAddress(ctx context.Context, name, device, cidr string) error {
	// The device name is Incus's, not the guest's. A container sees `eth1`
	// because Incus names the veth end; a virtual machine sees `enp6s0`,
	// because the kernel names a PCI device. Passing the Incus name into the
	// guest was a no-op there: an audit measured a VM carrying its bridge
	// address and loopback and never the address the API had published, for
	// three minutes, while the device on the host correctly held the
	// reservation.
	//
	// TestAVirtualMachineIsConfiguredOnItsOwnInterfaceName fails without this.
	iface, err := d.guestInterface(ctx, name, device)
	if err != nil {
		return err
	}
	if _, err := d.run(ctx, "exec", name, "--", "ip", "address", "add", cidr, "dev", iface); err != nil &&
		// Already-there is the outcome this call wants, in whichever wording
		// the guest's `ip` uses (addressAlreadyThere: busybox differs).
		!addressAlreadyThere(err) {
		return fmt.Errorf("give %s to %s inside the guest: %w", cidr, name, err)
	}
	if _, err := d.run(ctx, "exec", name, "--", "ip", "link", "set", iface, "up"); err != nil {
		return fmt.Errorf("bring %s up inside %s: %w", iface, name, err)
	}
	return nil
}

// agentPoll is how often a virtual machine is asked whether its agent answers,
// and agentWait is how long that is worth doing. A VM boots in tens of seconds;
// beyond a minute the machine has a problem the caller should hear about rather
// than wait through.
const (
	agentPoll = 2 * time.Second
	agentWait = 90 * time.Second
)

// waitForAgent blocks until a virtual machine answers `incus exec`, and does
// nothing at all for a container.
//
// TestAVirtualMachineWaitsBeforeAddingADevice fails without this.
func (d *Incus) waitForAgent(ctx context.Context, name string) error {
	if !d.VM {
		return nil
	}
	poll := d.agentPoll
	if poll <= 0 {
		poll = agentPoll
	}
	deadline := time.Now().Add(agentWait)
	for {
		_, err := d.run(ctx, "exec", name, "--", "true")
		if err == nil {
			return nil
		}
		if isNotRunning(err) {
			// Nothing to wait for: the device is added cold and the address is
			// applied at boot. This is the ordinary Terraform order.
			return nil
		}
		if !isAgentNotReady(err) {
			// Anything else is the caller's problem, reported rather than
			// waited through: a machine that is gone will not come back.
			return fmt.Errorf("reach the agent of %s: %w", name, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the agent of %s did not answer within %s", name, agentWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// guestInterface answers what the guest calls the interface Incus knows as
// device.
//
// For a container the two names are the same and this costs one lookup that
// confirms it. For a virtual machine they are never the same, and the link is
// the MAC address: Incus reports it per interface in the instance state, keyed
// by the guest's own name, and the device carries the same value.
func (d *Incus) guestInterface(ctx context.Context, name, device string) (string, error) {
	if !d.VM {
		return device, nil
	}
	out, err := d.run(ctx, "query", "/1.0/instances/"+name+"/state")
	if err != nil {
		return "", fmt.Errorf("read the state of %s to name its interfaces: %w", name, err)
	}
	var state struct {
		Network map[string]struct {
			HWAddr string `json:"hwaddr"`
		} `json:"network"`
	}
	if err := json.Unmarshal(out, &state); err != nil {
		return "", fmt.Errorf("decode the state of %s: %w", name, err)
	}
	wanted, err := d.deviceMAC(ctx, name, device)
	if err != nil {
		return "", err
	}
	for iface, cfg := range state.Network {
		if iface == "lo" {
			continue
		}
		if strings.EqualFold(cfg.HWAddr, wanted) {
			return iface, nil
		}
	}
	// Refused rather than guessed: configuring the wrong interface would put
	// the address on another network, and the caller is about to publish it.
	return "", fmt.Errorf("no interface in %s carries the address of device %s (%s)", name, device, wanted)
}

// deviceMAC reads the hardware address Incus gave a device. It is the only
// value both sides agree on: the host knows the device by name, the guest
// knows the interface by name, and neither name matches on a virtual machine.
func (d *Incus) deviceMAC(ctx context.Context, name, device string) (string, error) {
	out, err := d.run(ctx, "config", "device", "get", name, device, "hwaddr")
	if err == nil {
		if mac := strings.TrimSpace(string(out)); mac != "" {
			return mac, nil
		}
	}
	// A device that does not declare one gets a generated address, and Incus
	// keeps it in the instance's volatile configuration rather than in the
	// device: `volatile.<device>.hwaddr`. Reading only the device found nothing
	// and refused every attachment with "declares no hardware address",
	// measured on three consecutive virtual machines.
	raw, err := d.run(ctx, "query", "/1.0/instances/"+name)
	if err != nil {
		return "", fmt.Errorf("read %s to find the address of device %s: %w", name, device, err)
	}
	var instance struct {
		Config          map[string]string            `json:"config"`
		ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
		Devices         map[string]map[string]string `json:"devices"`
	}
	if err := json.Unmarshal(raw, &instance); err != nil {
		return "", fmt.Errorf("decode %s: %w", name, err)
	}
	for _, set := range []map[string]map[string]string{instance.Devices, instance.ExpandedDevices} {
		if mac := set[device]["hwaddr"]; mac != "" {
			return mac, nil
		}
	}
	if mac := instance.Config["volatile."+device+".hwaddr"]; mac != "" {
		return mac, nil
	}
	return "", fmt.Errorf("device %s of %s declares no hardware address", device, name)
}

// instanceView is one machine's devices: its own, and the ones a profile adds.
type instanceView struct {
	own      map[string]map[string]string
	expanded map[string]map[string]string
}

// instanceDevices reads both device sets in one call. Attach reuses and edits
// only own devices, because profile devices belong to the operator; free names
// are picked against the expanded set, because a profile's eth0 is just as taken.
func (d *Incus) instanceDevices(ctx context.Context, name string) (instanceView, error) {
	out, err := d.run(ctx, "query", "/1.0/instances/"+name)
	if err != nil {
		return instanceView{}, err
	}
	var raw struct {
		Devices         map[string]map[string]string `json:"devices"`
		ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return instanceView{}, fmt.Errorf("decode devices of %s: %w", name, err)
	}
	return instanceView{own: raw.Devices, expanded: raw.ExpandedDevices}, nil
}

// freeInterface returns the first ethN no device uses. Interface names are what
// the guest sees, so they have to look like interface names; deriving one from
// the network name produces something no init script recognises. The match is
// on the exact name: a substring check would let an existing eth10 shadow eth1.
//
// From eth0, not from eth1. It used to start at 1 on the assumption that the
// boot interface always holds eth0, which stopped being true when a machine
// with nothing to publish started booting with no interface at all (#202): the
// first NIC attached afterwards was named eth1, the guest had no such device,
// and configuring it failed with `Cannot find device "eth1"`. The suite
// reported that as a machine not carrying its address, two steps from the
// cause. The loop already skips a name in use, so starting at 0 changes nothing
// for a machine that does have eth0.
func freeInterface(devices map[string]map[string]string) string {
	for i := 0; i < 64; i++ {
		candidate := fmt.Sprintf("eth%d", i)
		if _, used := devices[candidate]; !used {
			return candidate
		}
	}
	return "eth63"
}

// EnsureNetwork implements Driver. It creates a managed bridge carrying the
// block the pack computed — or an OVN network in OVN mode — and succeeds when
// the network is already there.
//
// Incus wants the gateway address with the block's mask, not the block itself,
// so "10.0.0.0/24" with gateway "10.0.0.1" becomes ipv4.address=10.0.0.1/24.
// IPv6 is turned off rather than left to the runtime: an emulated subnet
// publishes the addresses it allocated, and an unannounced IPv6 address on the
// same NIC is a second address the control plane knows nothing about.
func (d *Incus) EnsureNetwork(ctx context.Context, spec NetworkSpec) error {
	if !safeName.MatchString(spec.Name) {
		return fmt.Errorf("invalid network name %q", spec.Name)
	}
	// Caught here rather than left to the runtime: Incus answers "Network
	// interface is too long", which says nothing about which name to fix.
	if len(spec.Name) > MaxNetworkNameLen {
		return fmt.Errorf("network name %q is %d characters, the limit is %d (use NetworkName)",
			spec.Name, len(spec.Name), MaxNetworkNameLen)
	}
	address, err := gatewayAddress(spec.CIDR, spec.Gateway)
	if err != nil {
		return err
	}
	wantType := "bridge"
	if d.OVN {
		wantType = "ovn"
	}
	// An existing network is only "ensured" if it carries the block asked for,
	// as the type the mode wants. Reusing one that answers to the same name
	// with another block would give every machine on it an address outside the
	// subnet the API published; reusing a bridge in OVN mode would quietly give
	// up the isolation the mode exists for.
	if out, err := d.run(ctx, "query", "/1.0/networks/"+spec.Name); err == nil {
		var existing struct {
			Type   string            `json:"type"`
			Config map[string]string `json:"config"`
		}
		if err := json.Unmarshal(out, &existing); err != nil {
			return fmt.Errorf("decode network %s: %w", spec.Name, err)
		}
		if existing.Type != wantType {
			return fmt.Errorf("network %s already exists as %s, not %s; refusing to reuse it",
				spec.Name, existing.Type, wantType)
		}
		if got := existing.Config["ipv4.address"]; got != address {
			return fmt.Errorf("network %s already exists carrying %s, not %s; refusing to reuse it",
				spec.Name, got, address)
		}
		return nil
	} else if !isNotFound(err) {
		return fmt.Errorf("inspect network %s: %w", spec.Name, err)
	}

	args := []string{"network", "create", spec.Name}
	if d.OVN {
		// The uplink is what an OVN network draws its router address from;
		// without one the create is refused outright.
		if err := d.ensureUplink(ctx); err != nil {
			return err
		}
		// The block has to be delegated before the network that carries it: an
		// OVN network outside its uplink's routes is refused outright.
		if err := d.delegateRoute(ctx, spec.CIDR); err != nil {
			return err
		}
		args = append(args, "--type=ovn", "network="+d.uplinkName())
	}
	args = append(args,
		"ipv4.address="+address,
		"ipv4.nat="+strconv.FormatBool(spec.NAT),
		"ipv6.address=none",
	)
	for k, v := range spec.Labels {
		args = append(args, "user."+k+"="+v)
	}
	if _, err := d.run(ctx, args...); err != nil {
		return fmt.Errorf("create network %s (%s): %w", spec.Name, address, err)
	}
	return nil
}

// RemoveNetwork implements Driver. Incus refuses to delete a network still in
// use, and that refusal is propagated rather than forced: a subnet whose
// machines are still running must not be deletable, which is what the client
// expects and what a DependencyViolation is for.
//
// A peering counts as "in use" too, and that one is not the client's business:
// the emulator created it, so it goes first, and only then is a remaining
// refusal a real dependency. The retry is deliberate — dropping the peers of a
// network whose machines block the delete anyway would still be undone by the
// next reconciliation, so nothing is lost by trying.
func (d *Incus) RemoveNetwork(ctx context.Context, name string) error {
	// Ours, or nothing happens. This path had no check at all, not even safeName,
	// and a restored state names the network it deletes.
	if !safeName.MatchString(name) || !ownedNetwork(name) {
		return fmt.Errorf("refusing to delete network %q: not one the emulator created", name)
	}
	if _, err := d.run(ctx, "network", "delete", name); err != nil {
		if isNotFound(err) {
			return nil
		}
		if d.OVN && strings.Contains(strings.ToLower(err.Error()), "in use") {
			if peers, peersErr := d.networkPeers(ctx, name); peersErr == nil && len(peers) > 0 {
				for _, peer := range peers {
					_, _ = d.run(ctx, "network", "peer", "delete", name, peer.Name)
				}
				if _, err := d.run(ctx, "network", "delete", name); err == nil || isNotFound(err) {
					return nil
				}
			}
		}
		return fmt.Errorf("delete network %s: %w", name, err)
	}
	return nil
}

// gatewayAddress renders the gateway in the form Incus expects. An empty
// gateway defaults to the first usable address of the block, which is what
// every cloud does and what the pack's allocator reserves.
func gatewayAddress(cidr, gateway string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", fmt.Errorf("parse network CIDR %q: %w", cidr, err)
	}
	addr := prefix.Masked().Addr().Next()
	if gateway != "" {
		addr, err = netip.ParseAddr(gateway)
		if err != nil {
			return "", fmt.Errorf("parse network gateway %q: %w", gateway, err)
		}
		if !prefix.Contains(addr) {
			return "", fmt.Errorf("gateway %s is outside %s", gateway, cidr)
		}
	}
	return fmt.Sprintf("%s/%d", addr, prefix.Bits()), nil
}

// Stop implements Driver.
func (d *Incus) Stop(ctx context.Context, name string) error {
	if !safeName.MatchString(name) {
		return fmt.Errorf("refusing to stop %q: not a name this emulator creates", name)
	}
	if _, ok, err := d.Inspect(ctx, name); err != nil || !ok {
		return err
	}
	if _, err := d.run(ctx, "stop", "--force", name); err != nil {
		return fmt.Errorf("stop instance %s: %w", name, err)
	}
	return nil
}

// Remove implements Driver.
//
// Stopped first, then deleted, so a machine still initialising is torn down in
// order rather than deleted under its own init. This does not prevent the
// transient ERROR rows in `incus list`: those are the daemon racing any stop of
// a running instance — while the stop operation holds the instance lock the
// daemon still answers "Running", a concurrent list walks /proc of the dead
// init, fails to render the state, and falls back to a bare ERROR placeholder
// until the stop completes (measured on incus 7.2, and visible in the daemon's
// own instances_get.go). The watch stream explains it in our log instead.
func (d *Incus) Remove(ctx context.Context, name string) error {
	// Checked here and not only at creation: the name reaches this function from
	// the store, and the store can be replaced wholesale over PUT /_feint/state.
	// Without this, a crafted snapshot chose which instance the operator's own
	// credentials would delete.
	if !safeName.MatchString(name) {
		return fmt.Errorf("refusing to remove %q: not a name this emulator creates", name)
	}
	if _, err := d.run(ctx, "stop", "--force", name); err != nil && !isNotFound(err) {
		// Not fatal: an instance that refuses to stop must still be deletable,
		// which is what --force below is for.
		_ = err
	}
	if _, err := d.run(ctx, "delete", "--force", name); err != nil {
		// Asked, not guessed: a delete can fail for reasons whose message also
		// contains "not found" ("Storage pool not found" was one), and treating
		// those as success left the instance running while the caller believed
		// it gone.
		if gone, checkErr := d.gone(ctx, "/1.0/instances/"+name); checkErr == nil && gone {
			return nil
		}
		return fmt.Errorf("delete instance %s: %w", name, err)
	}
	return nil
}

// gone reports whether the object at the endpoint no longer exists, by asking
// the daemon rather than by reading its prose.
func (d *Incus) gone(ctx context.Context, endpoint string) (bool, error) {
	if _, err := d.run(ctx, "query", endpoint); err != nil {
		if isNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// WaitRunning blocks until the machine is up and carries an address, or until
// the context is done.
//
// Start deliberately does not wait: a virtual machine takes tens of seconds to
// boot, and an API call must not. But a caller that needs to talk to the machine
// does have to wait for it, and polling in a shell script with sleep is how a
// suite becomes both slow and flaky. Whoever needs readiness asks for it.
func (d *Incus) WaitRunning(ctx context.Context, name string) (Machine, error) {
	const interval = 250 * time.Millisecond

	for {
		machine, ok, err := d.Inspect(ctx, name)
		switch {
		case err != nil:
			return Machine{}, err
		case ok && machine.Running && machine.IP != "":
			return machine, nil
		}

		select {
		case <-ctx.Done():
			return Machine{}, fmt.Errorf("waiting for %s: %w", name, ctx.Err())
		case <-time.After(interval):
		}
	}
}

// Inspect implements Driver. An instance that is running but has not obtained an
// address yet reports an empty IP rather than an error: a booting VM is a normal
// state, not a failure.
func (d *Incus) Inspect(ctx context.Context, name string) (Machine, bool, error) {
	// Read-only, and checked anyway: a name beginning with `-` is read by incus
	// as a flag, so even a list can be turned into something else.
	if !safeName.MatchString(name) {
		return Machine{}, false, nil
	}
	out, err := d.run(ctx, "list", name, "--format", "json")
	if err != nil {
		if isNotFound(err) {
			return Machine{}, false, nil
		}
		return Machine{}, false, fmt.Errorf("list instance %s: %w", name, err)
	}

	var instances []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		State  struct {
			Network map[string]struct {
				Addresses []struct {
					Family  string `json:"family"`
					Address string `json:"address"`
					Scope   string `json:"scope"`
				} `json:"addresses"`
			} `json:"network"`
		} `json:"state"`
	}
	if err := json.Unmarshal(out, &instances); err != nil {
		return Machine{}, false, fmt.Errorf("decode instance list for %s: %w", name, err)
	}

	// `incus list <name>` matches by prefix, so an exact match is required.
	for _, in := range instances {
		if in.Name != name {
			continue
		}
		// Interfaces come back as a map, and a map has no order: publishing the
		// first one Go happens to visit means publishing a different address
		// between two reads of the same machine. Sorted, eth1 (the first private
		// NIC) wins over eth0 only if named so; what matters is that the answer
		// never changes on its own.
		names := make([]string, 0, len(in.State.Network))
		for iface := range in.State.Network {
			if iface != "lo" {
				names = append(names, iface)
			}
		}
		slices.Sort(names)

		ip := ""
		for _, iface := range names {
			for _, addr := range in.State.Network[iface].Addresses {
				if addr.Family == "inet" && addr.Scope == "global" {
					ip = addr.Address
					break
				}
			}
			if ip != "" {
				break
			}
		}
		return Machine{
			Name:    in.Name,
			ID:      in.Name,
			IP:      ip,
			Running: strings.EqualFold(in.Status, "Running"),
		}, true, nil
	}
	return Machine{}, false, nil
}

func (d *Incus) inspectOrFail(ctx context.Context, name string) (Machine, error) {
	m, ok, err := d.Inspect(ctx, name)
	if err != nil {
		return Machine{}, err
	}
	if !ok {
		return Machine{}, fmt.Errorf("instance %s vanished right after starting", name)
	}
	return m, nil
}

func (d *Incus) run(ctx context.Context, args ...string) ([]byte, error) {
	if d.runner != nil {
		return d.runner(ctx, args...)
	}
	binary := d.Binary
	if binary == "" {
		binary = "incus"
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return runCLI(ctx, binary, timeout, args...)
}
