package machine

import (
	"context"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/serialise"
)

// TestAttachAsksWhoseInstanceItIsUnderTheLockItActsUnder holds the placement
// of Attach's instance question (#934): it is asked after the per-machine lock
// is taken, so the answer acted on is the answer taken under the exclusion the
// action runs in.
//
// Moving the question above the lock compiled, vetted, and left every test in
// this package green — measured by a falsifier pass over #909, and again on
// ad6e6fb. Argv alone cannot see it: the same commands are emitted in the same
// order either way. What can see it is ordering against the lock, so this test
// takes the lock itself, as another action on the same machine would, and
// watches for the ownership probe while it holds it.
//
// Sound in the passing direction: with the question under the lock, the probe
// cannot be emitted until the test releases, so no amount of scheduling delay
// produces a false failure. With the question above the lock the probe is
// emitted within microseconds of the goroutine starting, and the test waits
// 200 ms for it.
func TestAttachAsksWhoseInstanceItIsUnderTheLockItActsUnder(t *testing.T) {
	const machine = "feint-scw-mine"
	network := NetworkName(NetworkPrefix, "0b9c1d2e-aaaa-bbbb-cccc-111122223333")

	probed := make(chan struct{}, 1)
	f := attachRuntime(machine)
	answer := f.hook
	f.hook = func(call int, args []string) ([]byte, error, bool) {
		if len(args) == 4 && args[0] == "config" && args[1] == "get" && args[3] == "user."+LabelKey {
			select {
			case probed <- struct{}{}:
			default:
			}
		}
		return answer(call, args)
	}
	d := newFakeDriver(f)

	// Another action on the same machine holds the lock Attach takes.
	release := serialise.Lock("incus.attach." + machine)
	done := make(chan error, 1)
	go func() {
		done <- d.Attach(context.Background(), machine, Attachment{Network: network})
	}()

	select {
	case <-probed:
		release()
		// Bounded (#935): this branch is already a failure, and a subject that
		// then never returned would turn it into a hang that names nothing.
		_ = awaitEvent(t, done, eventDeadline, "Attach asked whose instance it is outside the lock, then never returned once it was released")
		t.Fatal("Attach asked whose instance it is while another action on the same machine held its lock:\n" +
			"the answer it then acts on was taken outside the exclusion the action runs in")
	case <-time.After(200 * time.Millisecond):
	}

	release()
	if err := awaitEvent(t, done, eventDeadline, "Attach never finished after the lock was released"); err != nil {
		t.Fatalf("once the lock was free, the attach of an owned machine must succeed: %v", err)
	}
	// And the question was asked at all, after the release: a guard that
	// stopped asking would pass the wait above.
	select {
	case <-probed:
	default:
		t.Fatalf("Attach never asked whose instance it is; commands: %v", f.commands())
	}
}

// TestDetachAsksWhoseInstanceItIsUnderTheLockItActsUnder is the same property
// on Detach, which takes the same per-machine lock and removes a device on the
// strength of the same answer. Same method: the test holds the lock, and the
// ownership probe must not be emitted while it does.
func TestDetachAsksWhoseInstanceItIsUnderTheLockItActsUnder(t *testing.T) {
	const machine = "feint-scw-mine"
	network := NetworkName(NetworkPrefix, "0b9c1d2e-aaaa-bbbb-cccc-111122223333")

	probed := make(chan struct{}, 1)
	f := attachRuntime(machine)
	answer := f.hook
	f.hook = func(call int, args []string) ([]byte, error, bool) {
		if len(args) == 4 && args[0] == "config" && args[1] == "get" && args[3] == "user."+LabelKey {
			select {
			case probed <- struct{}{}:
			default:
			}
		}
		return answer(call, args)
	}
	d := newFakeDriver(f)

	release := serialise.Lock("incus.attach." + machine)
	done := make(chan error, 1)
	go func() {
		done <- d.Detach(context.Background(), machine, network)
	}()

	select {
	case <-probed:
		release()
		// Bounded (#935): this branch is already a failure, and a subject that
		// then never returned would turn it into a hang that names nothing.
		_ = awaitEvent(t, done, eventDeadline, "Detach asked whose instance it is outside the lock, then never returned once it was released")
		t.Fatal("Detach asked whose instance it is while another action on the same machine held its lock:\n" +
			"the answer it then acts on was taken outside the exclusion the removal runs in")
	case <-time.After(200 * time.Millisecond):
	}

	release()
	// The machine carries no device on the network, which is the absent case
	// Detach answers nil to: nothing to remove, and not a refusal.
	if err := awaitEvent(t, done, eventDeadline, "Detach never finished after the lock was released"); err != nil {
		t.Fatalf("once the lock was free, the detach of an owned machine must not fail: %v", err)
	}
	select {
	case <-probed:
	default:
		t.Fatalf("Detach never asked whose instance it is; commands: %v", f.commands())
	}
}
