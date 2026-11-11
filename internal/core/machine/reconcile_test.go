package machine

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

// removingDriver records the names a reconciliation asked to remove, which is
// the only thing worth asserting here: a reconciliation is judged by the
// commands it emits, not by what it returns about itself.
type removingDriver struct {
	recordingDriver
	removed []string
}

func (d *removingDriver) Remove(_ context.Context, name string) error {
	d.removed = append(d.removed, name)
	return nil
}

func record(id, machine string) *resource.Resource {
	res := resource.New(id, "instance/server",
		resource.Tenant{Provider: "acme", Project: "p", Zone: "z"}, "running",
		time.Unix(1700000000, 0).UTC())
	if machine != "" {
		res.Runtime = map[string]string{"machine": machine}
	}
	return res
}

// TestReconcileDestroysOnlyTheMachinesItOwns is cited by ReconcileGone.
//
// #853's gap in one sentence: a restore brings the control plane back and
// touches nothing else, so a resource that disappeared leaves its container
// running and the API no longer mentions it. This is the gesture that closes
// it, and the four populations below are the ones that can go wrong.
func TestReconcileDestroysOnlyTheMachinesItOwns(t *testing.T) {
	t.Run("a record with a machine of ours is destroyed", func(t *testing.T) {
		d := &removingDriver{}
		done := bootBinding(d).ReconcileGone(t.Context(), []*resource.Resource{
			record("a", "feint-acme-a"),
			record("b", "feint-acme-b"),
		})
		if !slices.Equal(d.removed, []string{"feint-acme-a", "feint-acme-b"}) {
			t.Errorf("removed %v", d.removed)
		}
		if len(done.Destroyed) != 2 || len(done.Skipped) != 0 {
			t.Errorf("reported %+v", done)
		}
	})

	t.Run("a record with no machine is not an event", func(t *testing.T) {
		// The ordinary case for an address, a volume or a security group.
		// Reporting it would bury the machines under records that never had
		// one.
		d := &removingDriver{}
		done := bootBinding(d).ReconcileGone(t.Context(), []*resource.Resource{record("a", "")})
		if len(d.removed) != 0 {
			t.Errorf("removed %v for a record that named no machine", d.removed)
		}
		if len(done.Destroyed) != 0 || len(done.Skipped) != 0 {
			t.Errorf("reported %+v", done)
		}
	})

	t.Run("a foreign name reaches no command as itself", func(t *testing.T) {
		// A restored store is untrusted input: snapshot.go documents the
		// format as meant to outlive its instance, and an audit turned a
		// crafted one into a forced removal of a name the emulator never
		// created. The reconciliation must not be the path that reopens it.
		d := &removingDriver{}
		bootBinding(d).ReconcileGone(t.Context(), []*resource.Resource{
			record("a", "production-database"),
		})
		if slices.Contains(d.removed, "production-database") {
			t.Fatalf("a name outside this binding's prefix reached a removal: %v", d.removed)
		}
		// And it did not become a silent no-op either: the derived name is the
		// only machine that record could ever have had, so that is what is
		// removed. A reconciliation that skipped it would leave the real
		// container of a tampered record running.
		if !slices.Equal(d.removed, []string{"feint-acme-a"}) {
			t.Errorf("removed %v, want the derived name", d.removed)
		}
	})

	t.Run("no runtime destroys nothing and says so", func(t *testing.T) {
		// Metadata only is the DEFAULT, so this is the common case rather
		// than an edge one. An empty Destroyed read as success is how a
		// caller comes to believe containers were removed on a station that
		// never started any.
		done := bootBinding(nil).ReconcileGone(t.Context(), []*resource.Resource{
			record("a", "feint-acme-a"),
		})
		if len(done.Destroyed) != 0 {
			t.Errorf("a binding with no runtime claims to have destroyed %v", done.Destroyed)
		}
		if !slices.Equal(done.Skipped, []string{"feint-acme-a"}) {
			t.Errorf("skipped %v", done.Skipped)
		}
	})

	t.Run("nothing to reconcile emits nothing", func(t *testing.T) {
		d := &removingDriver{}
		done := bootBinding(d).ReconcileGone(t.Context(), nil)
		if len(d.removed) != 0 || len(done.Destroyed) != 0 || len(done.Skipped) != 0 {
			t.Errorf("an empty reconciliation did something: %v %+v", d.removed, done)
		}
	})
}
