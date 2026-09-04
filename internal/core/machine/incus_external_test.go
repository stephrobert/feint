package machine

import (
	"context"
	"strings"
	"testing"
)

// Every re-plug of an OVN NIC used to cost the guest the public addresses it
// already carried (#675). Measured on 2026-09-04 under `--vm incus-ovn`, on
// the device and in the guest of a server holding 203.0.113.3 on eth1:
//
//	after attaching 203.0.113.5   device external=.3/32,.5/32   guest .5/32 alone
//	after detaching 203.0.113.5   device external=.3/32         guest no public address
//
// The machine answered again only because the next join replayed its
// addresses — and #670's counters recorded that as one repair, which is the
// only reason anybody knew. These hold the driver at the level the runner can
// hold it: the commands it emits, and their order around the re-plug.

// ovnNICWithExternal answers the fake for a machine with one OVN NIC on
// fnt-web, pinned at 10.30.1.10 and routing the given public /32s, and keeps
// the device's ipv4.routes.external in step with the driver's own edits: a
// query after `config device set` reads what the set wrote, the way the
// runtime answers.
func ovnNICWithExternal(f *fakeRuntime, external string) {
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "config device set srv eth1 ipv4.routes.external="):
			external = strings.TrimPrefix(key, "config device set srv eth1 ipv4.routes.external=")
			return nil, nil, true
		case key == "query /1.0/instances/srv":
			doc := `{"devices": {"eth1": {"type": "nic", "network": "fnt-web", "ipv4.address": "10.30.1.10", "ipv4.routes.external": "` + external + `"}},
			 "expanded_devices": {"eth1": {"type": "nic", "network": "fnt-web", "ipv4.address": "10.30.1.10", "ipv4.routes.external": "` + external + `"}}}`
			return []byte(doc), nil, true
		case key == "network get fnt-web ipv4.address":
			return []byte("10.30.1.1/24\n"), nil, true
		case strings.HasPrefix(key, "network get fnt-web user."):
			return []byte("feint\n"), nil, true
		case strings.HasPrefix(key, "network get "+DefaultUplinkName):
			return []byte(""), nil, true
		}
		return nil, nil, false
	}
}

// position answers the index of the first command carrying the substring,
// or -1, so an order can be held rather than a presence.
func position(f *fakeRuntime, substr string) int {
	for i, cmd := range f.commands() {
		if strings.Contains(cmd, substr) {
			return i
		}
	}
	return -1
}

// TestRoutingASecondAddressLeavesTheFirstOnTheGuest: the attach, which is the
// gesture the measurement named first.
func TestRoutingASecondAddressLeavesTheFirstOnTheGuest(t *testing.T) {
	f := &fakeRuntime{}
	ovnNICWithExternal(f, "203.0.113.3/32")
	d := ovnDriver(f)

	if err := d.RouteAddress(context.Background(), AddressSpec{Machine: "srv", Address: "203.0.113.5", Network: "fnt-web"}); err != nil {
		t.Fatalf("route: %v", err)
	}
	replug := position(f, "config device set srv eth1 ipv4.routes.external=203.0.113.3/32,203.0.113.5/32")
	first := position(f, "exec srv -- ip address add 203.0.113.3/32 dev eth1")
	second := position(f, "exec srv -- ip address add 203.0.113.5/32 dev eth1")
	if replug < 0 {
		t.Fatalf("the device was not given the merged list:\n%s", strings.Join(f.commands(), "\n"))
	}
	if first < replug {
		t.Fatalf("the first address was not given back to the guest after the re-plug (at %d, re-plug at %d):\n%s",
			first, replug, strings.Join(f.commands(), "\n"))
	}
	if second < replug {
		t.Fatalf("the second address was not added after the re-plug:\n%s", strings.Join(f.commands(), "\n"))
	}
}

// TestDetachingOneAddressLeavesTheOthersOnTheGuest: the detach, asserted
// before any join could hide it — the join is what hid it for a day.
func TestDetachingOneAddressLeavesTheOthersOnTheGuest(t *testing.T) {
	f := &fakeRuntime{}
	ovnNICWithExternal(f, "203.0.113.3/32,203.0.113.5/32")
	d := ovnDriver(f)

	if err := d.UnrouteAddress(context.Background(), "srv", "203.0.113.5"); err != nil {
		t.Fatalf("unroute: %v", err)
	}
	replug := position(f, "config device set srv eth1 ipv4.routes.external=203.0.113.3/32")
	kept := position(f, "exec srv -- ip address add 203.0.113.3/32 dev eth1")
	if replug < 0 {
		t.Fatalf("the device was not given the kept list:\n%s", strings.Join(f.commands(), "\n"))
	}
	if kept < replug {
		t.Fatalf("the address the device still routes was not given back to the guest after the re-plug (at %d, re-plug at %d):\n%s",
			kept, replug, strings.Join(f.commands(), "\n"))
	}
	if gone := position(f, "exec srv -- ip address add 203.0.113.5/32"); gone >= 0 {
		t.Fatalf("the detached address was given back to the guest:\n%s", strings.Join(f.commands(), "\n"))
	}
}

// TestDetachingFromARoutedNICKeepsTheAddressItStillPins is the other path,
// held so nobody has to reason about it: a routed NIC keeps its extras in
// ipv4.routes in both modes (routeOntoRoutedNIC writes that key), and
// repairRoutedInterface reads it back and re-adds the pinned address. It
// passed before #675 and is the measurement that the defect was not here.
func TestDetachingFromARoutedNICKeepsTheAddressItStillPins(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"query /1.0/instances/srv": `{
			"devices": {"eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.3", "ipv4.routes": "203.0.113.5/32", "ipv4.host_address": "169.254.0.1"}},
			"expanded_devices": {"eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.3", "ipv4.routes": "203.0.113.5/32", "ipv4.host_address": "169.254.0.1"}}
		}`,
		"config device get srv eth0 ipv4.routes": "203.0.113.5/32\n",
		"network get " + DefaultUplinkName:       "",
	}}
	d := ovnDriver(f)
	if err := d.UnrouteAddress(context.Background(), "srv", "203.0.113.5"); err != nil {
		t.Fatalf("unroute: %v", err)
	}
	if position(f, "exec srv -- ip address add 203.0.113.3/32 dev eth0") < 0 {
		t.Fatalf("the routed NIC's pinned address was not restored after its edit:\n%s", strings.Join(f.commands(), "\n"))
	}
	if position(f, "exec srv -- ip route add default via 169.254.0.1 dev eth0") < 0 {
		t.Fatalf("the routed NIC's default route was not restored:\n%s", strings.Join(f.commands(), "\n"))
	}
}
