package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Start owns two facts the conformance suite cannot see until they hurt, both
// argument-level and both measured on Incus 7.2 before being coded (#116):
//
//   - a machine with no attachment must boot on a network the emulator owns,
//     because a route is refused outside them (mustOwn) and covering a profile
//     NIC with a firewall means overriding it, which re-plugs the device after
//     boot and costs the guest its DHCP lease with nothing left to renew it;
//   - a public address must be a device key before the first boot, because
//     editing a route key on a live OVN NIC is the same re-plug.

// startScript answers the calls Start makes on the path that creates a new
// instance: the instance does not exist until "start" ran, and the default
// network does not exist at all.
func startScript(f *fakeRuntime) {
	started := false
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch args[0] {
		case "list":
			if started {
				return []byte(`[{"name":"srv","status":"Running","state":{"network":{}}}]`), nil, true
			}
			return []byte(`[]`), nil, true
		case "query":
			if strings.Contains(args[len(args)-1], "/1.0/networks/") {
				return nil, errors.New("Network not found"), true
			}
		case "start":
			started = true
		}
		return nil, nil, false
	}
}

// A machine carries the addresses its provider's API publishes, and no others
// (#202).
//
// This replaces the test that required a machine with no attachment to boot on
// the emulator's own bridge. That test asserted the behaviour being removed, and
// its own comment named the assumption that made it necessary: "NATed, or the
// guest cannot install its ssh daemon". #203 built images that already carry
// one, so the guest installs nothing and the bridge has no job left.
//
// Measured against real accounts, empty before and after: a Scaleway server has
// one routed public address and `private_ip: none`; an Exoscale instance has one
// address. This emulator gave two or three, and the extra came from here.
func TestAMachineWithNoNetworkCarriesOnlyItsPublicAddress(t *testing.T) {
	f := &fakeRuntime{}
	startScript(f)
	d := newFakeDriver(f)

	_, err := d.Start(context.Background(), Spec{
		Name:            "srv",
		Image:           "alpine:3.21",
		PublicAddresses: []string{"203.0.113.7"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if creates := f.matching("network create " + DefaultMachineNetwork); len(creates) != 0 {
		t.Errorf("a network was created for a machine that publishes no address on one:\n%s",
			strings.Join(creates, "\n"))
	}

	inits := f.matching("init ")
	if len(inits) != 1 {
		t.Fatalf("expected one init, got:\n%s", strings.Join(f.commands(), "\n"))
	}
	if strings.Contains(inits[0], "--network") {
		t.Errorf("the machine joined a network nothing publishes: %s", inits[0])
	}
	// The guest has to be told, or the routed address lands on the device and
	// the interface comes up carrying nothing.
	if !strings.Contains(inits[0], "cloud-init.network-config=") {
		t.Errorf("no network-config was rendered, so the guest configures nothing: %s", inits[0])
	}

	devices := f.matching("config device add srv eth0 nic")
	if len(devices) != 1 {
		t.Fatalf("expected one routed interface, got:\n%s", strings.Join(f.commands(), "\n"))
	}
	for _, want := range []string{"nictype=routed", "ipv4.address=203.0.113.7"} {
		if !strings.Contains(devices[0], want) {
			t.Errorf("the interface is missing %q: %s", want, devices[0])
		}
	}
	// A routed NIC carries the address itself; the route key belongs to a NIC
	// that sits on a network, and setting it here would be a second mechanism
	// for one address.
	if routes := f.matching("ipv4.routes"); len(routes) != 0 {
		t.Errorf("a routed interface was also given route keys:\n%s", strings.Join(routes, "\n"))
	}
}

// And the machine a real cloud gives nothing: no address asked for, no private
// network. It boots, and it carries no interface at all.
//
// The refusing half of the case above. Without it, a driver that always added a
// routed NIC would pass that test and invent an address here, which is the exact
// defect being removed.
func TestAMachineWithNothingPublishedCarriesNoInterface(t *testing.T) {
	f := &fakeRuntime{}
	startScript(f)
	d := newFakeDriver(f)

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "alpine:3.21"}); err != nil {
		t.Fatalf("start: %v", err)
	}

	if creates := f.matching("network create"); len(creates) != 0 {
		t.Errorf("a network was created for a machine with no address:\n%s",
			strings.Join(creates, "\n"))
	}
	if devices := f.matching("config device add srv eth0"); len(devices) != 0 {
		t.Errorf("an interface was added for a machine with no address:\n%s",
			strings.Join(devices, "\n"))
	}
	inits := f.matching("init ")
	if len(inits) != 1 || strings.Contains(inits[0], "--network") {
		t.Errorf("the machine joined a network anyway:\n%s", strings.Join(f.commands(), "\n"))
	}
}

// The route keys must be set while the instance is cold — after init, before
// start. Setting them on a running instance is exactly the live edit whose
// re-plug this order exists to avoid.
func TestPublicAddressesAreRoutedBeforeTheFirstBoot(t *testing.T) {
	modes := []struct {
		name string
		key  string
		ovn  bool
	}{
		{"bridge", "ipv4.routes", false},
		{"ovn", "ipv4.routes.external", true},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			f := &fakeRuntime{}
			startScript(f)
			d := newFakeDriver(f)
			d.OVN = mode.ovn

			_, err := d.Start(context.Background(), Spec{
				Name:            "srv",
				Image:           "alpine:3.21",
				Attachments:     []Attachment{{Network: "fnt-abc", Address: "10.181.0.2"}},
				PublicAddresses: []string{"203.0.113.2", "203.0.113.9"},
			})
			if err != nil {
				t.Fatalf("start: %v", err)
			}

			routeIdx, startIdx, uplinkIdx := -1, -1, -1
			want := "config device set srv eth0 " + mode.key + "=203.0.113.2/32,203.0.113.9/32"
			for i, cmd := range f.commands() {
				switch {
				case cmd == want:
					routeIdx = i
				case cmd == "start srv":
					startIdx = i
				case strings.HasPrefix(cmd, "network set "+DefaultUplinkName+" ipv4.routes=") &&
					strings.Contains(cmd, "203.0.113.2/32") && uplinkIdx == -1:
					uplinkIdx = i
				}
			}
			if routeIdx == -1 {
				t.Fatalf("the public routes never became a device key (%s):\n%s",
					want, strings.Join(f.commands(), "\n"))
			}
			if startIdx == -1 || routeIdx > startIdx {
				t.Fatalf("the routes were set after the boot (route at %d, start at %d): the live edit re-plugs an OVN NIC",
					routeIdx, startIdx)
			}
			if mode.ovn {
				// Incus validates the device key against the uplink's routes
				// and refuses it otherwise — measured: "Uplink network doesn't
				// contain ... in its routes". So the /32 must reach the uplink
				// first, or the whole boot fails.
				if uplinkIdx == -1 || uplinkIdx > routeIdx {
					t.Fatalf("the uplink does not carry the /32 before the device names it (uplink at %d, device at %d):\n%s",
						uplinkIdx, routeIdx, strings.Join(f.commands(), "\n"))
				}
			} else if uplinkIdx != -1 {
				t.Fatalf("bridge mode touched the OVN uplink:\n%s", strings.Join(f.commands(), "\n"))
			}
		})
	}
}

// A mode switch leaves the default network behind as the wrong type, and
// EnsureNetwork rightly refuses to reuse it — so every boot of the new mode
// failed until somebody swept by hand. The replacement is bounded twice over:
// only the emulator's own labelled network goes, and only when it is empty.
func TestTheDefaultNetworkFollowsTheMode(t *testing.T) {
	leftover := func(labelled bool, usedBy string) string {
		label := ""
		if labelled {
			label = `"user.` + LabelKey + `": "feint"`
		}
		return `{"type": "bridge", "config": {` + label + `}, "used_by": [` + usedBy + `]}`
	}

	cases := []struct {
		name     string
		existing string
		replaced bool
	}{
		{"an empty labelled bridge is replaced in ovn mode", leftover(true, ""), true},
		{"a network with a machine on it stays", leftover(true, `"/1.0/instances/x"`), false},
		{"an unlabelled network is never touched", leftover(false, ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRuntime{answers: map[string]string{
				"query /1.0/networks/" + DefaultMachineNetwork: tc.existing,
			}}
			d := newFakeDriver(f)
			d.OVN = true

			// The error path is not under test: EnsureNetwork will refuse the
			// wrong-typed leftover in the cases where it stays, and that
			// refusal is its own test's business.
			_ = d.ensureDefaultNetwork(context.Background())

			deleted := len(f.matching("network delete "+DefaultMachineNetwork)) > 0
			if deleted != tc.replaced {
				t.Errorf("deleted=%v, want %v:\n%s", deleted, tc.replaced,
					strings.Join(f.commands(), "\n"))
			}
		})
	}
}

// A Start that fails after init must not leave the instance behind: the next
// poweron would find it, take the already-exists path, and boot a machine
// missing the very keys the failed call was setting — measured in OVN mode,
// where the half-made machine came up with no route key, no DHCP lease and no
// ssh daemon, while the API said running.
func TestAFailedStartLeavesNoHalfMadeInstance(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"ipv4.address=10.181.0.2": errors.New("Device validation failed"),
	}}
	startScript(f)
	d := newFakeDriver(f)

	_, err := d.Start(context.Background(), Spec{
		Name:        "srv",
		Image:       "alpine:3.21",
		Attachments: []Attachment{{Network: "fnt-abc", Address: "10.181.0.2"}},
	})
	if err == nil {
		t.Fatal("a refused device key did not fail the start")
	}
	if len(f.matching("delete --force srv")) != 1 {
		t.Fatalf("the half-made instance was left behind:\n%s", strings.Join(f.commands(), "\n"))
	}
}
