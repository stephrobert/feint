package machine

import (
	"errors"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// A read that writes is still a write, and all three packs have one: the address
// of a virtual machine arrives tens of seconds after the start, so every pack
// publishes it on the next GET. Observe is the transactional half of that, and
// these are the two properties a per-pack version kept getting wrong (#211).

// The copy handed to look() is read inside the hold, not before it.
//
// Outscale's readVms was the one pack that had noticed a read writes, and it
// still serialised around a copy List had produced earlier: the lock ordered the
// write-back and not the read it was built on, so the handler wrote back a
// resource it had seen before anybody else's change landed.
// The write it must not miss is the one that lands *while the second caller is
// waiting for the lock*, so the two are ordered with channels rather than left to
// the scheduler: a first Observe holds the target and changes it, a second is
// already blocked on the lock, and what the second one's look sees is the whole
// question. Reading before the lock and reading after it give different answers
// only in that window, which is why a version of this test that wrote first and
// called afterwards passed against both — and said so under falsification.
func TestObserveReadsInsideTheHold(t *testing.T) {
	st, b, _ := transitionFixture()

	holding := make(chan struct{})
	waiting := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		_, _ = b.Observe(st, time.Now, "server", "srv-1", func(res *resource.Resource) bool {
			// The hold is established; let the second caller pile up behind it.
			close(holding)
			<-waiting
			res.State = "running"
			return true
		})
	}()

	<-holding
	// The second caller starts now, so whatever copy it ends up with, it asked
	// for it before the first one had written anything.
	second := make(chan string, 1)
	go func() {
		_, _ = b.Observe(st, time.Now, "server", "srv-1", func(res *resource.Resource) bool {
			second <- res.State
			return false
		})
	}()
	// Nothing here can prove the second goroutine has reached the lock, so the
	// window is opened generously instead: this only has to be long enough that
	// a Get placed before the lock has run.
	time.Sleep(20 * time.Millisecond)
	close(waiting)
	<-done

	if seen := <-second; seen != "running" {
		t.Errorf("the second look saw %q; it was handed a copy read before the hold, so its "+
			"write-back would undo what the first one had just committed", seen)
	}
}

// Nothing is written when the look reports nothing new, and that is not a
// micro-optimisation: Updated is modification_date on the wire, so committing on
// every read would make a resource appear to change every time a client looked at
// it — and Terraform diffs on exactly that.
func TestObserveWritesNothingWhenNothingChanged(t *testing.T) {
	st, b, _ := transitionFixture()

	before, _ := st.Get("example", "server", "srv-1")
	if _, err := b.Observe(st, func() time.Time { return before.Updated.Add(time.Hour) },
		"server", "srv-1", func(res *resource.Resource) bool {
			// A look may touch the copy and still conclude nothing is new; what
			// decides is the verdict, not whether the copy was written to.
			res.Attrs["scratch"] = "value"
			return false
		}); err != nil {
		t.Fatalf("observe: %v", err)
	}

	after, _ := st.Get("example", "server", "srv-1")
	if !after.Updated.Equal(before.Updated) {
		t.Errorf("a read with nothing to report stamped Updated: %v became %v", before.Updated, after.Updated)
	}
	if _, wrote := after.Attrs["scratch"]; wrote {
		t.Error("a read with nothing to report wrote its scratch copy back to the store")
	}
}

// And the write-back stays conditional: a resource deleted while the runtime was
// answering must stay deleted, or a GET resurrects what a DELETE removed.
func TestObserveDoesNotResurrectADeletedTarget(t *testing.T) {
	st, b, _ := transitionFixture()

	_, err := b.Observe(st, time.Now, "server", "srv-1", func(res *resource.Resource) bool {
		// The delete lands while the runtime is answering. Same store, no hold:
		// this is DELETE arriving on another connection.
		st.Delete("example", "server", "srv-1")
		res.State = "running"
		return true
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("observe reported %v; the caller must learn the resource is gone", err)
	}
	if _, found := st.Get("example", "server", "srv-1"); found {
		t.Error("the read brought back a deleted server, address and all")
	}
}

// A missing target costs no runtime call: asking a runtime about a resource that
// does not exist is work done for a 404, and on a slow driver it is seconds of it.
func TestObserveRefusesAMissingTargetBeforeTheLook(t *testing.T) {
	st, b, _ := transitionFixture()

	looked := false
	_, err := b.Observe(st, time.Now, "server", "srv-absent", func(*resource.Resource) bool {
		looked = true
		return true
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("observe reported %v, want ErrNotFound", err)
	}
	if looked {
		t.Error("the look ran on a resource that does not exist")
	}
}
