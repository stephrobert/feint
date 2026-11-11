package machine

import (
	"slices"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
)

// TestAResetLeavesAStoppedRecordStopped is cited by ReconcileMissing.
//
// ## The defect it closes, as it was measured
//
// A record keeps its machine name after the machine is gone — that is how
// ReconcileGone finds what to destroy — so "names a machine and the host does
// not hold it" is also, word for word, the description of a server the
// checkpoint says is stopped. The first version of this half read only those
// two facts, so a reset started a container for a terminated VM and then wrote
// `running` over the state the checkpoint held.
//
// The runtime leg said it plainly, and the shape of the complaint is worth
// keeping: the verdict reported, as differences the lab had failed to
// reconcile, the very resource the reset had itself changed.
//
//	resource present and not in the checkpoint:    vm i-a3f536ec state=running
//	resource in the checkpoint and not present:    vm i-a3f536ec state=terminated
//	machine present and not in the checkpoint:     feint-osc-i-a3f536ec
//
// ## Why the comparison is here and not in the packs
//
// Each cloud spells a running machine its own way, and the Binding already
// carries that word as a field because PowerOn writes it. So the word varies
// and the gesture does not, which is this repository's own test for where a
// line belongs. Written in each pack, the next pack would have been the one
// that forgot.
//
// The accepting half is asserted too: a guard that refused every record would
// pass the failing case above and silently turn the whole recreating half off.
func TestAResetLeavesAStoppedRecordStopped(t *testing.T) {
	boot := func(res *resource.Resource) Boot {
		return Boot{Image: "img", User: "root", Hostname: res.ID}
	}

	stopped := absent("a")
	stopped.State = "terminated"
	running := absent("b")

	d := &startingDriver{}
	done := bootBinding(d).ReconcileMissing(t.Context(),
		[]*resource.Resource{stopped, running}, nil, boot)

	// The refusing half: nothing was started for the stopped record, which is
	// the assertion the host pays for.
	if slices.Contains(d.started, "feint-acme-a") {
		t.Errorf("a reset started a container for a record the checkpoint describes as stopped: %v",
			d.started)
	}
	if stopped.State != "terminated" {
		t.Errorf("the record says %q; a reset that rewrites the state it was asked to return to "+
			"then reports its own change as a difference", stopped.State)
	}
	if slices.Contains(done.Recreated, "feint-acme-a") {
		t.Errorf("a stopped record was reported recreated: %+v", done)
	}

	// The accepting half, in the same pass so neither can be satisfied alone.
	if !slices.Contains(d.started, "feint-acme-b") {
		t.Errorf("the running record was not started again: %v", d.started)
	}
	if !slices.Contains(done.Recreated, "feint-acme-b") {
		t.Errorf("recreated %v", done.Recreated)
	}
}
