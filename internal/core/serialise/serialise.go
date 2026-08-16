// Package serialise provides keyed mutual exclusion: one lock per named
// domain, held for the duration of a transaction the store lock cannot cover.
//
// It exists because every consumer used to grow its own copy of the same
// mechanism. The machine binding kept a per-target map for lifecycle
// transitions, and each provider pack carried a sync.Mutex for its address
// allocation — rebuilt from the store, an address chosen, the result
// persisted, a read-modify-write the store's own lock is blind to because it
// only covers each single operation. That mutex was written once per pack, and
// the concurrency barrage measured what copies cost: the double-allocation it
// guards against was fixed in one pack and found alive in another, and a
// fourth pack would have had to discover the need all over again.
//
// The domain is opaque. Callers compose it from whatever must be exclusive —
// a provider and a resource id, a provider and an address pool — and this
// package never parses it, so no provider name is ever known here (rule 5).
// Two rules for composing one:
//
//   - carry the provider, so two packs numbering their resources the same way
//     never wait on each other;
//   - name the transaction, not the world: one domain per pool or per target,
//     never one for the whole emulator, which would queue every request behind
//     a single slow holder.
package serialise

import "sync"

// locks is the process-wide set of domains currently held or waited on.
var locks = &keyedMutex{held: make(map[string]*heldLock)}

type heldLock struct {
	mu sync.Mutex
	// refs counts holders and waiters, so the entry outlives the first
	// release only while somebody still needs it.
	refs int
}

type keyedMutex struct {
	mu   sync.Mutex
	held map[string]*heldLock
}

// Lock blocks until domain is free and returns the release.
//
// The entry is dropped once the last waiter is gone. Keeping it would grow the
// map by one mutex for every domain a session ever named, and an emulator that
// a client can turn into a memory sink is a defect of the same family as the
// races this lock closes.
func Lock(domain string) (release func()) {
	return locks.lock(domain)
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	l, ok := k.held[key]
	if !ok {
		l = &heldLock{}
		k.held[key] = l
	}
	l.refs++
	k.mu.Unlock()

	l.mu.Lock()

	// Once, because a caller that both defers the release and calls it on an
	// early return would otherwise unlock a mutex it no longer holds, which
	// panics and takes the server with it.
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Unlock()

			k.mu.Lock()
			l.refs--
			if l.refs == 0 {
				delete(k.held, key)
			}
			k.mu.Unlock()
		})
	}
}
