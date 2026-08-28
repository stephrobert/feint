package machine

import (
	"context"
	"reflect"
	"testing"
)

// The recorder is an instrument, and an instrument that lies reads exactly
// like proof. So this file holds the instrument itself: the vocabulary against
// the interfaces it claims to cover, the unknown-gesture detector against a
// planted unknown, and the recording against a known lifecycle — the accepting
// half, because a detector that refuses everything passes every planted test
// and measures nothing.

// contractInterfaces is the surface the vocabulary must cover: Driver and its
// optional halves, exactly the set NewRecorder implements.
var contractInterfaces = map[string]reflect.Type{
	"Driver":     reflect.TypeOf((*driver)(nil)).Elem(),
	"Router":     reflect.TypeOf((*router)(nil)).Elem(),
	"Firewaller": reflect.TypeOf((*firewaller)(nil)).Elem(),
	"Peerer":     reflect.TypeOf((*peerer)(nil)).Elem(),
	"Isolator":   reflect.TypeOf((*isolator)(nil)).Elem(),
	"Balancer":   reflect.TypeOf((*balancer)(nil)).Elem(),
	"Capable":    reflect.TypeOf((*Capable)(nil)).Elem(),
}

// TestTheContractNamesEveryGesture holds the vocabulary against the driver
// interfaces, in both directions. A method added to Driver or an optional half
// without a vocabulary row is a gesture the contract cannot name — the exact
// hole property 2 of #515 exists to close — and a row naming no method is
// fiction that would let a planted event of that name pass as legitimate.
func TestTheContractNamesEveryGesture(t *testing.T) {
	methods := map[string]bool{}
	for name, iface := range contractInterfaces {
		for i := 0; i < iface.NumMethod(); i++ {
			method := iface.Method(i).Name
			methods[method] = true
			if !KnownGesture(method) {
				t.Errorf("%s.%s is a gesture the contract cannot name: add it to contractGestures, with its class", name, method)
			}
		}
	}
	for kind := range contractGestures {
		if !methods[kind] {
			t.Errorf("the vocabulary names %q and no driver interface carries it: a planted event of that name would pass as legitimate", kind)
		}
	}
}

// TestAPlantedUnknownGestureIsReported is the positive control of the
// unknown-gesture detector. A control that looks for absence must first prove
// it can find: without this plant, OutsideContract returning nothing is
// indistinguishable from OutsideContract looking nowhere.
func TestAPlantedUnknownGestureIsReported(t *testing.T) {
	r := NewRecorder()
	if _, err := r.Start(context.Background(), Spec{Name: "feint-x-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if outside := r.OutsideContract(); len(outside) != 0 {
		t.Fatalf("a contract gesture was reported as outside it: %v", outside)
	}

	r.Record(Gesture{Kind: "FormatDisk", Resource: "feint-x-1"})

	outside := r.OutsideContract()
	if len(outside) != 1 || outside[0].Kind != "FormatDisk" {
		t.Fatalf("the planted unknown gesture was not reported: got %v, want the FormatDisk event", outside)
	}
}

// TestARecorderReplaysTheLifecycleInCallOrder is the accepting half: the
// recorder behaves as a working runtime for the ordinary lifecycle, and its
// sequence is the calls, in order, host-changing gestures only. Reads are
// deliberately absent — packs legitimately differ in how often they look, and
// a sequence polluted by Inspect would make the cross-pack equivalence lie.
func TestARecorderReplaysTheLifecycleInCallOrder(t *testing.T) {
	r := NewRecorder()
	ctx := context.Background()

	if err := r.EnsureNetwork(ctx, NetworkSpec{Name: "feint-net-1", CIDR: "10.230.0.0/24"}); err != nil {
		t.Fatalf("ensure network: %v", err)
	}
	m, err := r.Start(ctx, Spec{Name: "feint-x-1", Attachments: []Attachment{{Network: "feint-net-1", Address: "10.230.0.7"}}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(m.Addresses) != 1 || m.Addresses[0] != "10.230.0.7" {
		t.Fatalf("the machine does not carry its attachment's address: %v", m.Addresses)
	}
	if got, found, _ := r.Inspect(ctx, "feint-x-1"); !found || !got.Running {
		t.Fatalf("a started machine inspects as %+v, found=%v", got, found)
	}
	if err := r.Stop(ctx, "feint-x-1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got, found, _ := r.Inspect(ctx, "feint-x-1"); !found || got.Running {
		t.Fatalf("a stopped machine inspects as %+v, found=%v", got, found)
	}
	if err := r.Remove(ctx, "feint-x-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, found, _ := r.Inspect(ctx, "feint-x-1"); found {
		t.Fatal("a removed machine is still there")
	}
	// Removing what is already gone succeeds, as the contract requires.
	if err := r.Remove(ctx, "feint-x-1"); err != nil {
		t.Fatalf("second remove: %v", err)
	}

	want := []string{"EnsureNetwork", "Start", "Stop", "Remove", "Remove"}
	if got := r.Sequence(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence %v, want %v: the recording is not the calls in order, and every property built on it would lie", got, want)
	}
}
