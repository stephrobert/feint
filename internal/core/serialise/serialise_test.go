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

	// left is not tidiness. Without it this test returns while its four
	// holders are still on their way out of Lock, and the next test reads
	// locks.held — a package global — and counts domains nobody is holding any
	// more. That is a test failing for another test's timing, and it is what
	// happened: green on every Linux runner, red on macos-15-intel under -race
	// (TestADomainIsForgottenOnceNobodyHoldsIt, 2026-08-21), where the
	// scheduler is slow enough for the gap to open.
	//
	// The general shape is the one this repository keeps meeting: an assertion
	// about shared state is only as good as the guarantee that every other
	// actor has finished. Deleting the four lines below makes the neighbour
	// flake rather than this test, which is exactly why it is written here.
	var left sync.WaitGroup
	left.Add(domains)

	for i := range domains {
		go func() {
			free := Lock(string(rune('a' + i)))
			arrived.Done()
			<-release
			free()
			left.Done()
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
	left.Wait()
}

// A map that keeps one mutex per domain a session ever named is the memory
// sink an emulator must not become. The refcount is what stops it, and it is
// exactly the kind of bookkeeping that reads as correct and leaks.
func TestADomainIsForgottenOnceNobodyHoldsIt(t *testing.T) {
	// Measured as a difference, not as an absolute, and that is the whole
	// correctness of this test rather than a style choice.
	//
	// locks.held is a package global. Reading it raw asserts something about
	// every other test in this package, so a neighbour still on its way out of
	// Lock fails *this* one — which is what happened on macos-15-intel under
	// -race on 2026-08-21, where the scheduler is slow enough for the gap to
	// open, while every Linux runner stayed green. A test that fails for
	// another test's timing reports the wrong subject, and the reflex it
	// teaches is to re-run until green.
	//
	// A difference is immune to that: whatever anyone else holds, this test's
	// own hundred domains must leave nothing behind. The mutation that reddens
	// it is the real leak — dropping the delete from release — and that one is
	// reproducible here.
	count := func() int {
		locks.mu.Lock()
		defer locks.mu.Unlock()
		return len(locks.held)
	}

	before := count()
	for i := range 100 {
		release := Lock("forgotten-" + string(rune('a'+i%26)))
		release()
	}
	if leaked := count() - before; leaked != 0 {
		t.Fatalf("%d domains of this test's own hundred are still held after every caller left", leaked)
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
