package machine

import (
	"context"
	"strings"
	"testing"
)

// The fallback interface is a loan, not a fixture. A machine born unattached
// boots on DefaultMachineNetwork so cloud-init has a way out; the #201
// investigation measured that interface surviving every later attachment, on
// all three packs, carrying an address no provider API publishes. The rule is
// the author's: same count of addresses on every provider for the same
// request. These tests hold the transition that enforces it.

// fallbackInstance is a machine born on the fallback, as Start creates it:
// one NIC, the instance's own, on DefaultMachineNetwork.
const fallbackInstance = `{
  "devices": {
    "eth0": {"type": "nic", "network": "` + DefaultMachineNetwork + `"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "network": "` + DefaultMachineNetwork + `"}
  }
}`

func TestAttachRetiresTheFallbackInterface(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"query /1.0/instances/srv": fallbackInstance,
	}}
	d := newFakeDriver(f)

	err := d.Attach(context.Background(), "srv", Attachment{
		Network: "fnt-pn", Address: "10.181.0.2", PrefixLen: 24,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if got := f.matching("config device remove srv eth0"); len(got) == 0 {
		t.Errorf("the fallback interface survived the attachment; calls: %v", f.commands())
	}
}

// Re-attaching to the fallback network itself — which no pack does, but the
// guard must hold — removes nothing: a machine's only interface never
// retires.
func TestAttachToTheFallbackRemovesNothing(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"query /1.0/instances/srv": fallbackInstance,
	}}
	d := newFakeDriver(f)

	err := d.Attach(context.Background(), "srv", Attachment{Network: DefaultMachineNetwork})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if got := f.matching("config device remove"); len(got) != 0 {
		t.Errorf("attaching to the fallback network removed a device: %v", got)
	}
}

// A public /32 routed while the fallback was primary must move to the
// interface that replaces it, and the guest's default route with it: a
// machine that holds a public address keeps its way out, through the
// interface that now carries the address.
func TestAttachMovesThePublicAddressOffTheFallback(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		// Both interfaces exist by the time the transition runs: the private
		// NIC was just attached, the fallback still carries the /32.
		"query /1.0/instances/srv": `{
		  "devices": {
		    "eth0": {"type": "nic", "network": "` + DefaultMachineNetwork + `",
		             "ipv4.routes.external": "203.0.113.9/32"},
		    "eth1": {"type": "nic", "network": "fnt-pn", "ipv4.address": "10.181.0.2"}
		  },
		  "expanded_devices": {
		    "eth0": {"type": "nic", "network": "` + DefaultMachineNetwork + `",
		             "ipv4.routes.external": "203.0.113.9/32"},
		    "eth1": {"type": "nic", "network": "fnt-pn", "ipv4.address": "10.181.0.2"}
		  }
		}`,
		"network get fnt-pn user." + LabelKey:               "feint",
		"network get fnt-pn ipv4.address":                   "10.181.0.1/24",
		"network get " + DefaultUplinkName + " ipv4.routes": "10.181.0.0/24",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	err := d.Attach(context.Background(), "srv", Attachment{
		Network: "fnt-pn", Address: "10.181.0.2", PrefixLen: 24,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if got := f.matching("config device remove srv eth0"); len(got) == 0 {
		t.Fatalf("the fallback interface survived the attachment; calls: %v", f.commands())
	}
	moved := false
	for _, cmd := range f.matching("config device set srv eth1") {
		if strings.Contains(cmd, "ipv4.routes.external=") && strings.Contains(cmd, "203.0.113.9/32") {
			moved = true
		}
	}
	if !moved {
		t.Errorf("the public /32 did not move to the new primary; calls: %v", f.commands())
	}
	if got := f.matching("exec srv -- ip route add default via 10.181.0.1"); len(got) == 0 {
		t.Errorf("the default route did not follow the public address; calls: %v", f.commands())
	}
}
