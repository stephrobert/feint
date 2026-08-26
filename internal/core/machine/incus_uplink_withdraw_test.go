package machine

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The withdrawal half of the uplink queue (#519), mirror of
// incus_uplink_batch_test.go. Fifteen concurrent subnet deletes were measured
// at a flat ~1.3 s each — one network delete plus one uplink write per
// network, all serialised under uplinkMu — the same straight line #473
// removed from the create side, at half the slope. The deletes stay
// serialised (#341's rule about concurrent uplink rebuilds); what these tests
// refuse is one firewall rebuild per network where the burst can share one.

// withdrawHarness is a runner answering what RemoveNetwork asks, with mutable
// uplink routes so a drain's effect is observable.
type withdrawHarness struct {
	mu     sync.Mutex
	routes string
	writes int
	// setErr fails the next uplink writes when non-nil.
	setErr error
}

func (h *withdrawHarness) run(_ context.Context, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(key, "network get fnt-") && strings.HasSuffix(key, "ipv4.address"):
		// fnt-wd<N> carries 10.72.<N>.1/24.
		name := strings.TrimPrefix(strings.Fields(key)[2], "fnt-wd")
		return []byte("10.72." + name + ".1/24\n"), nil
	case key == "network get feint-uplink ipv4.routes":
		h.mu.Lock()
		out := h.routes + "\n"
		h.mu.Unlock()
		return []byte(out), nil
	case strings.HasPrefix(key, "network set feint-uplink ipv4.routes="):
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.setErr != nil {
			return nil, h.setErr
		}
		h.routes = strings.TrimPrefix(key, "network set feint-uplink ipv4.routes=")
		h.writes++
		return nil, nil
	}
	return nil, nil
}

// TestQueuedWithdrawalsShareOneUplinkWrite holds the batching itself: seven
// deletes have landed their blocks in the queue while another held the lock,
// and the eighth's drain withdraws all eight in a single write. Without the
// drain, seven blocks stay delegated — each a real host route pointed at the
// uplink for a network that no longer exists, which is #341's leftover shape.
func TestQueuedWithdrawalsShareOneUplinkWrite(t *testing.T) {
	const deletes = 8
	blocks := make([]string, 0, deletes)
	for i := 1; i <= deletes; i++ {
		blocks = append(blocks, "10.72."+strconv.Itoa(i)+".0/24")
	}
	h := &withdrawHarness{routes: strings.Join(blocks, ",")}
	d := NewIncus()
	d.runner = h.run
	d.OVN = true

	// The seven deletes that finished while somebody else held uplinkMu: their
	// networks are gone, their withdrawal is queued, their turn never has to
	// write. Queued directly because the interleaving is the mutex's to pick;
	// what this test holds is the property whatever the order — a drain
	// carries the whole queue.
	for _, block := range blocks[:deletes-1] {
		d.queueUplinkWithdrawal(block)
	}

	if err := d.RemoveNetwork(context.Background(), "fnt-wd"+strconv.Itoa(deletes)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, block := range blocks {
		if routeListContains(h.routes, block) {
			t.Errorf("%s is still delegated after its network's delete; the uplink carries %q", block, h.routes)
		}
	}
	if h.writes != 1 {
		t.Fatalf("%d deletes cost %d uplink writes, want the one shared write: each write is a serial firewall rebuild (#519)", deletes, h.writes)
	}
}

// TestConcurrentOVNDeletesWithdrawEveryBlock is the same burst with the mutex
// picking the interleaving: whatever the order, every block is off the uplink
// when the last delete returns. The write count is the previous test's to
// hold — under a real scheduler it is only bounded, not fixed.
func TestConcurrentOVNDeletesWithdrawEveryBlock(t *testing.T) {
	const deletes = 8
	blocks := make([]string, 0, deletes)
	for i := 1; i <= deletes; i++ {
		blocks = append(blocks, "10.72."+strconv.Itoa(i)+".0/24")
	}
	h := &withdrawHarness{routes: strings.Join(blocks, ",")}
	d := NewIncus()
	d.runner = h.run
	d.OVN = true

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 1; i <= deletes; i++ {
		name := "fnt-wd" + strconv.Itoa(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := d.RemoveNetwork(context.Background(), name); err != nil {
				t.Errorf("remove %s: %v", name, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, block := range blocks {
		if routeListContains(h.routes, block) {
			t.Errorf("%s is still delegated after its network's delete; the uplink carries %q", block, h.routes)
		}
	}
}

// TestADelegationCancelsAQueuedWithdrawalOfItsBlock holds the recreate case
// the shared write opens: a subnet deleted and recreated on the same block
// between a neighbour's delete and its drain. The delegation must survive —
// a drain that withdrew the caller's block "no matter what" would strip a
// live network's route to honour a dead one.
func TestADelegationCancelsAQueuedWithdrawalOfItsBlock(t *testing.T) {
	h := &withdrawHarness{routes: "10.72.5.0/24,10.72.9.0/24"}
	d := NewIncus()
	d.runner = h.run
	d.OVN = true

	// A delete of the network carrying 10.72.5.0/24 succeeded and queued the
	// withdrawal…
	d.queueUplinkWithdrawal("10.72.5.0/24")
	// …then a create took the same block back before any drain ran.
	d.uplinkMu.Lock()
	err := d.delegateQueuedRoutes(context.Background(), "10.72.5.0/24")
	d.uplinkMu.Unlock()
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}

	// The next delete's drain must withdraw its own block and nothing else.
	if err := d.RemoveNetwork(context.Background(), "fnt-wd9"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if !routeListContains(h.routes, "10.72.5.0/24") {
		t.Fatalf("the recreated block was withdrawn by a dead network's queued drain; the uplink carries %q", h.routes)
	}
	if routeListContains(h.routes, "10.72.9.0/24") {
		t.Fatalf("the deleted network's own block stayed delegated; the uplink carries %q", h.routes)
	}
}

// TestAFailedWithdrawalKeepsItsBlocksQueued: a drain whose write fails must
// not lose the queue — the blocks go back, the holder that saw the failure
// reports it, and the next drain retries them.
func TestAFailedWithdrawalKeepsItsBlocksQueued(t *testing.T) {
	h := &withdrawHarness{routes: "10.72.3.0/24", setErr: errors.New("Failed rebuilding firewall")}
	d := NewIncus()
	d.runner = h.run
	d.OVN = true

	d.queueUplinkWithdrawal("10.72.3.0/24")
	d.uplinkMu.Lock()
	err := d.drainUplinkWithdrawals(context.Background())
	d.uplinkMu.Unlock()
	if err == nil {
		t.Fatal("a failed uplink write was reported as a withdrawal")
	}

	// The runtime recovers; the next drain must still know the block.
	h.mu.Lock()
	h.setErr = nil
	h.mu.Unlock()
	d.uplinkMu.Lock()
	err = d.drainUplinkWithdrawals(context.Background())
	d.uplinkMu.Unlock()
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if routeListContains(h.routes, "10.72.3.0/24") {
		t.Fatalf("the failed write's block was lost from the queue; the uplink carries %q", h.routes)
	}
}
