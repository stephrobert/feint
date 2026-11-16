package serialise

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A caller whose change lands while a pass is already reading must not be
// satisfied by that pass: it returns only once a pass that began after its
// call has completed. This is the guarantee that separates the coalescer from
// a debounce, and the test stages it rather than betting on timing: the first
// pass is held open on a channel while the second caller arrives.
func TestRunWaitsForAPassThatSawTheCall(t *testing.T) {
	var c Coalescer

	// The state a pass reads, as a pack's pass reads the store.
	var mu sync.Mutex
	state := []string{}
	var seen [][]string

	release := make(chan struct{})
	firstStarted := make(chan struct{})
	// Once rather than select/default: a mutant that ran two passes at once
	// could reach close twice and panic the whole test binary.
	var first sync.Once
	pass := func() {
		mu.Lock()
		snapshot := append([]string(nil), state...)
		seen = append(seen, snapshot)
		mu.Unlock()
		isFirst := false
		first.Do(func() {
			isFirst = true
			close(firstStarted)
		})
		if isFirst {
			<-release // hold the first pass open so the second caller arrives inside it
		}
	}

	firstDone := make(chan struct{})
	go func() {
		c.Run(pass)
		close(firstDone)
	}()
	// Bounded, and watching the caller as well as the pass: a Run that returns
	// on an idle coalescer without running anything never closes firstStarted,
	// and a bare receive here hung the test until go test's 10-minute alarm
	// (#970) instead of saying so.
	select {
	case <-firstStarted:
	case <-firstDone:
		select {
		case <-firstStarted:
		default:
			t.Fatal("the first caller returned from an idle coalescer without running a pass")
		}
	case <-time.After(testWait):
		t.Fatalf("waited %v and the first caller's pass never started", testWait)
	}

	// The second caller writes its change, then asks. The running pass has
	// already taken its snapshot and cannot have seen it.
	mu.Lock()
	state = append(state, "late-change")
	mu.Unlock()

	secondDone := make(chan struct{})
	go func() {
		c.Run(pass)
		close(secondDone)
	}()

	select {
	case <-secondDone:
		t.Fatal("the second caller returned while the only completed pass predates its change")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	awaitClosed(t, secondDone, "the second caller never returned once the held pass was released")
	awaitClosed(t, firstDone, "the first caller never returned once its pass was released")

	mu.Lock()
	defer mu.Unlock()
	last := seen[len(seen)-1]
	if len(last) != 1 || last[0] != "late-change" {
		t.Fatalf("the pass that satisfied the second caller did not read its change: %v", seen)
	}
}

// testWait bounds every wait of this file on something the coalescer must make
// happen. It is generous because a pass here takes microseconds: reaching it
// means the event is not coming, and the test says which one instead of
// hanging until go test's alarm.
const testWait = 10 * time.Second

// awaitClosed waits for ch to close or to carry a value, at most testWait, and
// fails naming what did not happen. It is a minimal copy of the shape of awaitEvent
// (internal/core/machine, await_test.go), kept here because a test helper is
// not importable across packages and this package needs only the bare wait.
func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	timer := time.NewTimer(testWait)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		t.Fatalf("waited %v and %s", testWait, what)
	}
}

// awaitGroup is wg.Wait() bounded by awaitClosed (#963). The goroutines a test
// counts on wg run the coalescer, so a bare wg.Wait() is a wait on the subject,
// and a pass that never wakes its waiters holds it until go test's alarm.
func awaitGroup(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	awaitClosed(t, done, what)
}

// awaitParked polls Waiting until at least n callers are parked, at most
// testWait, and fails naming what did not happen (#963). Polled, because
// parking is the one event the coalescer does not signal.
func awaitParked(t *testing.T, c *Coalescer, n int, what string) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for c.Waiting() < n {
		if time.Now().After(deadline) {
			t.Fatalf("waited %v and %s: Waiting is %d, want %d", testWait, what, c.Waiting(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// A burst of callers is served by a bounded number of passes, not one each.
// Every goroutine is released at once and the pass is slow enough that the
// whole burst arrives while the first pass runs; the fixed code then needs at
// most the running pass plus one more, and the unfixed shape — one pass per
// caller — is what the count refuses.
func TestABurstOfCallersSharesItsPasses(t *testing.T) {
	var c Coalescer
	var passes atomic.Int64
	pass := func() {
		passes.Add(1)
		time.Sleep(20 * time.Millisecond)
	}

	const callers = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c.Run(pass)
		}()
	}
	close(start)
	awaitGroup(t, &wg, "the burst of callers never all returned, so a pass never woke its waiters")

	if got := passes.Load(); got >= callers {
		t.Fatalf("%d callers ran %d passes: nothing was coalesced", callers, got)
	}
	if got := passes.Load(); got > 4 {
		t.Fatalf("%d callers needed %d passes; a burst arriving inside one pass needs at most that pass plus one", callers, got)
	}
}

// An idle coalescer runs the pass immediately and synchronously.
func TestAnIdleCoalescerRunsAtOnce(t *testing.T) {
	var c Coalescer
	ran := false
	c.Run(func() { ran = true })
	if !ran {
		t.Fatal("the pass did not run")
	}
}

// Waiting reports the callers parked on the wait, and the two facts a burst test
// needs from it: it reaches the number that arrived, and it is back to zero once
// they are covered.
//
// It exists because a test cannot otherwise tell "the burst has arrived" from
// "the burst is still walking towards the wait", and outscale's burst test slept
// 100 ms instead — a bet that macos-15-intel called on 2026-09-02.
func TestWaitingCountsTheCallersParkedOnAPass(t *testing.T) {
	var c Coalescer
	held := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once

	go c.Run(func() {
		once.Do(func() { close(started) })
		<-held
	})
	awaitClosed(t, started, "the first pass never began on an idle coalescer")

	const burst = 5
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Run(func() {}) }()
	}
	deadline := time.Now().Add(30 * time.Second)
	for c.Waiting() < burst {
		if time.Now().After(deadline) {
			t.Fatalf("Waiting reached %d of %d callers", c.Waiting(), burst)
		}
		time.Sleep(time.Millisecond)
	}

	close(held)
	awaitGroup(t, &wg, "the parked callers never all returned once the held pass ended")
	if got := c.Waiting(); got != 0 {
		t.Errorf("Waiting is %d once every caller returned, want 0", got)
	}
}

// A caller that arrives AFTER the covering pass has begun needs one of its own,
// and that is the coalescer being correct rather than wasteful: the pass in
// flight started before this caller's change, so nothing it does can cover it.
//
// This is the fact that made outscale's burst test flaky, and it is worth a test
// of its own because the flake read as a defect in the coalescer. A burst of
// five that all arrive in time costs two passes; one straggler makes it three,
// legitimately. The old test slept 100 ms and then asserted "at most two", so on
// a slow enough runner it was asserting that no straggler existed — a race, not
// a property. Staged here instead of raced.
func TestALateCallerNeedsAPassOfItsOwn(t *testing.T) {
	var c Coalescer
	var passes atomic.Int64
	first := make(chan struct{})
	second := make(chan struct{})
	started := make(chan struct{}, 8)

	pass := func() {
		n := passes.Add(1)
		started <- struct{}{}
		switch n {
		case 1:
			<-first
		case 2:
			<-second
		}
	}

	go c.Run(pass) // pass 1, held open
	awaitClosed(t, started, "pass 1 never began on an idle coalescer")

	// One caller arrives while pass 1 runs: it will be covered by pass 2.
	intime := make(chan struct{})
	go func() { c.Run(pass); close(intime) }()
	awaitParked(t, &c, 1, "the in-time caller never parked on pass 1")

	close(first) // pass 1 ends, pass 2 begins and is held
	awaitClosed(t, started, "pass 2 never began once pass 1 ended, so the parked caller was never woken")

	// The straggler arrives now, with pass 2 already running. Nothing pass 2
	// does can cover it, so it must run a third.
	late := make(chan struct{})
	go func() { c.Run(pass); close(late) }()
	awaitParked(t, &c, 1, "the straggler never parked on pass 2")

	close(second) // pass 2 ends, pass 3 begins for the straggler
	awaitClosed(t, started, "pass 3 never began for the straggler once pass 2 ended")
	awaitClosed(t, intime, "the in-time caller never returned once pass 2 ended")
	awaitClosed(t, late, "the straggler never returned once pass 3 ended")

	if got := passes.Load(); got != 3 {
		t.Fatalf("a burst with one straggler cost %d passes, want 3: two for the burst and one the late caller cannot share", got)
	}
}
