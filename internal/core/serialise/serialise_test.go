package serialise

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The property is exclusion on one domain. Everything else this package
// promises hangs off it, so it is measured rather than assumed.
func TestLockExcludesTwoHoldersOfOneDomain(t *testing.T) {
	var inside atomic.Int32
	var overlap atomic.Bool
	var wg sync.WaitGroup

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := Lock("pool-1")
			defer release()

			if inside.Add(1) > 1 {
				overlap.Store(true)
			}
			// Long enough that an unserialised caller is inside at the same
			// time rather than merely likely to be.
			time.Sleep(2 * time.Millisecond)
			inside.Add(-1)
		}()
	}
	wg.Wait()

	if overlap.Load() {
		t.Fatal("two holders were inside one domain at the same time")
	}
}

// The accepting half, and it is not decoration: a single global lock passes
// the test above and queues every domain of the process behind one slow
// holder. The address pools of three packs and every machine target go through
// this map, so "keyed" is a throughput property, not a naming convenience.
func TestLockDoesNotQueueDifferentDomains(t *testing.T) {
	const domains = 4
	var arrived sync.WaitGroup
	arrived.Add(domains)
	release := make(chan struct{})
	done := make(chan struct{})

	for i := range domains {
		go func() {
			free := Lock(string(rune('a' + i)))
			defer free()
			arrived.Done()
			<-release
		}()
	}

	go func() {
		arrived.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("four different domains could not be held at once: the lock is global, not per domain")
	}
	close(release)
}

// A map that keeps one mutex per domain a session ever named is the memory
// sink an emulator must not become. The refcount is what stops it, and it is
// exactly the kind of bookkeeping that reads as correct and leaks.
func TestADomainIsForgottenOnceNobodyHoldsIt(t *testing.T) {
	for i := range 100 {
		release := Lock(string(rune('a' + i%26)))
		release()
	}

	locks.mu.Lock()
	held := len(locks.held)
	locks.mu.Unlock()

	if held != 0 {
		t.Fatalf("%d domains are still held after every caller left", held)
	}
}

// A release called twice unlocks a mutex the caller no longer holds, which
// panics. Ordinary Go style produces it: a defer plus an explicit call on an
// early return.
func TestReleaseIsIdempotent(t *testing.T) {
	release := Lock("pool-1")
	release()
	release()

	// And the domain is still usable afterwards: a double release must not
	// leave the lock held by a ghost.
	again := Lock("pool-1")
	again()
}
