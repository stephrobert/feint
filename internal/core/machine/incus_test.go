package machine

import (
	"context"
	"strings"
	"testing"
)

// gatewayAddress is where a wrong assumption becomes a network nobody can reach:
// Incus wants the gateway carrying the block's mask, not the block itself.
func TestGatewayAddress(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		gateway string
		want    string
		wantErr bool
	}{
		{
			name:    "explicit gateway keeps the block mask",
			cidr:    "10.0.0.0/24",
			gateway: "10.0.0.1",
			want:    "10.0.0.1/24",
		},
		{
			name: "empty gateway defaults to the first address",
			cidr: "192.168.42.0/24",
			want: "192.168.42.1/24",
		},
		{
			name: "the mask is the block mask, not a guess",
			cidr: "172.31.0.0/20",
			want: "172.31.0.1/20",
		},
		{
			name:    "a gateway outside the block is refused",
			cidr:    "10.0.0.0/24",
			gateway: "10.0.1.1",
			wantErr: true,
		},
		{
			name:    "an unparseable block is refused",
			cidr:    "10.0.0.0",
			wantErr: true,
		},
		{
			name:    "an unparseable gateway is refused",
			cidr:    "10.0.0.0/24",
			gateway: "not-an-address",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gatewayAddress(tt.cidr, tt.gateway)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("gatewayAddress(%q, %q) = %q, want an error", tt.cidr, tt.gateway, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("gatewayAddress(%q, %q) = %v", tt.cidr, tt.gateway, err)
			}
			if got != tt.want {
				t.Errorf("gatewayAddress(%q, %q) = %q, want %q", tt.cidr, tt.gateway, got, tt.want)
			}
		})
	}
}

// The pick is by exact device name: a substring check let an existing eth10
// shadow eth1, and a machine with a profile eth0 must not receive a second one.
func TestFreeInterfacePicksTheFirstUnusedName(t *testing.T) {
	nic := func(names ...string) map[string]map[string]string {
		devices := make(map[string]map[string]string, len(names))
		for _, name := range names {
			devices[name] = map[string]string{"type": "nic"}
		}
		return devices
	}

	tests := []struct {
		name    string
		devices map[string]map[string]string
		want    string
	}{
		// A machine with no device at all takes eth0, and this reversed with
		// #202. The case used to want eth1, defensively: every machine had a
		// profile eth0, so an empty map meant the devices could not be read and
		// assuming eth0 was taken was the safe guess. A machine that publishes
		// nothing now boots with --no-profiles and genuinely has none, so the
		// empty map is the truth rather than a blind spot — and naming its first
		// NIC eth1 gave a guest that had no such device, which failed with
		// `Cannot find device "eth1"`.
		{name: "a machine with no device at all takes eth0", devices: nic(), want: "eth0"},
		{name: "profile eth0 does not block eth1", devices: nic("eth0"), want: "eth1"},
		{name: "next free after a gap", devices: nic("eth0", "eth1", "eth3"), want: "eth2"},
		{name: "eth10 does not shadow eth1", devices: nic("eth0", "eth10"), want: "eth1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := freeInterface(tt.devices); got != tt.want {
				t.Errorf("freeInterface(%v) = %q, want %q", tt.devices, got, tt.want)
			}
		})
	}
}

// The interface ends up carrying exactly the secondary addresses the pack asked
// for: the ones that arrived are added, the ones that left are removed (#172).
//
// Reconciled rather than appended, and both halves matter. A driver that only
// added would leave an address on the machine after its API said it was
// unlinked — an address nothing publishes, which is the defect #202 exists to
// prevent, coming back through a different door.
func TestSecondaryAddressesAreReconciledNotAppended(t *testing.T) {
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "-4 -o addr show dev"):
			// The guest already carries the primary and one address the pack
			// has since unlinked.
			return []byte("2: eth1    inet 10.0.0.4/24 scope global eth1\n" +
				"2: eth1    inet 10.0.0.40/24 scope global eth1\n"), nil, true
		case strings.HasPrefix(joined, "query /1.0/instances/srv"):
			return []byte(`{"devices":{"eth1":{"type":"nic","network":"fnt-x"}},` +
				`"expanded_devices":{"eth1":{"type":"nic","network":"fnt-x"}},` +
				`"state":{"network":{"eth1":{"host_name":"veth0"}}}}`), nil, true
		}
		return nil, nil, false
	}
	d := newFakeDriver(f)

	err := d.Attach(context.Background(), "srv", Attachment{
		Network:   "fnt-x",
		Address:   "10.0.0.4",
		PrefixLen: 24,
		Secondary: []string{"10.0.0.50"},
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	commands := strings.Join(f.commands(), "\n")
	if !strings.Contains(commands, "address add 10.0.0.50/24") {
		t.Errorf("the new secondary address was not given to the guest:\n%s", commands)
	}
	if !strings.Contains(commands, "address del 10.0.0.40/24") {
		t.Errorf("the unlinked address was left on the guest:\n%s", commands)
	}
	// And never the primary, which belongs to the interface.
	if strings.Contains(commands, "address del 10.0.0.4/24") {
		t.Errorf("the primary address was removed:\n%s", commands)
	}
}
