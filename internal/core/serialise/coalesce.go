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
			c.cond.Wait()
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
