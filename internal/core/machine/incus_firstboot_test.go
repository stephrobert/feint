package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The first boot owes the guest the same thing a restart does (#587).
//
// WHAT WAS MEASURED, and why an argument-level test is the right level for it.
// On the maintainer's station (2026-08-29, `--vm incus-ovn`, Incus 7.2, OVN
// 24.03.6), three machines were created on the same Subnet, twice each, and
// asked how long after `CreateVms` answered they carried the address the API
// had published:
//
//	ubuntu:24.04  (ami-…0001)   27 ms, 32 ms
//	debian:12     (ami-…0002)   46 ms, 35 ms
//	alpine:3.21   (ami-…0003)   never — 45 s, 90 s and 180 s ceilings, six times
//
// The alpine image ships dhcpcd 10.1.0, which ARP-probes the offered address
// before accepting it; the probe reaches the uplink, the OVN gateway answers it
// because fronting that block is its job, and the guest declines its own lease
// for ever ("DAD detected 10.182.9.4", once a second). It is not slowness: at
// the end of a 90 s wait the guest's client was gone and a `udhcpc` exchange in
// that same guest was answered in 97–143 ms.
//
// So the driver must not depend on which DHCP client an image happens to ship,
// and it already does not anywhere else: Attach configures the guest,
// restoreGuestNetwork puts it back after a reboot. The first boot was the last
// door still trusting the lease. That the runtime accepts these commands is
// tools/conformance/outscale/network.sh's half; this is the half that holds
// they are emitted at all, and that they are emitted for nobody else's NIC.

// firstBootScript answers the calls a first Start makes: no instance before
// `start`, one after, the devices the launch created, and the network's mask.
func firstBootScript(f *fakeRuntime, devices string) {
	started := false
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch args[0] {
		case "list":
			if started {
				return []byte(`[{"name":"srv","status":"Running","state":{"network":{}}}]`), nil, true
			}
			return []byte(`[]`), nil, true
		case "query":
			last := args[len(args)-1]
			if strings.HasSuffix(last, "/1.0/instances/srv") {
				return []byte(devices), nil, true
			}
			if strings.Contains(last, "/1.0/networks/") {
				return nil, errors.New("Network not found"), true
			}
		case "network":
			if len(args) > 3 && args[1] == "get" && args[3] == "ipv4.address" {
				return []byte("10.181.0.1/24\n"), nil, true
			}
		case "start":
			started = true
		}
		return nil, nil, false
	}
}

const firstBootPinned = `{
  "devices": {
    "eth0": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.181.0.2"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.181.0.2"}
  }
}`

// The test #587 names. Without settleFirstBoot on the launch branch of Start,
// not one of these commands is emitted, and whether the machine ends up
// carrying its address is decided by the image's DHCP client.
func TestAFirstBootGivesTheGuestTheAddressItReserved(t *testing.T) {
	f := &fakeRuntime{}
	firstBootScript(f, firstBootPinned)
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{
		Name:        "srv",
		Image:       "alpine:3.21",
		Attachments: []Attachment{{Network: "fnt-368798629f8", Address: "10.181.0.2"}},
	}); err != nil {
		t.Fatalf("start a new machine: %v", err)
	}

	// The mask is the network's, read off the runtime: an address configured as
	// a /32 inside the guest has no connected route to its own subnet.
	if len(f.matching("exec srv -- ip address add 10.181.0.2/24 dev eth0")) == 0 {
		t.Errorf("a machine's first boot left its address to the guest's DHCP client:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	// And the routes that make the peered subnets reachable, which a lease that
	// never arrives never lays either.
	for _, block := range privateAggregates {
		want := "exec srv -- ip route add " + block + " via 10.181.0.1 dev eth0"
		if len(f.matching(want)) == 0 {
			t.Errorf("a machine's first boot got no route to %s:\n%s",
				block, strings.Join(f.commands(), "\n"))
		}
	}
}

// The refusing half, and it is the one that keeps the fix honest: a device that
// reserves nothing is DHCP's, and inventing an address for it would put a
// machine on an address no API published — the defect #202 removed.
func TestAFirstBootDoesNotInventAnAddressForANICThatPinsNone(t *testing.T) {
	const leased = `{
	  "devices": {
	    "eth0": {"type": "nic", "network": "fnt-368798629f8"}
	  },
	  "expanded_devices": {
	    "eth0": {"type": "nic", "network": "fnt-368798629f8"}
	  }
	}`
	f := &fakeRuntime{}
	firstBootScript(f, leased)
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{
		Name:        "srv",
		Image:       "alpine:3.21",
		Attachments: []Attachment{{Network: "fnt-368798629f8"}},
	}); err != nil {
		t.Fatalf("start a new machine: %v", err)
	}
	if got := f.matching("ip address add"); len(got) != 0 {
		t.Errorf("the first boot invented an address for a NIC that pins none:\n%s",
			strings.Join(got, "\n"))
	}
}

// Ownership, on the second door. `ownedManagedNICs` is shared with the restart
// path precisely so this cannot be true of one door and false of the other: a
// NIC an operator added by hand to one of our machines is theirs, and no
// command this path emits may name it.
func TestAFirstBootLeavesAForeignNICAlone(t *testing.T) {
	const foreign = `{
	  "devices": {
	    "eth0": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.181.0.2"},
	    "eth9": {"type": "nic", "network": "incusbr0", "ipv4.address": "10.76.154.9"}
	  },
	  "expanded_devices": {
	    "eth0": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.181.0.2"},
	    "eth9": {"type": "nic", "network": "incusbr0", "ipv4.address": "10.76.154.9"}
	  }
	}`
	f := &fakeRuntime{}
	firstBootScript(f, foreign)
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{
		Name:        "srv",
		Image:       "alpine:3.21",
		Attachments: []Attachment{{Network: "fnt-368798629f8", Address: "10.181.0.2"}},
	}); err != nil {
		t.Fatalf("start a new machine: %v", err)
	}
	if got := f.matching("eth9"); len(got) != 0 {
		t.Errorf("the first boot configured a NIC on a network the emulator did not create:\n%s",
			strings.Join(got, "\n"))
	}
	if got := f.matching("10.76.154.9"); len(got) != 0 {
		t.Errorf("the first boot named an address of somebody else's network:\n%s",
			strings.Join(got, "\n"))
	}
	// The accepting half in the same fixture: ours is still configured. A guard
	// that refused everything would pass the two assertions above and break the
	// product.
	if len(f.matching("exec srv -- ip address add 10.181.0.2/24 dev eth0")) == 0 {
		t.Errorf("the ownership check swallowed the emulator's own NIC:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}
