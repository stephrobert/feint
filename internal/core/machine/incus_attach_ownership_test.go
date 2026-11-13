package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The attach half of the driver asked one question where Detach asks two (#909).
//
// Attach checked the shape of the machine and network names and nothing else,
// so a crafted snapshot naming an instance of the host reached
// `incus config device add <that instance> ethN nic network=...`: an interface
// added to an instance the emulator never created. The names come from
// Resource.Runtime, which PUT /_feint/state and `feint snapshot load` restore
// verbatim.
//
// These are the twins of TestDetachRefusesAnInstanceItDoesNotOwn and
// TestDetachRefusesANetworkOutsideThePrefix, driven through the same injectable
// runner, and each reads the argv the driver emitted: a guard that returns an
// error after sending the command has guarded nothing.

// attachRuntime answers a plausible Incus for an attach. The label is what
// `config get <name> user.feint.provider` answers, per instance: only the
// instances in ours carry one.
func attachRuntime(ours ...string) *fakeRuntime {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/": `{"devices":{},"expanded_devices":{}}`,
	}}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		if len(args) == 4 && args[0] == "config" && args[1] == "get" && args[3] == "user."+LabelKey {
			for _, name := range ours {
				if args[2] == name {
					return []byte("scaleway\n"), nil, true
				}
			}
			return []byte("\n"), nil, true
		}
		return nil, nil, false
	}
	return f
}

// anyCommandNames reports whether any emitted argv carries the string as a
// whole word.
func anyCommandNames(f *fakeRuntime, needle string) bool {
	for _, call := range f.calls {
		for _, arg := range call {
			if arg == needle {
				return true
			}
		}
	}
	return false
}

// TestAttachRefusesAnInstanceItDoesNotOwn is the authorisation question.
// `production-database` is well formed, so safeName lets it through; only the
// label the emulator wrote can tell it from one of ours.
//
// The assertion is that no command carries the name at all, apart from the one
// read that asks about the label. A device add, a wait on the guest agent or an
// `exec` into it would each be the defect.
func TestAttachRefusesAnInstanceItDoesNotOwn(t *testing.T) {
	for _, vm := range []bool{false, true} {
		f := attachRuntime("feint-scw-mine") // not production-database
		d := newFakeDriver(f)
		d.VM = vm

		err := d.Attach(context.Background(), "production-database", Attachment{
			Network: "fnt-net", Address: "10.0.0.4", PrefixLen: 24,
		})
		if err == nil {
			t.Fatalf("vm=%v: Attach accepted an instance carrying none of the emulator's labels", vm)
		}
		for _, call := range f.commands() {
			if !strings.Contains(call, "production-database") {
				continue
			}
			if call != "config get production-database user."+LabelKey {
				t.Fatalf("vm=%v: a command reached an instance the emulator does not own: %q\nall commands: %v",
					vm, call, f.commands())
			}
		}
		if len(f.matching("config device")) != 0 {
			t.Fatalf("vm=%v: a device was touched; commands sent: %v", vm, f.commands())
		}
	}
}

// TestAttachRefusesANetworkOutsideThePrefix is the same question on the other
// name. A snapshot can name the host's own bridge here, and the NIC would be
// plugged into it. The witness is that no command names the bridge at all.
func TestAttachRefusesANetworkOutsideThePrefix(t *testing.T) {
	for _, network := range []string{"incusbr0", "hp-test-net", "docker0", "fnt"} {
		f := attachRuntime("feint-scw-mine")
		d := newFakeDriver(f)

		err := d.Attach(context.Background(), "feint-scw-mine", Attachment{Network: network})
		if err == nil {
			t.Fatalf("Attach accepted %q, which the emulator never created", network)
		}
		if anyCommandNames(f, "network="+network) || anyCommandNames(f, network) {
			t.Fatalf("a command named %q despite the refusal; commands sent: %v", network, f.commands())
		}
		if len(f.calls) != 0 {
			t.Fatalf("the network was refused, so nothing should have been sent; commands sent: %v", f.commands())
		}
	}
}

// TestAttachStillReachesAnInstanceTheEmulatorCreated is the accepting half, and
// it is half the point: a guard that refuses everything passes both attack
// tests above and breaks the product. A machine carrying the label, on a
// network NetworkName derived, gets its NIC.
//
// The foreign instance sits in the same runtime, so the test also shows the
// guard discriminates between two names rather than refusing every name.
func TestAttachStillReachesAnInstanceTheEmulatorCreated(t *testing.T) {
	network := NetworkName(NetworkPrefix, "0b9c1d2e-aaaa-bbbb-cccc-111122223333")
	if len(network) > MaxNetworkNameLen || !ownedNetwork(network) {
		t.Fatalf("NetworkName produced %q, which the guard would refuse or the runtime would reject", network)
	}
	f := attachRuntime("feint-scw-mine")
	d := newFakeDriver(f)

	if err := d.Attach(context.Background(), "feint-scw-mine", Attachment{Network: network}); err != nil {
		t.Fatalf("Attach on an owned instance and an owned network must succeed, got %v", err)
	}
	if got := f.matching("config device add feint-scw-mine eth"); len(got) != 1 || !strings.Contains(got[0], "network="+network) {
		t.Fatalf("the NIC was not added to the machine the emulator created: %v", f.commands())
	}

	// And a foreign instance beside it is still refused.
	if err := d.Attach(context.Background(), "production-database", Attachment{Network: network}); err == nil {
		t.Fatal("the foreign instance was accepted next to an owned one")
	}
	if got := f.matching("config device add production-database"); len(got) != 0 {
		t.Fatalf("a NIC was added to the foreign instance: %v", got)
	}
}

// TestAttachToAnInstanceThatIsGoneIsReported holds the one place Attach differs
// from Detach. An instance that is not there is an error, not a success: the
// caller is about to publish an address for it. The error must say what it was
// about, and nothing may be sent to the instance after the probe.
func TestAttachToAnInstanceThatIsGoneIsReported(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"config get feint-scw-gone": errors.New("Error: Instance not found"),
	}}
	d := newFakeDriver(f)

	err := d.Attach(context.Background(), "feint-scw-gone", Attachment{Network: "fnt-net"})
	if err == nil {
		t.Fatal("Attach on an instance that does not exist answered success")
	}
	if !isNotFound(err) {
		t.Fatalf("the error lost its meaning, and callers read it with isNotFound: %v", err)
	}
	if len(f.matching("config device")) != 0 {
		t.Fatalf("a device was touched on an instance that is gone: %v", f.commands())
	}
}
