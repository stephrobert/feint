package machine

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConcurrentOVNCreatesShareTheirUplinkWrites holds the batching half of
// #473. Every write to the uplink's ipv4.routes makes the daemon clear and
// rebuild its firewall — ~1 s measured on Incus 7.2 — and the writes are
// serialised under uplinkMu on purpose (#341). One write per subnet create
// therefore queued fifteen rebuilds behind each other on a fifteen-subnet
// apply; delegating the queue in one write is what removes the queue without
// touching the serialisation.
//
// The burst is staged rather than bet on: the first uplink write sleeps long
// enough that every other create has queued its block before the write
// returns, so the fixed driver needs at most two writes. The property the
// count refuses is exact in the other direction: without draining, eight
// distinct blocks cost eight writes, whatever the timing.
func TestConcurrentOVNCreatesShareTheirUplinkWrites(t *testing.T) {
	self := strconv.Itoa(os.Getpid())

	var mu sync.Mutex
	routes := ""
	writes := 0
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "query /1.0/networks/feint-uplink"):
			mu.Lock()
			out := ourUplinkJSON(self, routes)
			mu.Unlock()
			return []byte(out), nil
		case strings.HasPrefix(key, "query /1.0/networks?recursion=1"):
			return []byte("[]"), nil
		case strings.HasPrefix(key, "query /1.0/networks/"):
			return nil, errors.New("Network not found")
		case key == "network get feint-uplink ipv4.routes":
			mu.Lock()
			out := routes + "\n"
			mu.Unlock()
			return []byte(out), nil
		case strings.HasPrefix(key, "network set feint-uplink ipv4.routes="):
			mu.Lock()
			routes = strings.TrimPrefix(key, "network set feint-uplink ipv4.routes=")
			writes++
			mu.Unlock()
			// The rebuild the write costs, long enough for the whole burst to
			// have queued behind uplinkMu before this returns.
			time.Sleep(50 * time.Millisecond)
			return nil, nil
		}
		return nil, nil
	}

	d := NewIncus()
	d.runner = runner
	d.OVN = true

	const creates = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < creates; i++ {
		spec := NetworkSpec{
			Name: "fnt-batch" + strconv.Itoa(i),
			CIDR: "10.71." + strconv.Itoa(i+1) + ".0/24",
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := d.EnsureNetwork(context.Background(), spec); err != nil {
				t.Errorf("create %s: %v", spec.Name, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < creates; i++ {
		block := "10.71." + strconv.Itoa(i+1) + ".0/24"
		if !routeListContains(routes, block) {
			t.Errorf("%s was never delegated; the uplink carries %q", block, routes)
		}
	}
	if writes >= creates {
		t.Fatalf("%d creates cost %d uplink writes: nothing was batched, and each write is a serial firewall rebuild (#473)", creates, writes)
	}
}
