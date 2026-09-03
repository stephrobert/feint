package store_test

import (
	"bytes"
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

// A chain marked as an action's only signal is walked with the mode off (#654).
//
// The distinction the store reads is a property of the chain, not of a
// provider: PendingOnlySignal says "this action settles where it started, so
// nothing else tells the client it happened". A reboot is the case that exists
// today, and this core never learns the word.
//
// Both halves are asserted, because a store that walked everything would pass
// the first one and undo #124: the ordinary chain beside it must stay inert.
func TestAChainThatIsAnActionsOnlySignalIsWalkedInAnyMode(t *testing.T) {
	s := store.New()

	only := pendingServer()
	only.ID = "only-signal"
	only.PendingOnlySignal = true
	s.Put(only)
	s.Put(pendingServer()) // id-1, an ordinary chain

	for i, want := range []string{"stopping", "starting", "running"} {
		got, ok := s.Get("p", "server", "only-signal")
		if !ok {
			t.Fatal("the resource went missing")
		}
		if got.State != want {
			t.Fatalf("read %d answers %q, want %q: a chain nothing else signals must walk in any mode", i, got.State, want)
		}
		if plain, _ := s.Get("p", "server", "id-1"); plain.State != "running" {
			t.Fatalf("read %d moved the ordinary chain to %q: the mode still governs those", i, plain.State)
		}
	}
	// Settled, and it stays settled rather than walking for ever.
	if got, _ := s.Get("p", "server", "only-signal"); got.State != "running" {
		t.Errorf("the chain settled at %q, want running", got.State)
	}
	// And the mark is spent with the chain: what is left is an ordinary
	// resource, not one the store keeps taking the write lock for.
	if got, _ := s.Get("p", "server", "only-signal"); got.PendingOnlySignal {
		t.Error("a spent chain still carries its mark")
	}
}

// The mark survives a snapshot, for the reason the chain does (#654).
//
// A reboot in flight when the state is saved is a reboot still in flight when
// it is loaded. Restoring the states without the mark would leave a resource
// with a chain no mode ever walks: it would answer the first state of the chain
// for ever, which is worse than either behaviour this flag chooses between.
func TestTheOnlySignalMarkSurvivesASnapshot(t *testing.T) {
	s := store.New()
	r := pendingServer()
	r.PendingOnlySignal = true
	s.Put(r)

	var saved bytes.Buffer
	if err := s.Snapshot(&saved); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	loaded := store.New()
	if err := loaded.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, ok := loaded.Get("p", "server", "id-1"); !ok || got.State != "stopping" {
		t.Errorf("a restored chain answers %v, want stopping: the reboot was still in flight", got)
	}
}
