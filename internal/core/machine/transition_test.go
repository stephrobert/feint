package machine

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

func transitionFixture() (*store.Store, Binding, *resource.Resource) {
	st := store.New()
	b := Binding{Provider: "example"}
	res := &resource.Resource{
		ID:     "srv-1",
		Kind:   "server",
		Tenant: resource.Tenant{Provider: "example"},
		State:  "stopped",
		Attrs:  map[string]any{},
	}
	st.Put(res)
	return st, b, res
}

// The change of one transition runs to completion before the next one reads.
//
// This is the property the two pack copies existed for: the runtime call runs
// outside the store lock, so nothing else orders two writers on one target.
// Without the hold, two concurrent poweron each called the runtime and the
// loser overwrote the winner's state.
func TestTransitionExcludesASecondActionOnTheTarget(t *testing.T) {
	st, b, _ := transitionFixture()

	var inside atomic.Int32
	var overlap atomic.Bool
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := b.Transition(st, time.Now, "server", "srv-1", func(res *resource.Resource) {
				if inside.Add(1) > 1 {
					overlap.Store(true)
				}
				// Long enough that an unserialised caller is inside at the
				// same time rather than merely likely to be.
				time.Sleep(2 * time.Millisecond)
				inside.Add(-1)
				res.State = "running"
			})
			if err != nil {
				t.Errorf("transition: %v", err)
			}
		}()
	}
	wg.Wait()

	if overlap.Load() {
		t.Fatal("two changes ran on one target at the same time")
	}
}

// A delete landing while the change runs wins: the write-back is conditional,
// and the resource stays deleted rather than coming back with the change's
// state. The change deleting its own target is exactly what a concurrent
// DELETE does from the store's point of view, since the change runs outside
// the store lock.
func TestADeleteLandingMidTransitionWins(t *testing.T) {
	st, b, _ := transitionFixture()

	err := b.Transition(st, time.Now, "server", "srv-1", func(res *resource.Resource) {
		st.Delete("example", "server", "srv-1")
		res.State = "running"
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a transition over a deleted resource reported %v, want store.ErrNotFound", err)
	}
	if _, back := st.Get("example", "server", "srv-1"); back {
		t.Fatal("the deleted resource came back: the write-back is not conditional")
	}
}

// A target that never existed is reported before the change runs: the change
// is the runtime work, and powering on a machine for a resource nobody stores
// would leave a container no API describes.
func TestTransitionRefusesAMissingTargetBeforeTheChange(t *testing.T) {
	st, b, _ := transitionFixture()

	ran := false
	err := b.Transition(st, time.Now, "server", "srv-2", func(*resource.Resource) { ran = true })
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a transition on a missing target reported %v, want store.ErrNotFound", err)
	}
	if ran {
		t.Fatal("the change ran for a resource the store does not hold")
	}
}
