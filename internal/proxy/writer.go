package proxy

import (
	"encoding/json"
	"io"
	"sync"
)

// DefaultQueue is how many exchanges may wait to be written.
//
// One JSON Lines record is a few kilobytes and one write is a syscall, so the
// queue only ever fills when the disk stalls. Deep enough to ride out a stall,
// shallow enough that what it can cost in memory (a few megabytes) is bounded
// and stated rather than discovered.
const DefaultQueue = 1024

// Writer serialises redacted exchanges to JSON Lines, off the request path.
//
// Three properties, each of them a requirement of #72 rather than a preference:
//
//   - The client never waits for the disk. [Writer.Record] hands over and
//     returns; one goroutine does the encoding and the writing. A recorder that
//     doubled the latency of every call would change the thing it is measuring.
//   - A crash keeps what was already written. One value per line, one Write per
//     line, no buffering of our own: the file is complete up to its last newline
//     at every instant, which is what JSON Lines is for and what a large buffer
//     would take away.
//   - Full means dropped, and the file says so. The queue is bounded, so a slow
//     disk cannot grow the process without end; a drop still consumes a sequence
//     number, so the gap in Seq is the record of it. That is trace.Exchange.Seq
//     used for what its own documentation describes: "a reader that sees it jump
//     knows entries were dropped, which is the honest way to bound a buffer".
//
// The half that is not true, said here rather than left to be assumed: a gap is
// visible between two lines, so drops at the very end of a run — a disk that
// stalls and never recovers — leave a file that simply stops. Nothing inside it
// says how many are missing. That is what [Writer.Stats] is for and why the
// command prints it at exit; a reader holding only the file learns of those
// drops from nowhere. Writing a trailing marker would fix it and would put a
// second shape in a file whose whole value is that there is one.
//
// TestTheWriterDropsRatherThanBlocks and TestASlowDiskDoesNotHoldTheClient fail
// without the first and third.
type Writer struct {
	queue chan Redacted
	done  chan struct{}

	// Everything below is under mu, the writing goroutine's own counters
	// included. It costs one uncontended lock per record written, off the
	// request path, and it buys a Stats that is safe to call at any moment
	// instead of one whose comment says when — the shape of promise this
	// repository keeps finding to be untrue.
	mu      sync.Mutex
	seq     int64
	dropped int64
	written int64
	closed  bool
	err     error
}

// NewWriter starts the writing goroutine. The caller owns out and must close it
// after Close, never before: a transcript truncated by a file closed under the
// writer is the failure this whole type exists to avoid.
//
// A queue of zero or less takes DefaultQueue.
func NewWriter(out io.Writer, queue int) *Writer {
	if queue <= 0 {
		queue = DefaultQueue
	}
	w := &Writer{
		queue: make(chan Redacted, queue),
		done:  make(chan struct{}),
	}
	go w.run(out)
	return w
}

// run drains the queue until Close shuts it. Its lifetime is exactly the
// channel's: closed queue, drained, done — no context to cancel and no way for
// it to outlive its owner.
func (w *Writer) run(out io.Writer) {
	defer close(w.done)
	enc := json.NewEncoder(out)
	// A transcript is read by people and by jq. Escaping < and > into <
	// buys nothing outside a browser and makes every embedded URL unreadable.
	enc.SetEscapeHTML(false)
	for r := range w.queue {
		err := enc.Encode(r)
		w.mu.Lock()
		switch {
		case err != nil && w.err == nil:
			// Kept rather than reported per line: a full disk fails every
			// remaining record, and a proxy printing one line per request is a
			// proxy nobody can read the output of. Close says it once.
			w.err = err
		case err == nil:
			w.written++
		}
		w.mu.Unlock()
	}
}

// Record queues one exchange. It never blocks and never fails: whatever happens
// to the transcript, the client's request has already been answered and must not
// be held for it.
func (w *Writer) Record(r Redacted) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	// Numbered here rather than at the head of the exchange, so the file reads in
	// order and a gap means exactly one thing: a record that was dropped.
	// Numbered before the send, so a drop consumes its number.
	r.x.Seq = w.seq
	if w.closed {
		w.dropped++
		return
	}
	select {
	case w.queue <- r:
	default:
		w.dropped++
	}
}

// Close stops the writer and waits for what is queued to reach the file. It
// returns the first write error, if any. Calling it twice is safe.
func (w *Writer) Close() error {
	w.mu.Lock()
	alreadyClosed := w.closed
	w.closed = true
	if !alreadyClosed {
		close(w.queue)
	}
	w.mu.Unlock()

	<-w.done

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Stats reports how many exchanges reached the transcript and how many were
// dropped because the queue was full. Safe to call at any point.
func (w *Writer) Stats() (written, dropped int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written, w.dropped
}
