package machine

import (
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// Observe is Transition for a read that writes, and there are more of those than
// the word "read" suggests.
//
// A virtual machine gets its address tens of seconds after it starts, so no pack
// can publish one at create time. All three fill it in on the next read instead —
// which turns GET into a writer, through a door nobody thinks of as mutating.
// Outscale's readVms says so in a comment and serialises; Scaleway's getServer
// and Exoscale's getInstance and listInstances did neither, and that is the
// asymmetry #211 was filed for.
//
// The race it closes is not theoretical, and it is worse than a lost field:
//
//	getServer            reads the server, sees running, asks the runtime
//	                     for its address — tens of seconds
//	serverAction poweroff takes the lock, stops the machine, withdraws the
//	                     address, commits "stopped"
//	getServer            commits its copy: "running", address and all
//
// Commit refuses to resurrect a *deleted* resource and orders nothing else, so
// the read wins and the emulator ends up describing as running a machine that is
// stopped. That is the exact lie this project exists to avoid, arriving through
// a GET.
//
// Two differences from Transition, and both are why this is a second entry point
// rather than a flag on the first:
//
//   - the resource is re-read *inside* the hold, so the copy the caller renders
//     cannot predate the lock it just took. Serialising around a copy fetched
//     earlier still writes back a stale read, which is what readVms did;
//   - the write-back is conditional on look() reporting a change. Committing
//     unconditionally would stamp Updated on every GET, and Updated is
//     modification_date on the wire: a client that diffs it would see a resource
//     change every time it looked at it.
//
// The resource is returned so the caller renders what was written rather than
// what it read, and store.ErrNotFound covers both "never existed" and "deleted
// while the runtime was answering" — to a caller holding a copy of something the
// store no longer has, those are the same answer.
func (b Binding) Observe(
	st *store.Store,
	now func() time.Time,
	kind, id string,
	look func(*resource.Resource) bool,
) (*resource.Resource, error) {
	unlock := b.Serialise(id)
	defer unlock()

	res, found := st.Get(b.Provider, kind, id)
	if !found {
		return nil, store.ErrNotFound
	}

	// Outside the store lock on purpose: look() reaches the runtime, and holding
	// a store-wide lock across a runtime call would queue every other request in
	// the process behind one machine answering.
	if !look(res) {
		return res, nil
	}

	if err := st.Update(b.Provider, kind, id, func(stored *resource.Resource) error {
		stored.State = res.State
		stored.Runtime = res.Runtime
		stored.Attrs = res.Attrs
		stored.Updated = now()
		return nil
	}); err != nil {
		return nil, err
	}
	return res, nil
}
