package machine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The cold twin of the attach guard (#932).
//
// #909 made Attach, the hot join, ask whether the network is ours. The two cold
// paths that plug the same kind of NIC asked only safeName: the primary
// interface inside Start, and attachExtra for the NICs beyond the first.
// `incusbr0` is well formed, so both let it through.
//
// The name comes from Resource.Runtime, which PUT /_feint/state and
// `feint snapshot load` restore verbatim, and every pack reads it straight off
// into Spec.Attachments. Restoring a private-network resource whose stored
// network is `incusbr0` and issuing an ordinary poweron produced, at argv
// level:
//
//	init images:alpine/3.21/cloud feint-scw-victim --network incusbr0
//	config device add feint-scw-victim eth1 nic network=incusbr0 ipv4.address=192.168.1.250
//
// A machine the emulator started on the operator's own bridge, taking an
// address from their DHCP, and unremovable by the normal path afterwards
// because Detach refuses a network outside the prefix.
//
// These tests are driven through the injectable runner and read the argv the
// driver emitted: a guard that returns an error after sending the command has
// guarded nothing. No Incus daemon is involved and no machine is started.

// startOwnershipSpec is the boot a pack asks for: a name the emulator made and
// the attachments the stored resources named.
func startOwnershipSpec(machine string, networks ...string) Spec {
	atts := make([]Attachment, 0, len(networks))
	for _, n := range networks {
		atts = append(atts, Attachment{Network: n, Address: ""})
	}
	return Spec{Name: machine, Image: "alpine:3.21", Attachments: atts}
}

// TestStartRefusesANetworkOutsideThePrefix is the primary attachment: the name
// that becomes `init … --network <name>`.
//
// The witness is that no command names the network at all, and that nothing at
// all was sent: the guard sits at the entry of Start, ahead of the Inspect, so
// a refused boot leaves the runtime untouched.
//
// The machine name is a parameter, and the last row is the reason, the lesson
// #909 paid for: with every row sharing a machine name that ownedNetwork also
// refuses, the plausible copy-paste `ownedNetwork(spec.Name)` in place of
// `ownedNetwork(att.Network)` refuses every row for the wrong reason and this
// test stays green. The last of those rows gives the machine a name
// ownedNetwork accepts, so the guard has to be reading the network to refuse.
//
// The same reasoning applies between the guard's own two halves, and it had
// the same hole: the condition asks safeName *and* ownedNetwork, and with every
// row naming a network ownedNetwork also refuses, neutralising safeName alone
// left this test green — measured, on this very table. That matters because
// centralising the question deleted the two per-site safeName checks, so this
// condition is the only shape check the boot path has left; after that mutation
// a stored name with a slash or a space reaches `--network`, `network=`,
// d.networkLock and the /1.0/networks/<name> queries. The last two rows are
// well-formed-for-ownedNetwork and refused by safeName alone, so neither half
// can hide behind the other.
func TestStartRefusesANetworkOutsideThePrefix(t *testing.T) {
	for _, c := range []struct{ machine, network string }{
		{"srv", "incusbr0"}, // the operator's default bridge, the measured case
		{"srv", "docker0"},
		{"srv", "hp-test-net"},
		{"srv", "fnt"},     // the prefix without its separator
		{"srv", "fntlike"}, // a name that merely begins with the letters
		{"srv", "br0"},
		// A machine name that is itself a well-formed owned network name, so a
		// guard reading the machine where it should read the network accepts
		// this row and the boot goes through.
		{"fnt-mine", "incusbr0"},
		// Owned prefix, ill-formed name: ownedNetwork accepts these two and
		// only safeName refuses them, so they are what makes the shape half
		// measurable.
		{"srv", "fnt-a/b"},
		{"srv", "fnt-a b"},
	} {
		for _, vm := range []bool{false, true} {
			f := &fakeRuntime{}
			startScript(f)
			d := newFakeDriver(f)
			d.VM = vm

			_, err := d.Start(context.Background(), startOwnershipSpec(c.machine, c.network))
			if err == nil {
				t.Fatalf("vm=%v: Start of %q accepted network %q, which the emulator never created",
					vm, c.machine, c.network)
			}
			if !strings.Contains(err.Error(), c.network) {
				t.Errorf("vm=%v: the refusal does not say which network was refused: %v", vm, err)
			}
			if len(f.calls) != 0 {
				t.Fatalf("vm=%v: the network was refused, so nothing should have been sent; commands: %v",
					vm, f.commands())
			}
			if anyCommandNames(f, c.network) || anyCommandNames(f, "network="+c.network) {
				t.Fatalf("vm=%v: a command named %q despite the refusal; commands: %v",
					vm, c.network, f.commands())
			}
		}
	}
}

// TestStartRefusesAForeignNetworkOnAnExtraNIC is the second half of the same
// defect, and the half a per-call-site check would have missed: the primary
// network is one of ours, the extra NIC's is the host's bridge. attachExtra
// runs after `init` and after `start`, so a guard placed at the attachment site
// would already have created and booted the instance.
//
// The assertion is therefore stronger than a returned error: no init, no start,
// no device add, and no command naming the foreign bridge.
func TestStartRefusesAForeignNetworkOnAnExtraNIC(t *testing.T) {
	owned := NetworkName(NetworkPrefix, "11112222-3333-4444-5555-666677778888")
	f := &fakeRuntime{}
	startScript(f)
	d := newFakeDriver(f)

	_, err := d.Start(context.Background(), startOwnershipSpec("srv", owned, "incusbr0"))
	if err == nil {
		t.Fatal("Start accepted an extra NIC on the operator's own bridge")
	}
	if anyCommandNames(f, "incusbr0") || anyCommandNames(f, "network=incusbr0") {
		t.Fatalf("a command named the host's bridge despite the refusal; commands: %v", f.commands())
	}
	if len(f.matching("config device add")) != 0 {
		t.Fatalf("a device was added despite the refusal: %v", f.commands())
	}
	// And the boot never happened: the refusal is at the entry, not at the
	// attachment site, so there is no half-made instance to clean up.
	if len(f.matching("init ")) != 0 || len(f.matching("start ")) != 0 {
		t.Fatalf("the instance was created or started before the extra NIC was refused: %v", f.commands())
	}
}

// TestStartStillBootsOnTheNetworksTheEmulatorCreated is the accepting half, and
// it is half the point: a guard that refuses everything passes both attack
// tests above and breaks the product.
//
// Two names must pass, and each for its own reason:
//
//   - a NetworkName-derived name, which is what every pack's private network
//     becomes, on the primary interface and on an extra NIC;
//   - DefaultMachineNetwork, which is what Outscale asks for when a Vm sits in
//     the public Cloud and the address it receives is published as PrivateIp.
//     Refusing it would break that path.
func TestStartStillBootsOnTheNetworksTheEmulatorCreated(t *testing.T) {
	first := NetworkName(NetworkPrefix, "11112222-3333-4444-5555-666677778888")
	second := NetworkName(NetworkPrefix, "99990000-aaaa-bbbb-cccc-ddddeeeeffff")
	for _, name := range []string{first, second, DefaultMachineNetwork} {
		if len(name) > MaxNetworkNameLen || !ownedNetwork(name) {
			t.Fatalf("%q is a name the emulator produces, and the guard or the runtime would refuse it", name)
		}
	}

	t.Run("a derived network on both interfaces", func(t *testing.T) {
		f := &fakeRuntime{}
		startScript(f)
		d := newFakeDriver(f)
		// No lease is ever offered by this fixture, and the wait is not the
		// subject here.
		d.routePoll = time.Millisecond
		d.routeBudget = 5 * time.Millisecond

		spec := startOwnershipSpec("srv", first, second)
		if _, err := d.Start(context.Background(), spec); err != nil {
			t.Fatalf("Start on two networks the emulator created must succeed, got %v", err)
		}
		if got := f.matching("--network " + first); len(got) != 1 {
			t.Fatalf("the primary interface did not join %q: %v", first, f.commands())
		}
		if got := f.matching("config device add " + spec.Name + " eth1 nic network=" + second); len(got) != 1 {
			t.Fatalf("the extra NIC was not added on %q: %v", second, f.commands())
		}
	})

	t.Run("the default machine network Outscale asks for", func(t *testing.T) {
		f := &fakeRuntime{}
		startScript(f)
		d := newFakeDriver(f)
		d.routePoll = time.Millisecond
		d.routeBudget = 5 * time.Millisecond

		if _, err := d.Start(context.Background(), startOwnershipSpec("srv", DefaultMachineNetwork)); err != nil {
			t.Fatalf("Start on %s must succeed, or Outscale's public-cloud Vm path breaks: %v",
				DefaultMachineNetwork, err)
		}
		if got := f.matching("--network " + DefaultMachineNetwork); len(got) != 1 {
			t.Fatalf("the machine did not join %s: %v", DefaultMachineNetwork, f.commands())
		}
	})
}

// TestARestartIsRefusedWhenThePlanNamesAForeignNetwork holds a behaviour the
// guard changes and no other test measures: the already-running branch of Start
// emits no network name of its own, so the refusal is not about an argv it
// would have built. It is about the plan.
//
// A poweron of a machine the runtime is already running, whose stored plan
// names a network outside the prefix, is refused before the Inspect that would
// have found the instance. Binding.PowerOn then publishes FailedState for a
// machine that IS running, and that is the intended outcome, not an oversight:
// a plan this driver cannot trust is a plan it does not execute, and the
// alternative is restarting a machine whose own attachments name the operator's
// bridge. The assertion is that nothing was sent at all, the `start` included.
func TestARestartIsRefusedWhenThePlanNamesAForeignNetwork(t *testing.T) {
	// The instance exists and is running throughout, unlike startScript's
	// fixture: this is the branch of Start that only restarts.
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		if args[0] == "list" {
			return []byte(`[{"name":"srv","status":"Running","state":{"network":{}}}]`), nil, true
		}
		return nil, nil, false
	}
	d := newFakeDriver(f)

	_, err := d.Start(context.Background(), startOwnershipSpec("srv", "incusbr0"))
	if err == nil {
		t.Fatal("a restart whose stored plan names the host's own bridge was accepted")
	}
	if len(f.calls) != 0 {
		t.Fatalf("the plan was refused, so nothing should have been sent; commands: %v", f.commands())
	}
	if len(f.matching("start")) != 0 {
		t.Fatalf("the machine was restarted on a plan the driver cannot trust: %v", f.commands())
	}
}
