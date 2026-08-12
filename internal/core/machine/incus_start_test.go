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

func TestAMachineWithNoAttachmentBootsOnTheEmulatorsOwnNetwork(t *testing.T) {
	f := &fakeRuntime{}
	startScript(f)
	d := newFakeDriver(f)

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "alpine:3.21"}); err != nil {
		t.Fatalf("start: %v", err)
	}

	creates := f.matching("network create " + DefaultMachineNetwork)
	if len(creates) != 1 {
		t.Fatalf("expected the default machine network to be created once, got:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	// Labelled, or mustOwn refuses to route public addresses through it and the
	// sweep cannot remove it; NATed, or the guest cannot install its ssh daemon.
	for _, want := range []string{"user." + LabelKey + "=", "ipv4.nat=true"} {
		if !strings.Contains(creates[0], want) {
			t.Errorf("the default network is missing %q: %s", want, creates[0])
		}
	}

	inits := f.matching("init ")
	if len(inits) != 1 || !strings.Contains(inits[0], "--network "+DefaultMachineNetwork) {
		t.Fatalf("the machine did not boot on %s:\n%s",
			DefaultMachineNetwork, strings.Join(f.commands(), "\n"))
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

			routeIdx, startIdx := -1, -1
			want := "config device set srv eth0 " + mode.key + "=203.0.113.2/32,203.0.113.9/32"
			for i, cmd := range f.commands() {
				switch cmd {
				case want:
					routeIdx = i
				case "start srv":
					startIdx = i
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
		})
	}
}
