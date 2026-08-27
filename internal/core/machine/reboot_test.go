package machine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
)

// What a reboot is, held once for every pack (#547).
//
// Two packs wrote "a reboot is a stop then a start" and did it; the third filed
// reboot with poweron and called the start alone, under a comment saying the
// case was handled. The runtime refuses to relaunch a name it has already
// served, so on 2026-08-27 the action answered `success`, the API reported
// `running`, and the machine came out of it with the same container pid, an
// uptime still climbing and a transient marker unit still alive.

// rebootReconciler is the group-sync bench with a binding that names its
// running and failed states, which Reboot and PowerOn read.
func rebootReconciler(b *groupSyncBench, plan Plan) Reconciler {
	sync := b.sync()
	sync.Binding.RunningState = "running"
	sync.Binding.FailedState = "stopped"
	return Reconciler{
		Groups:      sync,
		PlanOf:      func(*testResource) Plan { return plan },
		PublicBlock: netip.MustParsePrefix("203.0.113.0/24"),
	}
}

// firstOf returns the index of the first gesture of that kind, or -1.
func firstOf(sequence []string, kind string) int {
	for i, got := range sequence {
		if got == kind {
			return i
		}
	}
	return -1
}

// TestARebootStopsTheMachineBeforeStartingIt is the whole of #547 at the level
// where it can be held deterministically: the gestures the pack asks of the
// runtime, in order.
func TestARebootStopsTheMachineBeforeStartingIt(t *testing.T) {
	b := newGroupSyncBench()
	vm := b.machine("m", "10.0.0.5")
	vm.State = "running"

	r := rebootReconciler(b, Plan{})
	if !r.Reboot(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
		t.Fatalf("the reboot did not bring the machine back; sequence: %v", b.rec.Sequence())
	}

	sequence := b.rec.Sequence()
	stop, start := firstOf(sequence, "Stop"), firstOf(sequence, "Start")
	if stop < 0 {
		t.Fatalf("the reboot never asked the runtime to stop the machine, so nothing restarted (#547); sequence: %v", sequence)
	}
	if start < 0 {
		t.Fatalf("the reboot never asked the runtime to start the machine; sequence: %v", sequence)
	}
	if stop > start {
		t.Fatalf("a reboot is a stop THEN a start; sequence: %v", sequence)
	}
	if vm.State != "running" {
		t.Errorf("state %q after a reboot the runtime honoured, want running", vm.State)
	}
}

// The accepting half of the state question, and the half a guard that refused
// everything would still pass: a reboot of a machine that is not up has nothing
// to take down, and asking the runtime to stop a stopped instance would log a
// failure for an ordinary case.
func TestARebootOfAStoppedMachineDoesNotStopItAgain(t *testing.T) {
	b := newGroupSyncBench()
	vm := b.machine("m", "10.0.0.5")
	vm.State = "stopped"

	r := rebootReconciler(b, Plan{})
	if !r.Reboot(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
		t.Fatalf("the reboot did not start the machine; sequence: %v", b.rec.Sequence())
	}
	if sequence := b.rec.Sequence(); firstOf(sequence, "Stop") >= 0 {
		t.Errorf("a machine that was not running was stopped anyway; sequence: %v", sequence)
	}
	if firstOf(b.rec.Sequence(), "Start") < 0 {
		t.Errorf("a reboot of a stopped machine must still start it; sequence: %v", b.rec.Sequence())
	}
}

// refusingDriver starts nothing, which is what a runtime out of disk, out of
// address space or simply down does.
type refusingDriver struct{ stopped []string }

func (d *refusingDriver) Name() string                   { return "refusing" }
func (d *refusingDriver) Available(context.Context) bool { return true }
func (d *refusingDriver) Start(context.Context, Spec) (Machine, error) {
	return Machine{}, errors.New("no space left on device")
}
func (d *refusingDriver) Stop(_ context.Context, name string) error {
	d.stopped = append(d.stopped, name)
	return nil
}
func (d *refusingDriver) Remove(context.Context, string) error { return nil }
func (d *refusingDriver) Inspect(_ context.Context, name string) (Machine, bool, error) {
	return Machine{Name: name}, true, nil
}
func (d *refusingDriver) EnsureNetwork(context.Context, NetworkSpec) error { return nil }
func (d *refusingDriver) Attach(context.Context, string, Attachment) error { return nil }
func (d *refusingDriver) Detach(context.Context, string, string) error     { return nil }
func (d *refusingDriver) RemoveNetwork(context.Context, string) error      { return nil }

// The answer to "what does a reboot the runtime refused publish?", which is the
// question #547 makes unavoidable: a control plane that answers `success` on an
// action it did not perform is the lie this project exists to refuse. The shape
// is #484's, not a new one — the binding writes FailedState, in the provider's
// own vocabulary.
func TestARebootTheRuntimeRefusesPublishesTheFailedState(t *testing.T) {
	driver := &refusingDriver{}
	binding := Binding{
		driver:       driver,
		Provider:     "acme",
		Prefix:       "feint-acme-",
		RuntimeKey:   "machine",
		AddressKey:   "address",
		RunningState: "running",
		FailedState:  "stopped",
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := Reconciler{
		Groups: GroupSync{Binding: binding},
		PlanOf: func(*resource.Resource) Plan { return Plan{} },
	}
	res := &resource.Resource{
		ID:      "srv-1",
		State:   "running",
		Runtime: map[string]string{"machine": "feint-acme-srv-1", "address": "10.0.0.5"},
	}

	if r.Reboot(context.Background(), res, Boot{Image: "ubuntu:24.04"}) {
		t.Fatal("a reboot the runtime refused reported success")
	}
	if res.State != "stopped" {
		t.Errorf("state %q after a reboot nothing came back from, want the failed state", res.State)
	}
	if len(driver.stopped) != 1 || driver.stopped[0] != "feint-acme-srv-1" {
		t.Errorf("the machine was not taken down: %v", driver.stopped)
	}
	if res.Runtime["address"] != "" {
		t.Errorf("an address is still published for a machine that did not come back: %q", res.Runtime["address"])
	}
}

// With no runtime at all the control plane must keep working, which is the
// documented degraded mode every conformance suite in CI runs in.
func TestARebootWithoutARuntimeKeepsTheControlPlaneWorking(t *testing.T) {
	binding := Binding{
		Provider:     "acme",
		Prefix:       "feint-acme-",
		RuntimeKey:   "machine",
		AddressKey:   "address",
		RunningState: "running",
		FailedState:  "stopped",
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := Reconciler{
		Groups: GroupSync{Binding: binding},
		PlanOf: func(*resource.Resource) Plan { return Plan{} },
	}
	res := &resource.Resource{ID: "srv-1", State: "running", Runtime: map[string]string{}}

	if !r.Reboot(context.Background(), res, Boot{}) {
		t.Fatal("a reboot with no runtime configured must still reach running")
	}
	if res.State != "running" {
		t.Errorf("state %q, want running: metadata-only is the documented degraded mode", res.State)
	}
}
