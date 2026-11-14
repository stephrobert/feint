package machine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// RouteAddress had three branches and asked the ownership question in two of
// them (#933).
//
// The routed branch asked it of the instance, the migration asked it of the
// network before releasing the address, and the managed branch — both mode
// halves of it — asked it of the network only, then wrote to the instance:
// `config device set <instance> eth1 ipv4.routes=<address>/32` and the
// `ip address add` behind it, on an instance carrying none of the emulator's
// labels. Measured through the injectable runner, which is the only place an
// argument-level fact like that is visible before it hurts.
//
// The machine name arrives from Resource.Runtime, restored verbatim by
// PUT /_feint/state and `feint snapshot load`, so it is an input from outside
// however internal it looks.
//
// These hold the two questions and the shape check, one test per question,
// each driving a value that passes every other question and fails only its
// own: a row that fails two questions at once cannot tell them apart, and
// neutralising one of them would change no verdict (#909, #932).

// routeRuntime answers a plausible Incus for one RouteAddress call on a
// managed network.
//
// The machine is every Scaleway server once its private NIC exists: a routed
// NIC carrying the launch address 203.0.113.4, and a managed NIC on `network`.
// The address the tests route is 203.0.113.9, which no routed NIC carries, so
// the call lands on the managed branch rather than on the migration.
//
// The two labels are the two ownership questions: `config get <machine>
// user.feint.provider` for the instance, `network get <network>
// user.feint.provider` for the network. Either answers empty when the test
// says the object is not the emulator's, which is what the host's own objects
// answer.
func routeRuntime(machine, network string, oursInstance, oursNetwork bool) *fakeRuntime {
	devices := fmt.Sprintf(`{
	  "eth0": {"type": "nic", "nictype": "routed",
	           "ipv4.address": "203.0.113.4", "ipv4.host_address": "169.254.0.1"},
	  "eth1": {"type": "nic", "network": %q, "ipv4.address": "10.30.1.10"}
	}`, network)
	instance := fmt.Sprintf(`{"devices": %s, "expanded_devices": %s}`, devices, devices)

	label := func(ours bool) []byte {
		if ours {
			return []byte("feint\n")
		}
		return []byte("\n")
	}
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch strings.Join(args, " ") {
		case "query /1.0/instances/" + machine:
			return []byte(instance), nil, true
		case "config get " + machine + " user." + LabelKey:
			return label(oursInstance), nil, true
		case "network get " + network + " user." + LabelKey:
			return label(oursNetwork), nil, true
		case "network get " + network + " ipv4.address":
			return []byte("10.30.1.1/24\n"), nil, true
		case "network get " + DefaultUplinkName + " ipv4.routes":
			return []byte("\n"), nil, true
		case "config device get " + machine + " eth1 ipv4.routes":
			return []byte("\n"), nil, true
		case "list " + machine + " --format json":
			return []byte(`[{"name":"` + machine + `","status":"Running","state":{"network":{}}}]`), nil, true
		}
		return nil, nil, false
	}
	return f
}

// changesIssued returns the commands that change something on the host:
// everything that is not one of the read verbs the driver asks its questions
// with. A guard that returns an error after sending the command has guarded
// nothing, and this is what tells the two apart.
func changesIssued(f *fakeRuntime) []string {
	var out []string
	for _, cmd := range f.commands() {
		read := false
		for _, verb := range []string{"query ", "config get ", "network get ", "list "} {
			if strings.HasPrefix(cmd, verb) {
				read = true
			}
		}
		if !read {
			out = append(out, cmd)
		}
	}
	return out
}

// TestRouteAddressRefusesAManagedNICOnAnInstanceTheEmulatorDidNotCreate is the
// authorisation question, on the branch that did not ask it.
//
// `production-database` is well formed, so safeName lets it through, and the
// network it is routed through is one the emulator created and labelled, so
// the network question passes too: this row fails the instance question and
// nothing else. Only the label the emulator wrote can tell the instance from
// one of ours.
func TestRouteAddressRefusesAManagedNICOnAnInstanceTheEmulatorDidNotCreate(t *testing.T) {
	const foreign = "production-database"
	const network = "fnt-368798629f8"
	for mode, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
		t.Run(mode, func(t *testing.T) {
			f := routeRuntime(foreign, network, false, true)
			d := newFakeDriver(f)
			d.OVN = ovn

			err := d.RouteAddress(context.Background(), AddressSpec{
				Machine: foreign, Address: "203.0.113.9", Network: network,
			})
			if err == nil {
				t.Fatal("an address was routed onto a device of an instance the emulator never created")
			}
			if changed := changesIssued(f); len(changed) != 0 {
				t.Fatalf("the refusal came after the host was changed:\n%s", strings.Join(changed, "\n"))
			}
			// And the name reached no command but the question itself: a
			// device set, an `exec` into the guest or a wait on its agent
			// would each be the defect.
			for _, cmd := range f.commands() {
				if strings.Contains(cmd, foreign) && cmd != "config get "+foreign+" user."+LabelKey {
					t.Errorf("a command reached the operator's own instance: %q\nall commands: %v",
						cmd, f.commands())
				}
			}
		})
	}
}

// TestRouteAddressRefusesAManagedNICOnANetworkTheEmulatorDoesNotOwn is the
// same pair of questions on the other name.
//
// `incusbr0` is the host's default bridge: well formed, so safeName lets it
// through, and the instance here is one of ours and carries the label, so this
// row fails the network question and nothing else. A route left on the
// operator's bridge would survive every sweep.
func TestRouteAddressRefusesAManagedNICOnANetworkTheEmulatorDoesNotOwn(t *testing.T) {
	const mine = "feint-scw-mine"
	for mode, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
		t.Run(mode, func(t *testing.T) {
			f := routeRuntime(mine, "incusbr0", true, false)
			d := newFakeDriver(f)
			d.OVN = ovn

			err := d.RouteAddress(context.Background(), AddressSpec{
				Machine: mine, Address: "203.0.113.9", Network: "incusbr0",
			})
			if err == nil {
				t.Fatal("an address was routed through the host's own bridge")
			}
			if changed := changesIssued(f); len(changed) != 0 {
				t.Fatalf("the refusal came after the host was changed:\n%s", strings.Join(changed, "\n"))
			}
			for _, cmd := range f.commands() {
				if strings.Contains(cmd, "incusbr0") && cmd != "network get incusbr0 user."+LabelKey {
					t.Errorf("a command named the operator's own bridge: %q\nall commands: %v",
						cmd, f.commands())
				}
			}
		})
	}
}

// TestRouteAddressRefusesAMachineNameThatIsNotACommandArgument is the shape
// question, and it needs its own row because the two ownership questions above
// would accept this name: the instance it names is labelled as ours and so is
// the network, and only safeName refuses a name carrying a path separator.
//
// Nothing at all may be sent, not even a read: a name `incus` would read as a
// flag, or as another object's, must not become an argument of any command.
func TestRouteAddressRefusesAMachineNameThatIsNotACommandArgument(t *testing.T) {
	const illFormed = "feint-scw-a/b"
	const network = "fnt-368798629f8"
	for mode, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
		t.Run(mode, func(t *testing.T) {
			f := routeRuntime(illFormed, network, true, true)
			d := newFakeDriver(f)
			d.OVN = ovn

			err := d.RouteAddress(context.Background(), AddressSpec{
				Machine: illFormed, Address: "203.0.113.9", Network: network,
			})
			if err == nil {
				t.Fatalf("a machine name no command may carry was accepted: %q", illFormed)
			}
			if len(f.calls) != 0 {
				t.Fatalf("the name reached the runtime before the refusal:\n%s",
					strings.Join(f.commands(), "\n"))
			}
		})
	}
}

// TestRouteAddressStillRoutesOnAnOwnedInstanceAndNetwork is the accepting
// half, and it is half the point: a guard that refuses everything passes every
// test above and breaks the product. Two networks, because both must keep
// working — the one NetworkName derives for a pack's private network, and
// fnt-default, which is the network Outscale's public-cloud Vm path boots on.
func TestRouteAddressStillRoutesOnAnOwnedInstanceAndNetwork(t *testing.T) {
	derived := NetworkName(NetworkPrefix, "0b9c1d2e-aaaa-bbbb-cccc-111122223333")
	if len(derived) > MaxNetworkNameLen || !ownedNetwork(derived) {
		t.Fatalf("NetworkName produced %q, which the runtime would reject or a guard would refuse", derived)
	}
	const mine = "feint-scw-mine"
	for _, network := range []string{derived, DefaultMachineNetwork} {
		for mode, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
			t.Run(network+" in "+mode, func(t *testing.T) {
				f := routeRuntime(mine, network, true, true)
				d := newFakeDriver(f)
				d.OVN = ovn

				if err := d.RouteAddress(context.Background(), AddressSpec{
					Machine: mine, Address: "203.0.113.9", Network: network,
				}); err != nil {
					t.Fatalf("routing through a network and an instance the emulator created must succeed, got %v", err)
				}
				wanted := []string{
					"config device set " + mine + " eth1 ipv4.routes=203.0.113.9/32",
					"exec " + mine + " -- ip address add 203.0.113.9/32 dev eth1",
				}
				if ovn {
					wanted = []string{
						"network set " + DefaultUplinkName + " ipv4.routes=203.0.113.9/32",
						"config device set " + mine + " eth1 ipv4.routes.external=203.0.113.9/32",
					}
				}
				for _, must := range wanted {
					if len(f.matching(must)) == 0 {
						t.Errorf("the address never reached the machine, missing %q in:\n%s",
							must, strings.Join(f.commands(), "\n"))
					}
				}
			})
		}
	}
}

// TestRouteAddressOnAnInstanceThatIsGoneIsReported holds the one thing an
// ownership probe must not turn into a success. The caller is about to publish
// this address to a client, so an instance that is not there is an error and
// not "nothing to do" — that tolerance belongs to UnrouteAddress, which has an
// uplink route to withdraw either way. The error keeps its meaning, because
// callers read it with isNotFound.
func TestRouteAddressOnAnInstanceThatIsGoneIsReported(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"config get feint-scw-gone": errors.New("Error: Instance not found"),
	}}
	d := newFakeDriver(f)

	err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "feint-scw-gone", Address: "203.0.113.9", Network: "fnt-368798629f8",
	})
	if err == nil {
		t.Fatal("routing an address to an instance that does not exist answered success")
	}
	if !isNotFound(err) {
		t.Fatalf("the error lost its meaning, and callers read it with isNotFound: %v", err)
	}
	if changed := changesIssued(f); len(changed) != 0 {
		t.Fatalf("a command changed something for an instance that is gone:\n%s", strings.Join(changed, "\n"))
	}
}
