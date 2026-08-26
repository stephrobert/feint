package outscale

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// passIsolator is a driver whose first isolation call can be held open, so a
// test can put a whole burst of callers *inside* the first reconciliation pass
// instead of hoping to land there. It records which networks each
// IsolateNetwork call named, which is how the test reads both what a pass saw
// and how many passes ran.
type passIsolator struct {
	machine.Noop
	mu    sync.Mutex
	calls []string

	firstStarted chan struct{}
	release      chan struct{}
	firstOnce    sync.Once
}

func newPassIsolator() *passIsolator {
	return &passIsolator{
		firstStarted: make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (f *passIsolator) IsolateNetwork(_ context.Context, network string, _ []string) error {
	f.mu.Lock()
	f.calls = append(f.calls, network)
	f.mu.Unlock()
	f.firstOnce.Do(func() {
		close(f.firstStarted)
		<-f.release // hold the first pass open so the burst arrives inside it
	})
	return nil
}

func (f *passIsolator) networksSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// The isolation passes of a burst of subnet creates are shared: a burst that
// arrives while a pass is already reading is served by that pass plus one
// more, never by one pass per caller — one pass per caller is the O(N) per
// request that #473's straight line is made of. And sharing stays sound
// because the pass re-reads the store when it runs: the covering pass below
// names subnets stored after the first pass began.
//
// The collapse arithmetic of the coalescer is held by the serialise tests;
// what only this level can assert is the wiring — that isolateNetworks routes
// through the coalescer at all, and computes its members inside the coalesced
// pass rather than before Run.
func TestConcurrentSubnetCreatesShareTheirIsolationPasses(t *testing.T) {
	env := emulator.DefaultEnv()
	driver := newPassIsolator()
	env.Machines = driver
	p := New(env)

	subnet := func(id, network, block string) *resource.Resource {
		res := resource.New(id, kindSubnet, resource.Tenant{Provider: Name}, "available", time.Now())
		res.Attrs = map[string]any{"NetId": "vpc-1", "IpRange": block}
		res.Runtime = map[string]string{runtimeNetworkKey: network}
		return res
	}

	// First change, first pass: it starts, and stays open on the driver.
	env.Store.Put(subnet("subnet-0", "fnt-first", "10.0.1.0/24"))
	firstDone := make(chan struct{})
	go func() {
		p.isolateNetworks(context.Background())
		close(firstDone)
	}()
	<-driver.firstStarted

	// The burst lands while the first pass is reading: five more subnets, five
	// more callers. None may be satisfied by the pass in flight.
	const burst = 5
	var wg sync.WaitGroup
	burstDone := make(chan struct{})
	for i := 0; i < burst; i++ {
		env.Store.Put(subnet("subnet-"+string(rune('1'+i)), "fnt-late"+string(rune('1'+i)), "10.0."+string(rune('2'+i))+".0/24"))
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.isolateNetworks(context.Background())
		}()
	}
	go func() { wg.Wait(); close(burstDone) }()

	// Long enough for every burst caller to have reached the coalescer while
	// the first pass is still held open — which is what makes the pass count
	// below a staged fact rather than a bet.
	select {
	case <-burstDone:
		t.Fatal("a burst caller returned while the only pass in flight predates its subnet")
	case <-time.After(100 * time.Millisecond):
	}

	close(driver.release)
	<-firstDone
	<-burstDone

	seen := driver.networksSeen()
	passes := 0
	coveredLast := false
	for _, network := range seen {
		if network == "fnt-first" {
			passes++ // fnt-first is in the store throughout, so every pass names it once
		}
		if network == "fnt-late5" {
			coveredLast = true
		}
	}
	if !coveredLast {
		t.Fatalf("no pass named the last subnet of the burst; the members were computed before the pass ran: %v", seen)
	}
	if passes > 2 {
		t.Fatalf("a burst of %d callers inside one pass cost %d passes; sharing means the pass in flight plus one, and one per caller is #473's slope (calls: %v)",
			burst, passes, seen)
	}
}
