package emulator

import (
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/trace"
)

// A subscriber that stops reading is skipped, never waited for.
//
// publish runs inside the handler of a request a real client is waiting on. A
// browser tab left open on a page nobody is looking at must not be able to hold
// a terraform apply — which is the lesson store.Snapshot already learned here,
// when a reader that stopped consuming a response froze the whole emulator by
// holding its lock for as long as it took.
//
// Internal rather than driven over HTTP, and deliberately: an external test that
// opened the stream and never read it proved nothing, because net/http's own
// buffers drain the handler on the reader's behalf, so the mutation that makes
// publish block survived it. The property belongs to the channel, so the test
// goes where the channel is.
//
// The wait is bounded rather than left to the test binary's timeout: a test that
// hangs instead of failing blocks every test behind it and reads as a slow
// machine, which cost this repository twenty minutes of a falsification run.
func TestASlowSubscriberNeverBlocksAPublish(t *testing.T) {
	s := newStream()
	// Subscribed and never read: the channel fills, and every publish after
	// that has to return anyway.
	_, _, unsubscribe := s.subscribe()
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		for i := 0; i < ringSize*3; i++ {
			s.publishExchange(trace.Exchange{Method: "GET", Path: "/x", Status: 200, Mounted: true})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%d publishes did not finish with one idle subscriber: "+
			"a browser tab can hold every request the emulator answers", ringSize*3)
	}

	// The accepting half: the ring still holds what it should, so "skip the
	// subscriber" did not become "drop the entry".
	if held := len(s.recent()); held != ringSize {
		t.Errorf("the ring holds %d entries after the run, want %d", held, ringSize)
	}
}
