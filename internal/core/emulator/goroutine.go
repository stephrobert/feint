package emulator

import (
	"runtime"
)

// Who touched the store: the identity of a goroutine, and why this file exists.
//
// The `behaviour` axis credits an operation when the store saw its handler
// touch a resource whose whole lifecycle a span observed. That needs an answer
// to "which request caused this touch", and the store gives one for free:
// `store.Observe` documents that the callback "runs outside the store's lock,
// **on the goroutine that made the touch**", synchronously, before the touching
// call returns. So the goroutine handling a request *is* the causal link
// between that request and every touch it makes. Nothing is guessed.
//
// The axis used to approximate that link instead of reading it: a touch was
// attributed only while exactly one non-probe request was in flight anywhere in
// the process (soleClientFlightLocked, removed by #398). The approximation is
// right when nothing overlaps and silent when something does, which made the
// number a function of the scheduler — 313 and 314 on two identical runs,
// because terraform runs at `-parallelism=10` under a span that brackets its
// whole lifecycle.
//
// Go publishes no goroutine identity, so this reads the one place the runtime
// prints it: the header line of `runtime.Stack`, "goroutine 42 [running]:".
// That is a known cost, not a free one — `runtime.Stack` walks the caller's
// stack, and an HTTP handler's stack is deep enough for it to measure 12 µs at
// depth 30 and 22 µs at depth 60 on the machine this was written on. Which is
// why it is not paid on every request: [observer.beginFlight] asks for an
// identity only while a span is open, and observer.spansOpen is the check that
// costs nothing when none is: one atomic load per request, no lock.
//
// What that gating means for the record, stated because it is the residual
// indeterminacy rather than a detail: a request already in flight when a span
// opens carries no identity, so its touches cannot be attributed. Every suite
// here opens its span from the shell before launching a client, so no client
// request is ever in flight at that moment — and when one is, the touch is
// counted and published by the close of the span, which answers how many
// touches it could not attribute — see assertSpan.close and prove.sh — instead
// of silently vanishing.
//
// TestConcurrentClientsKeepTheirAttribution fails without this file: it drives
// two overlapping lifecycles through one span and requires both operations to
// be marked, which the in-flight approximation could never do.

// goroutineID answers the identity of the calling goroutine, or 0 when the
// runtime printed something this cannot parse.
//
// Zero is not an identity: [observer.attributedOperationLocked] refuses to
// match on it, so a parse that fails leaves a touch unattributed rather than
// attributing it to the first flight that also failed to parse.
func goroutineID() uint64 {
	// Long enough for "goroutine <id> [", which is all that is read. The rest
	// of the traceback is written into this buffer and discarded.
	var buf [32]byte
	n := runtime.Stack(buf[:], false)
	b := buf[:n]
	const prefix = "goroutine "
	if len(b) < len(prefix)+1 || string(b[:len(prefix)]) != prefix {
		return 0
	}
	var id uint64
	digits := 0
	for _, c := range b[len(prefix):] {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
		digits++
	}
	if digits == 0 {
		return 0
	}
	return id
}
