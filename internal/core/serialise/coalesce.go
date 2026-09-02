package serialise

import "sync"

// Coalescer collapses concurrent demands for the same full-set pass into as
// few executions as the demands allow, while keeping each caller's guarantee:
// Run returns only once a pass that *began after the call* has completed.
//
// It exists for the reconciliations that are written against the whole current
// state rather than against one change — an isolation pass reads every subnet
// of the store and applies what each may reach. Run one such pass per change
// under load and the work is O(N) per request, serialised on the runtime's own
// locks: fifteen concurrent subnet creates each paid a full pass over all
// fifteen networks, which is the straight line of #473 (a flat ~6 s per
// create, cut at the emulator's write deadline from the tenth on). Any pass
// that starts after a change already covers it, so a burst of fifteen needs at
// most the pass in flight plus one more — never fifteen.
//
// The guarantee is the reason this is not a plain "skip if running" debounce:
// a caller whose change landed while a pass was already reading would return
// with its change possibly unseen, and the pack would answer the client before
// the state it just wrote was reconciled. TestRunWaitsForAPassThatSawTheCall
// fails against that shortcut.
//
// The zero value is ready to use. Like Lock, the domain of one Coalescer is
// whatever its owner says it is — one per pack's reconciliation, never one for
// the whole emulator.
type Coalescer struct {
	mu      sync.Mutex
	cond    *sync.Cond
	running bool
	// begun and done number the passes: begun is bumped when a pass starts,
	// done records the highest finished one. A caller is satisfied by any pass
	// numbered strictly above the value begun had when it arrived.
	begun uint64
	done  uint64
	// waiting counts the callers parked on cond, and exists so a test can wait
	// for a burst to have ARRIVED rather than sleep and hope. See Waiting.
	waiting int
}

// Waiting is how many callers are parked waiting for a pass to cover them.
//
// It is an observation point for tests, and it is here rather than in a test
// helper because the fact it reports is not observable from outside: a caller
// that has entered Run has not necessarily reached the wait, and the difference
// is exactly what a burst test depends on.
//
// The defect it was added for, on 2026-09-02: outscale's
// TestConcurrentSubnetCreatesShareTheirIsolationPasses staged a burst of five
// callers and then slept 100 ms, under a comment calling the pass count "a
// staged fact rather than a bet". It was a bet, and macos-15-intel called it —
// three passes instead of two, on the same tree that had passed on every other
// runner minutes earlier. A caller still walking towards the wait when the held
// pass is released starts a pass of its own, and the count is one higher.
//
// A number that moves the moment it is read, so it is only sound in the shape
// the test uses it: wait for it to REACH a value that cannot then drop without
// the test's own doing.
func (c *Coalescer) Waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiting
}

// Run executes pass, or waits for an execution that covers this call.
//
// The pass function must read the state it reconciles itself, at the moment it
// runs: a pass replays no argument from the callers it covers, which is what
// makes covering them sound.
func (c *Coalescer) Run(pass func()) {
	c.mu.Lock()
	if c.cond == nil {
		c.cond = sync.NewCond(&c.mu)
	}
	// Any pass that begins from now on reads state that includes this
	// caller's change; the one currently running (begun-th) may not.
	target := c.begun + 1
	for c.done < target {
		if c.running {
			c.waiting++
			c.cond.Wait()
			c.waiting--
			continue
		}
		c.running = true
		c.begun++
		mine := c.begun
		c.mu.Unlock()
		pass()
		c.mu.Lock()
		c.running = false
		if c.done < mine {
			c.done = mine
		}
		c.cond.Broadcast()
	}
	c.mu.Unlock()
}
