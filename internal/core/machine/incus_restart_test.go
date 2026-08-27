package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// What a machine loses when it is stopped and started again, and what this
// driver owes it back (#549).
//
// The measurement, on the Scaleway example stack under --vm incus-ovn on
// 2026-08-27: platform-web-0 was stopped and started through the API, came back
// running, on its address, reaching its own subnet — and no longer reaching
// platform-app-worker-a one subnet away, while platform-web-1, its identical
// neighbour and never restarted, kept reaching it in the same pass. Inside the
// restarted guest the three RFC 1918 aggregates were gone.
//
// These are argument-level assertions, which is the only level at which "the
// driver put the routes back" can be held without a runtime. That the runtime
// accepts them is tools/conformance/functional.sh's half.

// restartedInstance answers the calls Start makes on the path where the
// instance already exists, for a machine shaped like every Scaleway server the
// stack builds: a routed NIC carrying the public address, and one OVN NIC on a
// network the emulator owns.
func restartedInstance(f *fakeRuntime, devices string) {
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch args[0] {
		case "list":
			return []byte(`[{"name":"srv","status":"Running","state":{"network":{}}}]`), nil, true
		case "query":
			if strings.HasSuffix(args[len(args)-1], "/1.0/instances/srv") {
				return []byte(devices), nil, true
			}
		case "network":
			if len(args) > 3 && args[1] == "get" && args[3] == "ipv4.address" {
				return []byte("10.30.1.1/24\n"), nil, true
			}
		case "exec":
			// The guest answers that its interface is configured, which is what
			// the wait is looking for.
			if strings.Contains(strings.Join(args, " "), "-o addr show dev") {
				return []byte("2: eth1    inet 10.30.1.10/24 scope global eth1\n"), nil, true
			}
		}
		return nil, nil, false
	}
}

const routedPlusPrivate = `{
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.4"},
    "eth1": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.4"},
    "eth1": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10"}
  }
}`

// TestARestartedMachineGetsItsPeeredRoutesBack is the test #549 names: without
// restoreGuestRoutes on the already-exists branch of Start, not one of these
// commands is emitted and the guest comes back reaching its own subnet alone.
func TestARestartedMachineGetsItsPeeredRoutesBack(t *testing.T) {
	f := &fakeRuntime{}
	restartedInstance(f, routedPlusPrivate)
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"}); err != nil {
		t.Fatalf("start an existing machine: %v", err)
	}

	for _, block := range privateAggregates {
		want := "exec srv -- ip route add " + block + " via 10.30.1.1 dev eth1"
		if len(f.matching(want)) == 0 {
			t.Errorf("a restarted machine was not given back its route to %s:\n%s",
				block, strings.Join(f.commands(), "\n"))
		}
	}
}

// A machine that never went down must not pay for the repair either: the same
// commands are what a poweron on an already-running machine emits, and they are
// idempotent by construction ("file exists" is a previous call's work standing).
// This is the accepting half — a guard that refused everything would pass every
// negative test and break the product.
func TestRestoringRoutesIsIdempotent(t *testing.T) {
	f := &fakeRuntime{}
	restartedInstance(f, routedPlusPrivate)
	f.fail = map[string]error{
		"ip route add": errors.New("incus: Error: RTNETLINK answers: File exists"),
	}
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"}); err != nil {
		t.Fatalf("a second restore must not fail the start: %v", err)
	}
}

// TestRestoringRoutesLeavesAForeignNICAlone: the name of the network comes off
// the runtime, and a NIC an operator added by hand to one of our machines is
// theirs. Nothing is read from it and nothing is written through it.
func TestRestoringRoutesLeavesAForeignNICAlone(t *testing.T) {
	const foreign = `{
	  "devices": {
	    "eth1": {"type": "nic", "network": "incusbr0", "ipv4.address": "10.76.154.10"}
	  },
	  "expanded_devices": {
	    "eth1": {"type": "nic", "network": "incusbr0", "ipv4.address": "10.76.154.10"}
	  }
	}`
	f := &fakeRuntime{}
	restartedInstance(f, foreign)
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"}); err != nil {
		t.Fatalf("start: %v", err)
	}

	for _, forbidden := range []string{"network get incusbr0", "ip route add"} {
		if got := f.matching(forbidden); len(got) != 0 {
			t.Errorf("the repair reached a network the emulator did not create:\n%s",
				strings.Join(got, "\n"))
		}
	}
}

// The bridge mode has no router of its own and no peerings, so there is nothing
// to put back there. Asserted rather than assumed: the aggregates point at an
// OVN router, and laying them on a bridge would send a guest's private traffic
// at an address that answers nothing.
func TestABridgeModeRestartLaysNoAggregates(t *testing.T) {
	f := &fakeRuntime{}
	restartedInstance(f, routedPlusPrivate)
	d := newFakeDriver(f) // OVN is false

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := f.matching("ip route add"); len(got) != 0 {
		t.Errorf("bridge mode laid OVN aggregates:\n%s", strings.Join(got, "\n"))
	}
}

// A guest that never finishes configuring its interface must be reported, not
// waited through for ever, and the report must name the machine and the device
// — an operator reading "the machine is running" and finding it unreachable is
// the state #549 was.
func TestAGuestThatNeverConfiguresItsInterfaceIsReported(t *testing.T) {
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch args[0] {
		case "list":
			return []byte(`[{"name":"srv","status":"Running","state":{"network":{}}}]`), nil, true
		case "query":
			if strings.HasSuffix(args[len(args)-1], "/1.0/instances/srv") {
				return []byte(routedPlusPrivate), nil, true
			}
		case "exec":
			return nil, errors.New("incus: Error: Command not found"), true
		}
		return nil, nil, false
	}
	d := ovnDriver(f)
	d.routePoll = time.Millisecond
	d.routeBudget = 5 * time.Millisecond

	err := d.restoreGuestRoutes(context.Background(), "srv")
	if err == nil {
		t.Fatal("a guest that never configured its interface was reported as repaired")
	}
	for _, want := range []string{"srv", "eth1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not name %q: %v", want, err)
		}
	}
}

// And the start still succeeds: the machine is up, and answering FailedState
// for a machine the runtime is running is the lie in the other direction.
func TestAFailedRouteRestoreStillReportsTheMachineStarted(t *testing.T) {
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch args[0] {
		case "list":
			return []byte(`[{"name":"srv","status":"Running","state":{"network":{}}}]`), nil, true
		case "query":
			if strings.HasSuffix(args[len(args)-1], "/1.0/instances/srv") {
				return []byte(routedPlusPrivate), nil, true
			}
		case "exec":
			return nil, errors.New("incus: Error: Command not found"), true
		}
		return nil, nil, false
	}
	d := ovnDriver(f)
	d.routePoll = time.Millisecond
	d.routeBudget = 5 * time.Millisecond

	m, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"})
	if err != nil {
		t.Fatalf("a machine that is running must not be reported as a failed start: %v", err)
	}
	if m.Name != "srv" {
		t.Errorf("the started machine is %q, want srv", m.Name)
	}
}
