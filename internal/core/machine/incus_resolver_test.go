package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A device set re-plugs the NIC, and the guest is told to read its
// configuration again afterwards (#684).
//
// The order is the property, not the presence: the reload has to come after the
// set, because it is the set that takes the interface away, and after the link
// is back, because a reload fired into the gap reloads a configuration for an
// interface that does not exist yet.
func TestADeviceSetGivesTheGuestBackItsInterface(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"ip -o link show dev": "2: eth1: <BROADCAST,MULTICAST,UP>\n",
	}}
	d := newFakeDriver(f)

	if _, err := d.setDevice(context.Background(), "srv", "eth1", "ipv4.address=10.189.0.2"); err != nil {
		t.Fatalf("set the device: %v", err)
	}

	set := indexOfCall(f, "config device set srv eth1")
	reload := indexOfCall(f, "networkctl reload")
	link := indexOfCall(f, "link show dev")
	if set < 0 {
		t.Fatalf("the device was never set:\n%s", strings.Join(f.commands(), "\n"))
	}
	if reload < 0 {
		t.Fatalf("the guest was never told to read its configuration after the set:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if reload < set {
		t.Fatalf("the guest was told at %d, before the set at %d that takes the interface away:\n%s",
			reload, set, strings.Join(f.commands(), "\n"))
	}
	if link < 0 || link > reload {
		t.Fatalf("the guest was told at %d without waiting for the link (probe at %d):\n%s",
			reload, link, strings.Join(f.commands(), "\n"))
	}
}

// A set that failed changed nothing, so there is nothing to give back — and
// telling the guest anyway would hide the refusal behind a command that
// succeeds.
func TestASetThatFailedTellsNobody(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"config device set": errors.New("Error: Failed to update device"),
	}}
	d := newFakeDriver(f)

	if _, err := d.setDevice(context.Background(), "srv", "eth1", "ipv4.address=10.189.0.2"); err == nil {
		t.Fatal("a refused device set was reported as success")
	}
	if i := indexOfCall(f, "networkctl reload"); i >= 0 {
		t.Fatalf("the guest was told after a set that changed nothing:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// The wait is bounded and it is what makes the reload land on an interface that
// exists.
//
// `incus config device set` returns before the guest's kernel publishes the new
// link. Measured 2026-09-04 under `--vm incus-ovn`, from the guest's own
// journal: eth1 was renamed at 16:40:29 and configured, the address migration
// set both devices, a new veth became eth1 at 16:40:43, and nothing configured
// it after that.
func TestADeviceSetWaitsForTheLinkBeforeTellingTheGuest(t *testing.T) {
	const appears = 3 // the link is not back for the first two probes
	probes := 0
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		if len(args) >= 6 && args[0] == "exec" && args[3] == "ip" && args[5] == "link" {
			probes++
			if probes < appears {
				return nil, errors.New(`Error: Device "eth1" does not exist`), true
			}
			return []byte("2: eth1: <BROADCAST,MULTICAST,UP>\n"), nil, true
		}
		return nil, nil, false
	}
	d := newFakeDriver(f)
	d.routePoll = time.Millisecond

	if _, err := d.setDevice(context.Background(), "srv", "eth1", "ipv4.address=10.189.0.2"); err != nil {
		t.Fatalf("set the device: %v", err)
	}
	if probes < appears {
		t.Fatalf("the set gave up after %d probes, before the link came back at %d", probes, appears)
	}
	reload := indexOfCall(f, "networkctl reload")
	if reload < 0 {
		t.Fatalf("no reload was emitted:\n%s", strings.Join(f.commands(), "\n"))
	}
	last := -1
	for i, call := range f.commands() {
		if strings.Contains(call, "link show dev") {
			last = i
		}
	}
	if last > reload {
		t.Fatalf("the guest was told at %d, before the link was last probed at %d:\n%s",
			reload, last, strings.Join(f.commands(), "\n"))
	}
}

// An image without systemd-networkd still boots, and the emulator serves one:
// Alpine has no networkctl at all. A machine whose guest cannot be told is a
// machine that still carries its addresses and still answers, so the refusal is
// swallowed rather than failing the operation that caused it.
func TestAGuestWithoutNetworkctlIsNotARefusal(t *testing.T) {
	for _, spelling := range []string{
		`Error: Command not found`,
		`sh: networkctl: not found`,
		`Failed to connect to bus: No such file or directory`,
		`Unit systemd-networkd.service could not be found.`,
	} {
		f := &fakeRuntime{fail: map[string]error{
			"networkctl reload": errors.New(spelling),
		}}
		d := newFakeDriver(f)
		if err := d.reloadGuestNetwork(context.Background(), "srv"); err != nil {
			t.Fatalf("a guest answering %q was treated as a failure: %v", spelling, err)
		}
	}
}

// And the accepting half, without which a guard that swallowed everything would
// pass every test above: a guest that answers something else is a real refusal
// and travels back to the caller, who logs it.
func TestAGuestThatRefusesTheReloadIsReported(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"networkctl reload": errors.New("Error: Failed to reload network settings: Permission denied"),
	}}
	d := newFakeDriver(f)
	if err := d.reloadGuestNetwork(context.Background(), "srv"); err == nil {
		t.Fatal("a guest that refused the reload was reported as success")
	}
}

// indexOfCall answers where a command appears in the recorded argv, -1 when it
// never does.
func indexOfCall(f *fakeRuntime, substr string) int {
	for i, call := range f.commands() {
		if strings.Contains(call, substr) {
			return i
		}
	}
	return -1
}

// The second caller: a NIC plugged into a running machine.
//
// The add is not a set — nothing is re-plugged — but the guest's stack
// enumerated the link before any configuration matched it. Measured 2026-09-04:
// with only the set covered, the machine holding a public address resolved and
// the machine without one did not.
func TestAHotAttachGivesTheGuestItsNewInterface(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":  `{"expanded_devices":{},"devices":{}}`,
		"ip -o link show dev": "2: eth0: <BROADCAST,MULTICAST,UP>\n",
	}}
	d := newFakeDriver(f)

	if err := d.Attach(context.Background(), "srv", Attachment{Network: "fnt-net"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	add := indexOfCall(f, "config device add srv")
	reload := indexOfCall(f, "networkctl reload")
	if add < 0 {
		t.Fatalf("no device was added:\n%s", strings.Join(f.commands(), "\n"))
	}
	if reload < 0 {
		t.Fatalf("the guest was never told about its new interface:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if reload < add {
		t.Fatalf("the guest was told at %d, before the add at %d that gives it the interface:\n%s",
			reload, add, strings.Join(f.commands(), "\n"))
	}
}

// A machine whose NIC was attached before it was powered on gets its resolver
// at the boot, since its attach ran against a stopped guest and did nothing.
//
// Measured 2026-09-04 under `--vm incus-ovn`: that shape came up holding the
// uplink's own address as its only name server — Incus's default, announced
// because this emulator announces none — which is #660's dead on-link route in
// another spelling.
func TestAFirstBootGivesTheGuestItsResolver(t *testing.T) {
	const ownedNIC = `{
	  "expanded_devices": {"eth1": {"type": "nic", "network": "fnt-net", "ipv4.address": "10.189.0.2"}},
	  "devices": {"eth1": {"type": "nic", "network": "fnt-net", "ipv4.address": "10.189.0.2"}}
	}`
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":               ownedNIC,
		"network get fnt-net ipv4.address": "10.189.0.1/24\n",
		"ip -4 -o addr show dev eth1":      "2: eth1    inet 10.189.0.2/24 scope global eth1\n",
		"ip -o link show dev":              "2: eth1: <BROADCAST,MULTICAST,UP>\n",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.settleFirstBoot(context.Background(), "srv"); err != nil {
		t.Fatalf("settle the first boot: %v", err)
	}
	if i := indexOfCall(f, "resolvectl dns eth1 "+DefaultResolver); i < 0 {
		t.Fatalf("the boot left the interface without a resolver:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}
