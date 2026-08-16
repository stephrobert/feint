package machine

import (
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// Transition applies one change to one resource, holding that target for the
// read, the runtime work and the write-back.
//
// This is the transactional half of a lifecycle action, and it is the same in
// every pack: serialise the target, check it exists, run the change outside
// the store lock — launching a container takes tens of seconds where the lock
// takes microseconds — and write the result back conditionally, so a delete
// landing mid-transition wins. Two packs had written it separately
// (transitionOne in Outscale, transitionInstance in Exoscale), which is the
// duplication that let a fixed defect survive in the other copy.
//
// What deliberately stays in the pack is everything a client can observe: the
// states a change moves between are API surface, and a not-found is answered
// in the pack's own error dialect. Transition only reports it, as
// store.ErrNotFound, whether the resource was never there or was deleted
// while its machine was transitioning — to the caller that asked for a state
// the resource no longer has, the two are the same answer.
// A lifecycle action always writes back — the client asked for a state change,
// and the answer is whatever the runtime produced. So this is Observe with the
// question already answered, and the two share one implementation: the
// transactional half written twice is how a fixed defect survives in the copy,
// which is the history of this very function.
func (b Binding) Transition(st *store.Store, now func() time.Time, kind, id string, change func(*resource.Resource)) error {
	_, err := b.Observe(st, now, kind, id, func(res *resource.Resource) bool {
		// The runtime work runs outside the store lock, on the copy Get handed
		// out. Holding the write lock across a launch would queue every other
		// request behind one machine starting.
		//
		// The result is then applied conditionally. Put replaces the whole
		// resource, so it silently discarded anything another request had
		// written in the meantime — a delete racing a lifecycle loop used to be
		// undone, and the VM came back, address included.
		change(res)
		return true
	})
	return err
}
