package machine

import (
	"context"
	"strings"
	"testing"
)

// The egress path asked neither of the two questions, and it is the one the hot
// doors walk on every poweron, join and address route.
//
// `Runtime` is restored verbatim by `PUT /_feint/state` and by
// `feint snapshot load`, and the snapshot format is documented as meant to
// outlive its instance. A crafted snapshot naming an instance of the host
// therefore reached `incus exec <that instance> -- ip route del default`, and a
// name starting with a dash reached the driver as a flag.
//
// Stop, Remove and Addresses already ask it. This one did not. Measured by a
// security audit on 2026-11-13:
//
//	drop:  [DropEgress production-database]
//	route: [RouteEgress -h via incusbr0]
func TestEgressRefusesAMachineTheEmulatorCouldNotHaveCreated(t *testing.T) {
	for _, c := range []struct {
		name    string
		runtime string
	}{
		{name: "an instance of the host", runtime: "production-database"},
		{name: "a name the driver would read as a flag", runtime: "-h"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newGroupSyncBench()
			e := &egressRecorder{Recorder: b.rec}
			vm := b.machine("m", "10.0.0.5")
			vm.State = "running"
			// What a forged snapshot restores: a name the emulator never wrote.
			vm.Runtime["machine"] = c.runtime

			r := egressReconciler(b, e, Plan{Egress: "fnt-x"})
			r.ReplayAddresses(context.Background(), vm)

			for _, laid := range e.laid {
				if strings.Contains(laid, c.runtime) {
					t.Errorf("the egress path carried a name the emulator could not have created: %q\n"+
						"a crafted snapshot would reach the operator's own instance", laid)
				}
			}
		})
	}
}

// The accepting half, which a guard that refuses everything would also pass: a
// machine the emulator did create still gets its route laid.
func TestEgressStillLaysTheRouteForAMachineWeCreated(t *testing.T) {
	b := newGroupSyncBench()
	e := &egressRecorder{Recorder: b.rec}
	vm := b.machine("m", "10.0.0.5")
	vm.State = "running"

	r := egressReconciler(b, e, Plan{Egress: "fnt-x"})
	r.ReplayAddresses(context.Background(), vm)

	if len(e.laid) != 1 || !strings.HasSuffix(e.laid[0], "via fnt-x") {
		t.Fatalf("an ordinary machine did not get its egress laid: %v", e.laid)
	}
	if !strings.Contains(e.laid[0], "feint-bench-m") {
		t.Errorf("the name carried is %q, and the bench writes feint-bench-m", e.laid[0])
	}
}
