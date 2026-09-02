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
	pass := func() {
		mu.Lock()
		snapshot := append([]string(nil), state...)
		seen = append(seen, snapshot)
		mu.Unlock()
		select {
		case <-firstStarted:
		default:
			close(firstStarted)
			<-release // hold the first pass open so the second caller arrives inside it
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Run(pass)
	}()
	<-firstStarted

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
	<-secondDone
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	last := seen[len(seen)-1]
	if len(last) != 1 || last[0] != "late-change" {
		t.Fatalf("the pass that satisfied the second caller did not read its change: %v", seen)
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
	wg.Wait()

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
	<-started

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
	wg.Wait()
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
	<-started

	// One caller arrives while pass 1 runs: it will be covered by pass 2.
	intime := make(chan struct{})
	go func() { c.Run(pass); close(intime) }()
	for c.Waiting() < 1 {
		time.Sleep(time.Millisecond)
	}

	close(first) // pass 1 ends, pass 2 begins and is held
	<-started

	// The straggler arrives now, with pass 2 already running. Nothing pass 2
	// does can cover it, so it must run a third.
	late := make(chan struct{})
	go func() { c.Run(pass); close(late) }()
	for c.Waiting() < 1 {
		time.Sleep(time.Millisecond)
	}

	close(second) // pass 2 ends, pass 3 begins for the straggler
	<-started
	<-intime
	<-late

	if got := passes.Load(); got != 3 {
		t.Fatalf("a burst with one straggler cost %d passes, want 3: two for the burst and one the late caller cannot share", got)
	}
}
