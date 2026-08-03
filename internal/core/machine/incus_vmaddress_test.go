package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A virtual machine does not see the interface names Incus uses.
//
// Incus knows the device as `eth1`; the guest kernel names a PCI device
// `enp6s0`. The driver passed the Incus name into the guest, so `ip address add
// … dev eth1` matched nothing: an audit measured a VM carrying its bridge
// address and loopback, never the address the API had published, over three
// minutes, while the host-side reservation was correct the whole time.
//
// That is the failure this project exists to avoid — an address the control
// plane publishes and nothing answers on — and it is an argument-level fact, so
// the runner is what holds it.

// vmState is what `incus query /1.0/instances/<n>/state` answers for a VM whose
// second interface carries the MAC of device eth1. The guest names it enp6s0,
// which is the whole point.
const vmState = `{
  "network": {
    "lo":     {"hwaddr": ""},
    "enp5s0": {"hwaddr": "00:16:3e:aa:aa:aa"},
    "enp6s0": {"hwaddr": "00:16:3e:bb:bb:bb"}
  }
}`

// vmDevices is the real shape: a device added without a hwaddr, and the address
// Incus generated for it living in the instance's volatile configuration. The
// first version of this fixture declared it on the device, which is the rare
// case, and hid the path that failed on three consecutive machines.
const vmDevices = `{
  "config": {
    "volatile.eth0.hwaddr": "00:16:3e:aa:aa:aa",
    "volatile.eth1.hwaddr": "00:16:3e:bb:bb:bb"
  },
  "devices": {
    "eth1": {"type": "nic", "network": "fnt-net"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "network": "incusbr0"},
    "eth1": {"type": "nic", "network": "fnt-net"}
  }
}`

func TestAVirtualMachineIsConfiguredOnItsOwnInterfaceName(t *testing.T) {
	f := &fakeRuntime{hook: exactQueries}
	d := newFakeDriver(f)
	d.VM = true

	if err := d.configureGuestAddress(context.Background(), "vm", "eth1", "10.189.0.2/24"); err != nil {
		t.Fatalf("configure the guest: %v", err)
	}

	// The commands that reach the guest must name the interface the guest has.
	for _, cmd := range f.matching("exec vm -- ip") {
		if strings.Contains(cmd, "eth1") {
			t.Errorf("the guest was configured on the host's device name: %s", cmd)
		}
		if !strings.Contains(cmd, "enp6s0") {
			t.Errorf("the guest was not configured on its own interface: %s", cmd)
		}
	}
	if len(f.matching("exec vm -- ip address add 10.189.0.2/24 dev enp6s0")) != 1 {
		t.Errorf("the address was not given to the guest interface: %v", f.commands())
	}
	if len(f.matching("exec vm -- ip link set enp6s0 up")) != 1 {
		t.Errorf("the guest interface was not brought up: %v", f.commands())
	}
}

// A container keeps the name it already had.
//
// The two names match there, and paying a state lookup per attachment for a
// fact that cannot change would slow the mode that exists for being fast.
func TestAContainerIsConfiguredOnTheDeviceName(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f) // VM stays false

	if err := d.configureGuestAddress(context.Background(), "ct", "eth1", "10.189.0.2/24"); err != nil {
		t.Fatalf("configure the guest: %v", err)
	}
	if len(f.matching("query /1.0/instances/ct/state")) != 0 {
		t.Errorf("a container paid for a lookup it does not need: %v", f.commands())
	}
	if len(f.matching("exec ct -- ip address add 10.189.0.2/24 dev eth1")) != 1 {
		t.Errorf("the container was not configured on its device: %v", f.commands())
	}
}

// A virtual machine waits before a device is added to it.
//
// Two measurements on the same code, minutes apart: once the device was added
// and only its address was missing, once the add itself was refused with "PCI:
// slot 0 function 0 not available for virtio-net-pci, in use by
// virtio-balloon-pci". Incus documents NIC hotplug as supported for virtual
// machines, so what differed was when the add happened, not whether it can.
// Waiting for the agent is waiting for a machine that has finished coming up.
//
// The wait therefore belongs before `config device add`, not before the address
// is configured — where the first version of this fix put it, which is why it
// changed nothing about the refusal.
func TestAVirtualMachineWaitsBeforeAddingADevice(t *testing.T) {
	notReady := errors.New(`incus exec: Error: VM agent isn't currently running`)
	f := &fakeRuntime{}
	probes := 0
	// The first two probes fail the way a booting VM does; the third answers.
	f.hook = func(call int, args []string) ([]byte, error, bool) {
		if len(args) >= 4 && args[0] == "exec" && args[3] == "true" {
			probes++
			if probes < 3 {
				return nil, notReady, true
			}
			return nil, nil, true
		}
		return exactQueries(call, args)
	}
	d := newFakeDriver(f)
	d.VM = true
	d.agentPoll = time.Millisecond

	err := d.Attach(context.Background(), "vm", Attachment{
		Network: "fnt-net", Address: "10.189.0.2", PrefixLen: 24,
	})
	if err != nil {
		t.Fatalf("attach after the agent came up: %v", err)
	}
	if probes != 3 {
		t.Errorf("the driver probed the agent %d time(s), want it to wait for the third", probes)
	}
	// The order is the point: nothing may be added to the machine before the
	// probe that succeeded.
	touched, probed := -1, -1
	for i, cmd := range f.commands() {
		if strings.HasPrefix(cmd, "config device ") && touched < 0 {
			touched = i
		}
		if strings.HasPrefix(cmd, "exec vm -- true") {
			probed = i
		}
	}
	if touched < 0 {
		t.Fatalf("the device was never touched: %v", f.commands())
	}
	if probed > touched {
		t.Errorf("the device was configured before the machine answered: %v", f.commands())
	}
}

// An error that is not the agent booting is reported, not waited through.
//
// A machine that is gone will not come back, and waiting ninety seconds for it
// would turn a clear failure into a hang.
func TestAnErrorThatIsNotTheAgentIsReported(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"exec vm -- true": errors.New(`Error: Instance not found`),
	}}
	d := newFakeDriver(f)
	d.VM = true
	d.agentPoll = time.Millisecond

	err := d.Attach(context.Background(), "vm", Attachment{
		Network: "fnt-net", Address: "10.189.0.2", PrefixLen: 24,
	})
	if err == nil {
		t.Fatal("a missing instance was waited through instead of reported")
	}
	if !strings.Contains(err.Error(), "Instance not found") {
		t.Errorf("the error lost what it was about: %v", err)
	}
}

// An interface that carries no matching address is refused, not guessed.
//
// Configuring the wrong interface would put the address on another network, and
// the caller is about to publish it.
func TestAnUnmatchedDeviceIsRefusedRatherThanGuessed(t *testing.T) {
	f := &fakeRuntime{hook: func(call int, args []string) ([]byte, error, bool) {
		if len(args) == 2 && args[0] == "query" && args[1] == "/1.0/instances/vm/state" {
			return []byte(`{"network": {"lo": {"hwaddr": ""}, "enp5s0": {"hwaddr": "00:16:3e:aa:aa:aa"}}}`), nil, true
		}
		return exactQueries(call, args)
	}}
	d := newFakeDriver(f)
	d.VM = true

	err := d.configureGuestAddress(context.Background(), "vm", "eth1", "10.189.0.2/24")
	if err == nil {
		t.Fatal("an address was applied to an interface nothing matched")
	}
	for _, cmd := range f.matching("ip address add") {
		t.Errorf("an address was given to an interface that is not the device's: %s", cmd)
	}
}

// exactQueries answers the two instance queries by their exact path. The shared
// fakeRuntime matches answers by substring, and "/1.0/instances/vm" is a prefix
// of "/1.0/instances/vm/state": with both in the map, Go's map iteration order
// decided which one came back, and this test passed or failed at random.
func exactQueries(_ int, args []string) ([]byte, error, bool) {
	if len(args) == 2 && args[0] == "query" {
		switch args[1] {
		case "/1.0/instances/vm/state":
			return []byte(vmState), nil, true
		case "/1.0/instances/vm":
			return []byte(vmDevices), nil, true
		}
	}
	return nil, nil, false
}
