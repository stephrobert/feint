package store_test

import (
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// pendingServer is a resource mid-transition: settled at running, with two
// states to walk before it gets there.
func pendingServer() *resource.Resource {
	r := resource.New("id-1", "server", resource.Tenant{Provider: "p"}, "running", time.Unix(1700000000, 0).UTC())
	r.Pending = []string{"stopping", "starting", "running"}
	return r
}

// With the mode off — the default — a pending chain is inert, and that is what
// keeps every existing test and the conformance suite byte-identical (#124).
//
// The accepting half of the guard: a mechanism that advanced for everyone would
// pass every test about transitions and change what every existing client sees.
func TestAPendingChainIsIgnoredUntilTheOperatorAsksForIt(t *testing.T) {
	s := store.New()
	s.Put(pendingServer())

	for i := 0; i < 3; i++ {
		got, ok := s.Get("p", "server", "id-1")
		if !ok {
			t.Fatal("the resource vanished")
		}
		if got.State != "running" {
			t.Fatalf("read %d answered %q with the mode off, and off must answer the settled state "+
				"from the first read to the last", i, got.State)
		}
	}

	// And on, the same store walks the chain.
	s.Eventual(true)
	want := []string{"stopping", "starting", "running"}
	for i, state := range want {
		got, _ := s.Get("p", "server", "id-1")
		if got.State != state {
			t.Errorf("read %d answered %q, want %q", i, got.State, state)
		}
	}
}

// The observation advances the STORED resource, not the copy handed back.
//
// The defect this holds is silent and total: advancing the clone answers the
// first state of the chain on every read for ever, so a waiter never sees the
// machine settle and a suite hangs until its own timeout. It reads as working —
// the state did change — which is what makes it worth its own test.
func TestAnObservationAdvancesTheStoredResourceAndNotTheClone(t *testing.T) {
	s := store.New()
	s.Eventual(true)
	s.Put(pendingServer())

	first, _ := s.Get("p", "server", "id-1")
	second, _ := s.Get("p", "server", "id-1")
	if first.State == second.State {
		t.Fatalf("two reads both answered %q: the chain advanced on the clone, so it never advances "+
			"at all and a client waits for a state that will not come", first.State)
	}
	if first.State != "stopping" || second.State != "starting" {
		t.Errorf("the two reads answered %q then %q, want stopping then starting", first.State, second.State)
	}

	// And the caller's own copy is its own: consuming from it must not consume
	// from the store.
	third, _ := s.Get("p", "server", "id-1")
	third.Pending = nil
	fourth, _ := s.Get("p", "server", "id-1")
	if fourth.State != "running" {
		t.Errorf("after the caller emptied its own copy the store answered %q, want running: the "+
			"clone shares the chain rather than copying it", fourth.State)
	}
}

// Peek reads without counting as an observation, which is what lets a lifecycle
// action hold a resource without consuming the chain it is about to push (#637).
//
// Binding.Observe reads to ACT. With Get there, the action consumed its own
// first state and the client's first read answered the second one — a reboot
// that walked `starting, running` where fr-par answers
// `stopping, starting, running`.
func TestPeekDoesNotAdvanceAChain(t *testing.T) {
	s := store.New()
	s.Eventual(true)
	s.Put(pendingServer())

	for i := 0; i < 3; i++ {
		got, ok := s.Peek("p", "server", "id-1")
		if !ok {
			t.Fatal("the resource vanished")
		}
		if got.State != "running" {
			t.Fatalf("peek %d answered %q: a read that acts is not an observation and must leave "+
				"the chain where it found it", i, got.State)
		}
	}

	// The chain is still whole, so the first real observation answers its first
	// state — the property that made the reboot start one step in.
	got, _ := s.Get("p", "server", "id-1")
	if got.State != "stopping" {
		t.Errorf("the first observation after three peeks answered %q, want stopping", got.State)
	}
}
