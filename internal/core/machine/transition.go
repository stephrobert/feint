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
func (b Binding) Transition(st *store.Store, now func() time.Time, kind, id string, change func(*resource.Resource)) error {
	unlock := b.Serialise(id)
	defer unlock()

	res, found := st.Get(b.Provider, kind, id)
	if !found {
		return store.ErrNotFound
	}

	// The runtime work runs outside the store lock, on the copy Get handed
	// out. Holding the write lock across a launch would queue every other
	// request behind one machine starting.
	change(res)

	// The result is applied conditionally. Put replaces the whole resource,
	// so it silently discarded anything another request had written in the
	// meantime — a delete racing a lifecycle loop used to be undone, and the
	// VM came back, address included.
	return st.Update(b.Provider, kind, id, func(stored *resource.Resource) error {
		stored.State = res.State
		stored.Runtime = res.Runtime
		stored.Attrs = res.Attrs
		stored.Updated = now()
		return nil
	})
}
