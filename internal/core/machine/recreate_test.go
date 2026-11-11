package machine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

// startingDriver records what a reconciliation asked to start, and can refuse.
type startingDriver struct {
	recordingDriver
	started []string
	refuse  bool
}

func (d *startingDriver) Start(_ context.Context, spec Spec) (Machine, error) {
	d.started = append(d.started, spec.Name)
	if d.refuse {
		return Machine{}, errors.New("the runtime refused")
	}
	return Machine{Name: spec.Name, Addresses: []string{"10.42.0.9"}, Running: true}, nil
}

func absent(id string) *resource.Resource {
	res := resource.New(id, "instance/server",
		resource.Tenant{Provider: "acme", Project: "p", Zone: "z"}, "running",
		time.Unix(1700000000, 0).UTC())
	res.Runtime = map[string]string{"machine": "feint-acme-" + id}
	return res
}

// TestReconcileStartsOnlyTheMachinesThatAreMissing is cited by
// ReconcileMissing and by the three packs that delegate to it.
//
// The measurement that sequenced this half last is worth repeating here,
// because it turned out the other way: a Boot rebuilt from a restored record
// is the Boot the original had, since every input is the record itself, the
// store the reset has already restored, or the image catalogue compiled into
// the binary. So what is left to get wrong is the reporting, and that is what
// these four populations are about.
func TestReconcileStartsOnlyTheMachinesThatAreMissing(t *testing.T) {
	boot := func(res *resource.Resource) Boot {
		return Boot{Image: "img", User: "root", Hostname: res.ID}
	}

	t.Run("a record with no machine on the host is started again", func(t *testing.T) {
		d := &startingDriver{}
		first, second := absent("a"), absent("b")
		done := bootBinding(d).ReconcileMissing(t.Context(),
			[]*resource.Resource{first, second}, nil, boot)
		if len(d.started) != 2 {
			t.Fatalf("started %v", d.started)
		}
		if len(done.Recreated) != 2 || len(done.Failed) != 0 {
			t.Errorf("reported %+v", done)
		}
		// The record is rewritten, which is the half that makes the next
		// reset a no-op: a resource still naming the container that is gone
		// would be reported missing again, forever.
		if first.Runtime["machine"] == "" || first.State != "running" {
			t.Errorf("the record was not brought up to date: %+v", first.Runtime)
		}
	})

	t.Run("a runtime that refuses is Failed, not Recreated", func(t *testing.T) {
		// PowerOn answers false and sets FailedState rather than Running. A
		// reset that folded this into Recreated would say it brought a machine
		// back that is in FailedState, and the verdict above it compares the
		// observable state anyway — so the lie would surface loudly, in the
		// wrong place.
		d := &startingDriver{refuse: true}
		res := absent("a")
		done := bootBinding(d).ReconcileMissing(t.Context(), []*resource.Resource{res}, nil, boot)
		if len(done.Recreated) != 0 {
			t.Errorf("a refused start was reported as recreated: %+v", done)
		}
		if !slices.Equal(done.Failed, []string{"feint-acme-a"}) {
			t.Errorf("failed %v", done.Failed)
		}
		if res.State != "failed" {
			t.Errorf("the record says %q after a refused start", res.State)
		}
	})

	t.Run("no runtime starts nothing and says so", func(t *testing.T) {
		// Metadata only is the DEFAULT. PowerOn would answer running without
		// starting anything, which is the documented degraded mode and not a
		// recreation to report.
		done := bootBinding(nil).ReconcileMissing(t.Context(),
			[]*resource.Resource{absent("a")}, nil, boot)
		if len(done.Recreated) != 0 || len(done.Failed) != 0 {
			t.Errorf("a binding with no runtime reported %+v", done)
		}
		if !slices.Equal(done.Skipped, []string{"feint-acme-a"}) {
			t.Errorf("skipped %v", done.Skipped)
		}
	})

	t.Run("nothing missing and no boot builder are both no-ops", func(t *testing.T) {
		d := &startingDriver{}
		if done := bootBinding(d).ReconcileMissing(t.Context(), nil, nil, boot); done.Recreated != nil {
			t.Errorf("an empty reconciliation did something: %+v", done)
		}
		// A nil builder is a programming error upstream, and starting a
		// machine from a zero Boot would be worse than doing nothing: it would
		// boot an image nobody asked for.
		if done := bootBinding(d).ReconcileMissing(t.Context(),
			[]*resource.Resource{absent("a")}, nil, nil); len(done.Recreated) != 0 {
			t.Errorf("a nil boot builder started something: %+v", done)
		}
		if len(d.started) != 0 {
			t.Errorf("commands were emitted: %v", d.started)
		}
	})
	t.Run("a machine the host still holds is left alone", func(t *testing.T) {
		// This is where the decision moved to, and why. The core used to pick
		// the missing records itself, with a "feint-" prefix: a provider's
		// vocabulary in the neutral layer, and broad enough to match a test's
		// own bookkeeping stamp, which it then tried to boot. The binding
		// knows its RuntimeKey, so it decides.
		d := &startingDriver{}
		res := absent("a")
		done := bootBinding(d).ReconcileMissing(t.Context(),
			[]*resource.Resource{res}, []string{"feint-acme-a"}, boot)
		if len(d.started) != 0 {
			t.Errorf("a machine the host holds was started again: %v", d.started)
		}
		if len(done.Recreated) != 0 || len(done.Failed) != 0 || len(done.Skipped) != 0 {
			t.Errorf("reported %+v for a record that needed nothing", done)
		}
	})

	t.Run("a record whose key names no machine is not a candidate", func(t *testing.T) {
		d := &startingDriver{}
		res := absent("a")
		res.Runtime = map[string]string{"address": "10.0.0.5"}
		if done := bootBinding(d).ReconcileMissing(t.Context(),
			[]*resource.Resource{res}, nil, boot); len(done.Recreated) != 0 {
			t.Errorf("a record with no machine name was started: %+v", done)
		}
		if len(d.started) != 0 {
			t.Errorf("commands were emitted: %v", d.started)
		}
	})
}
