package cli

import (
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// TestAPackThatDrivesMachinesReconcilesBothWays is cited by
// emulator.MissingReconciler, and it exists because of a defect that happened
// while that interface was being written.
//
// The first version carried both halves on ONE interface. Adding the second
// method turned every pack into a non-implementer: the type assertion in the
// reset handler failed, the reconciliation silently stopped happening, and the
// call still answered 200 with a clean-looking body. Nothing logged it. The
// only reason it surfaced is that a test asserted which records a pack had
// been HANDED rather than that the call succeeded.
//
// Splitting the interface in two removes that class of defect for the future —
// a pack gains one half when it is ready and keeps the other working — and
// this test closes the other side of it: a pack that drives machines must
// satisfy both, or a reset would quietly do half its job for that provider.
//
// The subject is "drives machines", and it is read from the pack itself rather
// than from a list of provider names: a fourth pack must inherit this without
// anyone editing a list here, which is rule 5 applied to a test.
func TestAPackThatDrivesMachinesReconcilesBothWays(t *testing.T) {
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}

	examined := 0
	for _, pack := range srv.Packs() {
		gone, reconciles := pack.(emulator.Reconciler)
		missing, starts := pack.(emulator.MissingReconciler)
		if !reconciles && !starts {
			// A pack that drives no machine implements neither, which is the
			// ordinary case and not a failure.
			continue
		}
		examined++
		if !reconciles {
			t.Errorf("%s can start machines again and cannot take them down: a reset would "+
				"leave every machine the checkpoint does not hold running", pack.Name())
		}
		if !starts {
			t.Errorf("%s can take machines down and cannot start them again: a reset would "+
				"return an operator to a checkpoint whose servers answer nothing", pack.Name())
		}
		_, _ = gone, missing
	}

	// A denominator, because a loop over nothing passes silently and would
	// have passed before either half existed.
	if examined < 3 {
		t.Fatalf("only %d pack(s) reconcile at all; this build drives machines for three", examined)
	}
}
