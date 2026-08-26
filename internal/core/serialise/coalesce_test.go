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
