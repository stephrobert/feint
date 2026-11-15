package machine

import (
	"context"
	"testing"
)

// The address twin of the egress test beside it (#947).
//
// ReplayEgress passed the stored machine name through Binding.ours before it
// reached a command; route, the function both address doors walk, did not,
// while the comment on ReplayEgress named the address path as covered. The
// name comes from Resource.Runtime, which `PUT /_feint/state` and
// `feint snapshot load` restore verbatim.
//
// The router here is the Recorder, which asks nothing of the name it is
// handed. That is deliberate: the Incus driver asks its own question at
// RouteAddress's entry (mustOwnInstance, #933), so a test through it could not
// tell whether the shared layer asked. Through a driver that asks nothing,
// the shared layer's question is the only one, and its absence shows.

// routedMachines returns the machine every RouteAddress the recorder saw was
// asked to carry the address on.
func routedMachines(rec *Recorder) []string {
	var out []string
	for _, e := range rec.Events() {
		if e.Kind != "RouteAddress" {
			continue
		}
		if spec, ok := e.Args.(AddressSpec); ok {
			out = append(out, spec.Machine)
		}
	}
	return out
}

// TestAnAddressIsRoutedOnlyToAMachineTheEmulatorCouldHaveCreated drives both
// doors, Route (an address attached to a running machine) and ReplayAddresses
// (the late-address door), with a stored name the emulator never wrote.
//
// The shared layer asks one question, so every row fails it; the rows differ
// in what kind of foreign name they are, and the machine name is the table's
// parameter, not a constant: an instance of the host, a name the driver would
// read as a flag, and a name carrying another pack's prefix, which is well
// formed and is somebody's, just not this binding's.
func TestAnAddressIsRoutedOnlyToAMachineTheEmulatorCouldHaveCreated(t *testing.T) {
	const address = "203.0.113.9"
	for _, runtime := range []string{"production-database", "-h", "feint-scw-x"} {
		for _, door := range []string{"Route", "ReplayAddresses"} {
			t.Run(door+"/"+runtime, func(t *testing.T) {
				b := newGroupSyncBench()
				b.binding().ForgetPlacements()
				t.Cleanup(b.binding().ForgetPlacements)
				e := &egressRecorder{Recorder: b.rec}
				vm := b.machine("m", "10.0.0.5")
				vm.State = "running"
				// What a forged snapshot restores: a name the emulator never wrote.
				vm.Runtime["machine"] = runtime

				r := egressReconciler(b, e, Plan{Publics: []string{address}})
				switch door {
				case "Route":
					r.Route(context.Background(), vm, address)
				default:
					r.ReplayAddresses(context.Background(), vm)
				}

				got := routedMachines(b.rec)
				if len(got) == 0 {
					t.Fatalf("the address was not routed at all; the question falls back to the derived name, it does not refuse the route: %v",
						b.rec.Events())
				}
				for _, machine := range got {
					if machine == runtime {
						t.Fatalf("the address was routed onto %q, a name the emulator could not have created;\n"+
							"a crafted snapshot reaches somebody else's instance through any driver that asks nothing", runtime)
					}
					if machine != "feint-bench-m" {
						t.Errorf("the address went to %q; the derived name is feint-bench-m", machine)
					}
				}
			})
		}
	}
}

// TestAnAddressStillRoutesToAMachineWeCreated is the accepting half: a guard
// that replaced every name would pass the test above and route every address
// onto the derived name even when the stored one is legitimate. Here the
// stored name is the binding's own, and it is the one carried, through both
// doors.
func TestAnAddressStillRoutesToAMachineWeCreated(t *testing.T) {
	const address = "203.0.113.10"
	for _, door := range []string{"Route", "ReplayAddresses"} {
		t.Run(door, func(t *testing.T) {
			b := newGroupSyncBench()
			b.binding().ForgetPlacements()
			t.Cleanup(b.binding().ForgetPlacements)
			e := &egressRecorder{Recorder: b.rec}
			vm := b.machine("m", "10.0.0.5")
			vm.State = "running"
			// A name the binding writes, but not the one it would derive for
			// this ID: so the test can tell "kept" from "replaced by the
			// fallback".
			vm.Runtime["machine"] = "feint-bench-renamed"

			r := egressReconciler(b, e, Plan{Publics: []string{address}})
			switch door {
			case "Route":
				r.Route(context.Background(), vm, address)
			default:
				r.ReplayAddresses(context.Background(), vm)
			}

			got := routedMachines(b.rec)
			if len(got) != 1 || got[0] != "feint-bench-renamed" {
				t.Fatalf("a machine the emulator created must keep its address on its own name, got %v", got)
			}
		})
	}
}

// TestAnUnrouteTakesBackTheRouteItsRouteLaid is the regression #947 opened and
// a contradicting review found. route asks Binding.ours and, for a stored name
// the emulator never wrote, lays the address on the derived name. Unroute was
// handed the raw stored name by every pack, so it took the address back from a
// machine that never held it, and the route on the derived name stayed: the
// machine kept answering on an address the API said was released. Before
// #947 nothing was laid at all.
//
// So the two ask the same question at the same place, and the test is the
// pair: whatever machine Route laid the address on, Unroute takes it back
// from that machine, for a foreign stored name and for a legitimate one.
func TestAnUnrouteTakesBackTheRouteItsRouteLaid(t *testing.T) {
	const address = "203.0.113.11"
	for _, runtime := range []string{"production-database", "feint-bench-renamed"} {
		t.Run(runtime, func(t *testing.T) {
			b := newGroupSyncBench()
			b.binding().ForgetPlacements()
			t.Cleanup(b.binding().ForgetPlacements)
			e := &egressRecorder{Recorder: b.rec}
			vm := b.machine("m", "10.0.0.5")
			vm.State = "running"
			vm.Runtime["machine"] = runtime

			r := egressReconciler(b, e, Plan{Publics: []string{address}})
			r.Route(context.Background(), vm, address)
			unrouteAsAPackDoes(r, vm, address)

			laid := map[string]int{}
			for _, ev := range b.rec.Events() {
				switch ev.Kind {
				case "RouteAddress":
					if spec, ok := ev.Args.(AddressSpec); ok {
						laid[spec.Machine]++
					}
				case "UnrouteAddress":
					if machine, ok := ev.Args.(string); ok {
						laid[machine]--
					}
				}
			}
			for machine, n := range laid {
				if n > 0 {
					t.Fatalf("the address is still routed onto %q after the unroute: Route and Unroute "+
						"aimed at different machines; events: %v", machine, b.rec.Sequence())
				}
			}
			if len(laid) == 0 {
				t.Fatalf("nothing was routed or unrouted: %v", b.rec.Sequence())
			}
		})
	}
}

// unrouteAsAPackDoes is the call every pack makes when an address is
// detached: the resource it was attached to, and the address.
func unrouteAsAPackDoes(r Reconciler, res *testResource, address string) {
	r.Unroute(context.Background(), res, address)
}
