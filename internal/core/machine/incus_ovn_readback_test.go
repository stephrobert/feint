package machine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// What the OVN repair reads back off a device after a re-plug is state the
// host holds, and both halves of it reach the guest's `ip address add` as bare
// argv elements: the pinned address as `<it>/<len>`, and every entry of
// ipv4.routes.external as it stands. These tests call the two functions
// directly, the way #945 measured attachExtra's own check, with a device view
// built by a marshaller so a crafted value cannot break the fixture's JSON.

// readbackNIC answers the fake for one OVN NIC on fnt-web whose device config
// is cfg, and the network's block.
func readbackNIC(t *testing.T, f *fakeRuntime, cfg map[string]string) {
	t.Helper()
	nic := map[string]string{"type": "nic", "network": "fnt-web"}
	for k, v := range cfg {
		nic[k] = v
	}
	doc, err := json.Marshal(map[string]map[string]map[string]string{
		"devices": {"eth1": nic}, "expanded_devices": {"eth1": nic},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch strings.Join(args, " ") {
		case "query /1.0/instances/srv":
			return doc, nil, true
		case "network get fnt-web ipv4.address":
			return []byte("10.30.1.1/24\n"), nil, true
		}
		return nil, nil, false
	}
}

// TestARepairAsksWhatItReadsOffTheDevice: the pinned address a re-plug repair
// puts back. Each crafted pin must reach no command; a legitimate one still
// reaches the guest.
func TestARepairAsksWhatItReadsOffTheDevice(t *testing.T) {
	for _, pin := range pinTaints {
		t.Run(pin, func(t *testing.T) {
			f := &fakeRuntime{}
			readbackNIC(t, f, map[string]string{"ipv4.address": pin})
			d := ovnDriver(f)
			if err := d.repairGuestInterface(context.Background(), "srv", "fnt-web", "eth1"); err == nil {
				t.Fatalf("the repair accepted the pin %q read off the device", pin)
			}
			if carriesPin(f, pin) {
				t.Fatalf("the pin %q read off the device reached a command: %v", pin, f.commands())
			}
		})
	}
	t.Run("a legitimate pin is still put back", func(t *testing.T) {
		f := &fakeRuntime{}
		readbackNIC(t, f, map[string]string{"ipv4.address": "10.30.1.10"})
		d := ovnDriver(f)
		if err := d.repairGuestInterface(context.Background(), "srv", "fnt-web", "eth1"); err != nil {
			t.Fatalf("the repair refused a legitimate pin: %v", err)
		}
		if len(f.matching("exec srv -- ip address add 10.30.1.10/24 dev eth1")) != 1 {
			t.Fatalf("the pin was not put back in the guest: %v", f.commands())
		}
	})
}

// TestARestoreOfExternalAddressesParsesEveryEntry: the external routes a
// repair gives back. One row per way an entry fails — not a prefix at all, a
// prefix followed by a flag, and an IPv6 prefix, which parses and is not IPv4
// — each placed after a legitimate entry, so a check that only looked at the
// first entry, or that applied the good ones before refusing, would show.
func TestARestoreOfExternalAddressesParsesEveryEntry(t *testing.T) {
	for _, bad := range []string{"-foo", "203.0.113.9/32 --force", "fe80::1/128"} {
		t.Run(bad, func(t *testing.T) {
			f := &fakeRuntime{}
			d := ovnDriver(f)
			cfg := map[string]string{"ipv4.routes.external": "203.0.113.3/32," + bad}
			if err := d.restoreExternalAddresses(context.Background(), "srv", "eth1", cfg); err == nil {
				t.Fatalf("the restore accepted the external route %q", bad)
			}
			if len(f.matching("ip address add")) != 0 {
				t.Fatalf("the restore sent an address before refusing the list: %v", f.commands())
			}
		})
	}
	t.Run("legitimate entries are all given back", func(t *testing.T) {
		f := &fakeRuntime{}
		d := ovnDriver(f)
		cfg := map[string]string{"ipv4.routes.external": "203.0.113.3/32,203.0.113.5/32"}
		if err := d.restoreExternalAddresses(context.Background(), "srv", "eth1", cfg); err != nil {
			t.Fatalf("the restore refused legitimate entries: %v", err)
		}
		for _, route := range []string{"203.0.113.3/32", "203.0.113.5/32"} {
			if len(f.matching("exec srv -- ip address add "+route+" dev eth1")) != 1 {
				t.Fatalf("%s was not given back: %v", route, f.commands())
			}
		}
	})
}

// routedNIC answers the fake for one routed NIC, eth0, whose device config is
// cfg, built by a marshaller.
func routedNIC(t *testing.T, f *fakeRuntime, cfg map[string]string) {
	t.Helper()
	nic := map[string]string{"type": "nic", "nictype": "routed"}
	for k, v := range cfg {
		nic[k] = v
	}
	doc, err := json.Marshal(map[string]map[string]map[string]string{
		"devices": {"eth0": nic}, "expanded_devices": {"eth0": nic},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		if strings.Join(args, " ") == "query /1.0/instances/srv" {
			return doc, nil, true
		}
		return nil, nil, false
	}
}

// TestARoutedRepairParsesEveryKeyItReadsBack: repairRoutedInterface reads
// three keys off a routed NIC — the launch addresses, the extra routes, the
// next hop — and turns each into an argument of the guest's `ip`. One row per
// key, the crafted value in that key and the other two legitimate, so each
// key's check is the only one any row puts in question. Two crafted values
// per key: one that does not parse, and one that parses and is not IPv4.
// Nothing at all may be sent to the guest when a key is refused: the parse
// happens before the first command, the link bring-up included.
func TestARoutedRepairParsesEveryKeyItReadsBack(t *testing.T) {
	legit := map[string]string{
		"ipv4.address":      "203.0.113.3",
		"ipv4.routes":       "203.0.113.5/32",
		"ipv4.host_address": "169.254.0.1",
	}
	crafted := map[string][]string{
		"ipv4.address":      {"-foo", "fe80::1"},
		"ipv4.routes":       {"-foo", "fe80::/64"},
		"ipv4.host_address": {"-foo", "fe80::1"},
	}
	for _, key := range []string{"ipv4.address", "ipv4.routes", "ipv4.host_address"} {
		for _, bad := range crafted[key] {
			t.Run(key+" "+bad, func(t *testing.T) {
				cfg := map[string]string{}
				for k, v := range legit {
					cfg[k] = v
				}
				// After a legitimate entry, so a check that read only the
				// first one would show.
				cfg[key] = legit[key] + "," + bad
				if key == "ipv4.host_address" {
					cfg[key] = bad
				}
				f := &fakeRuntime{}
				routedNIC(t, f, cfg)
				d := newFakeDriver(f)
				err := d.repairRoutedInterface(context.Background(), "srv", "eth0")
				if err == nil {
					t.Fatalf("the repair accepted %s=%q", key, bad)
				}
				if !strings.Contains(err.Error(), bad) {
					t.Errorf("the refusal does not name the value: %v", err)
				}
				if got := f.matching("exec "); len(got) != 0 {
					t.Fatalf("%s=%q was refused, so nothing should reach the guest: %v", key, bad, got)
				}
			})
		}
	}

	t.Run("legitimate keys are all restored", func(t *testing.T) {
		f := &fakeRuntime{}
		routedNIC(t, f, legit)
		d := newFakeDriver(f)
		if err := d.repairRoutedInterface(context.Background(), "srv", "eth0"); err != nil {
			t.Fatalf("the repair refused a legitimate routed NIC: %v", err)
		}
		for _, want := range []string{
			"exec srv -- ip address add 203.0.113.3/32 dev eth0",
			"exec srv -- ip address add 203.0.113.5/32 dev eth0",
			"exec srv -- ip route add 169.254.0.1 dev eth0",
			"exec srv -- ip route add default via 169.254.0.1 dev eth0",
		} {
			if len(f.matching(want)) != 1 {
				t.Fatalf("missing %q: %v", want, f.commands())
			}
		}
	})
}
