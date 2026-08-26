package machine

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// The property under test: a name that reaches the driver from a restored state
// can never make it touch something the emulator did not create.
//
// This is the test that was missing, and its absence is the whole story. The
// comment on safeName named the exact scenario an audit had reproduced,
// `incus delete --force production-database`, while the regex it documented
// accepted that string: a fix described in prose, never asserted, and therefore
// never a fix. So these cases are written the way the audit attacked, from a
// crafted Runtime name through an ordinary call, and they assert on the argv the
// driver emits rather than on a return value.
//
// Every case here fails if its guard is removed. That is the point of the file.

func quietBinding(driver Driver) Binding {
	return Binding{
		driver:   driver,
		Provider: "scaleway",
		Prefix:   "feint-scw-",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// mentions reports whether any command the driver issued carried the name.
func mentions(f *fakeRuntime, name string) bool {
	for _, call := range f.calls {
		for _, arg := range call {
			if strings.Contains(arg, name) {
				return true
			}
		}
	}
	return false
}

func TestStopIgnoresAMachineNameTheEmulatorCouldNotHaveCreated(t *testing.T) {
	f := &fakeRuntime{}
	b := quietBinding(newFakeDriver(f))

	// What a crafted snapshot puts in Resource.Runtime, then an ordinary
	// power-off.
	b.Stop(context.Background(), "srv-1", "production-database")

	if mentions(f, "production-database") {
		t.Fatalf("the driver was pointed at the operator's instance: %v", f.calls)
	}
	if !mentions(f, "feint-scw-srv-1") {
		t.Fatalf("the derived name should have been used instead: %v", f.calls)
	}
}

func TestRemoveIgnoresAMachineNameTheEmulatorCouldNotHaveCreated(t *testing.T) {
	f := &fakeRuntime{}
	b := quietBinding(newFakeDriver(f))

	b.Remove(context.Background(), "srv-1", "production-database")

	if mentions(f, "production-database") {
		t.Fatalf("the driver would have destroyed the operator's instance: %v", f.calls)
	}
}

func TestAddressIgnoresAMachineNameTheEmulatorCouldNotHaveCreated(t *testing.T) {
	f := &fakeRuntime{}
	b := quietBinding(newFakeDriver(f))

	// Read-only, but publishing another instance's address as the emulated
	// server's is still wrong.
	b.Address(context.Background(), "srv-1", "production-database")

	if mentions(f, "production-database") {
		t.Fatalf("the driver inspected the operator's instance: %v", f.calls)
	}
}

func TestRemoveNetworkRefusesANetworkTheEmulatorDidNotCreate(t *testing.T) {
	// incusbr0 is Incus's default bridge, and the audit deleted it this way.
	for _, name := range []string{"incusbr0", "br0", "lxdbr0"} {
		f := &fakeRuntime{}
		d := newFakeDriver(f)

		if err := d.RemoveNetwork(context.Background(), name); err == nil {
			t.Fatalf("%s: expected a refusal", name)
		}
		if mentions(f, name) {
			t.Fatalf("%s: the driver issued a command anyway: %v", name, f.calls)
		}
	}
}

func TestRemoveFirewallRefusesARuleSetTheEmulatorDidNotCreate(t *testing.T) {
	// The rule set exists and is perfectly well formed. What it lacks is the
	// description the emulator writes, which is the only thing that makes one
	// ours: the name cannot say, because a pack chooses that prefix.
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/network-acls/corp-baseline": `{"name": "corp-baseline", "description": "corporate baseline, do not touch"}`,
	}}
	d := newFakeDriver(f)

	if err := d.RemoveFirewall(context.Background(), "corp-baseline"); err == nil {
		t.Fatal("expected a refusal")
	}
	for _, call := range f.calls {
		if len(call) > 1 && call[0] == "network" && call[1] == "acl" {
			t.Fatalf("the driver would have deleted the operator's rule set: %v", f.calls)
		}
	}
}

func TestRemoveFirewallAcceptsTheEmulatorsOwnRuleSets(t *testing.T) {
	// The other half of the guard: it must not refuse what the emulator wrote,
	// including an isolation rule set, which is recognised by name because its
	// description is written by a second call that an interrupted run may miss.
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/network-acls/scw-a1b2c3": `{"description": "feint security group"}`,
	}}
	d := newFakeDriver(f)

	if err := d.RemoveFirewall(context.Background(), "scw-a1b2c3"); err != nil {
		t.Fatalf("a rule set the emulator created must be removable, got %v", err)
	}
	if err := d.RemoveFirewall(context.Background(), isolationACL(NetworkPrefix+"-a1b2c3")); err != nil {
		t.Fatalf("an isolation rule set must be removable, got %v", err)
	}
}

func TestIsolateNetworkRefusesANetworkTheEmulatorDidNotCreate(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f)

	// The audit's third primitive: this path unsets security.acls on the network
	// it is given, which disarms a firewall rather than isolating a subnet.
	err := d.IsolateNetwork(context.Background(), "incusbr0", []string{"fnt-abc"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if mentions(f, "incusbr0") {
		t.Fatalf("the driver touched the operator's bridge: %v", f.calls)
	}
}

func TestPeerNetworksRefusesForeignNetworksOnEitherEnd(t *testing.T) {
	cases := []struct {
		name    string
		network string
		peers   []string
		banned  string
	}{
		{"the network itself", "incusbr0", []string{"fnt-abc"}, "incusbr0"},
		{"a peer", "fnt-abc", []string{"incusbr0"}, "incusbr0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRuntime{}
			d := newFakeDriver(f)
			d.OVN = true

			if err := d.PeerNetworks(context.Background(), tc.network, tc.peers); err == nil {
				t.Fatal("expected a refusal")
			}
			if mentions(f, tc.banned) {
				t.Fatalf("the driver would have joined the operator's network: %v", f.calls)
			}
		})
	}
}

func TestUnrouteAddressRefusesAnInstanceWithoutTheEmulatorsLabel(t *testing.T) {
	// The empty label is what an unlabelled instance looks like: it exists, and
	// it is not ours. Declared rather than left to the fake's silence — since
	// #209 the fake answers "ours" by default, because that is the case every
	// other test is about, and the refusal is the half worth spelling out.
	f := &fakeRuntime{answers: map[string]string{"config get production-database": ""}}
	d := newFakeDriver(f)

	err := d.UnrouteAddress(context.Background(), "production-database", "10.0.0.5")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	// The ownership probe names the instance; nothing that reconfigures it may.
	for _, call := range f.calls {
		if len(call) > 0 && call[0] != "config" {
			t.Fatalf("the driver went past the ownership check: %v", f.calls)
		}
	}
}

func TestOwnedNetworkAcceptsWhatTheEmulatorDerives(t *testing.T) {
	// The guard must not be so tight that it rejects the emulator's own names:
	// a check that refuses everything would pass every test above and break the
	// product.
	name := NetworkName(NetworkPrefix, "b7d1e0f2-0000-4000-8000-000000000001")
	if !ownedNetwork(name) {
		t.Fatalf("NetworkName produced %q, which its own guard rejects", name)
	}
	if len(name) > MaxNetworkNameLen {
		t.Fatalf("%q is longer than a Linux interface name allows", name)
	}
}
