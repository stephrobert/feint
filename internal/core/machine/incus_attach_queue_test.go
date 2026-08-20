package machine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTwoMachinesAttachWithoutQueueing holds the bound that #348 was measured
// against: an attachment waits for its own machine, never for somebody else's.
//
// The lock this asserts used to be one package-level mutex, held across
// waitForAgent — so every attachment in the session queued behind whichever
// machine answered slowest. Measured on the Scaleway example stack under
// --vm incus-ovn, six attachments took 26s, 31s, 42s, 52s, 63s and 73s: a flat
// +10s each, which is one machine's wait paid again by every machine after it.
// Past the client's minute the apply retries, and the retry meets the NIC its
// own first attempt created — "the server is already attached to this private
// network".
//
// The test drives two attachments on two *different* machines, each of whose
// agent takes a beat to answer. Serialised, the pair costs two beats; per
// machine, it costs one. The assertion is on the total, because that is the
// property a stack feels: a slow machine must not tax its neighbours.
func TestTwoMachinesAttachWithoutQueueing(t *testing.T) {
	const beat = 300 * time.Millisecond

	d := &Incus{
		agentPoll: 5 * time.Millisecond,
		runner: func(_ context.Context, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			// Adding the device: one of the several calls Attach makes into the
			// machine while holding the lock. Under --vm incus-ovn the real ones
			// are the guest address configuration and the private-route install,
			// and together they are the ten seconds each attachment costs.
			case strings.Contains(joined, "config device add"):
				time.Sleep(beat)
				return []byte("ok"), nil
			case strings.Contains(joined, "exec"):
				return []byte("ok"), nil
			// The device list, read before a free name is chosen.
			case strings.Contains(joined, "config device") && strings.Contains(joined, "show"):
				return []byte("{}"), nil
			case strings.Contains(joined, "list"):
				return []byte(`[{"name":"m","status":"Running","type":"virtual-machine"}]`), nil
			}
			return []byte("{}"), nil
		},
	}

	var wg sync.WaitGroup
	start := time.Now()
	for _, name := range []string{"feint-scw-one", "feint-scw-two"} {
		wg.Add(1)
		go func(machine string) {
			defer wg.Done()
			// The error is not the subject: a stub runtime cannot complete an
			// attachment. What is asserted is how long the pair took.
			_ = d.Attach(context.Background(), machine, Attachment{Network: "fnt-net"})
		}(name)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// One beat plus slack. Two beats means the two machines queued.
	if limit := beat * 3 / 2; elapsed > limit {
		t.Fatalf("two attachments on two machines took %v, over %v: they queued behind one lock, "+
			"which is #348 — one machine's wait must not be paid by another", elapsed, limit)
	}
}
