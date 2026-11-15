package machine

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventDeadline bounds the waits in this package's tests for an event the code
// under test must produce: begin a plug, begin an edit, issue a command, call a
// look. It also bounds a few waits for a subject to return, where the return
// travels on a channel: Attach and Detach in incus_attach_lock_test.go, a build
// in incus_imagebuild_test.go, the first Observe in observe_test.go. The other
// returns stay unbounded: every wg.Wait() of the package, and the second
// operation each race test runs from its own goroutine, still block until the
// package timeout if the subject hangs. That is #963, with the receives of the
// same shape outside this package.
//
// The events it bounds arrive within milliseconds: the five start races of
// incus_start_race_test.go reach their plug in a few, and each whole test takes
// 0.20 s under -race, the fake's 200 ms beat (measured 2026-11-15). Five seconds
// is over twenty times that whole test, so a loaded machine running the
// package under -race does not reach it. And it is far under every timeout
// that would otherwise end the wait: the 10-minute default of `go test`, and
// the 90 s a falsify sweep over #909 ran with, where three mutations that made
// Attach refuse early each hung until it and came back truncated (#935).
const eventDeadline = 5 * time.Second

// fatalReporter is what awaitEvent needs of a test: *testing.T satisfies it,
// and so does the recorder its own test uses, which is the only way to watch a
// Fatalf without ending the goroutine that watches.
type fatalReporter interface {
	Helper()
	Fatalf(format string, args ...any)
}

// awaitEvent receives one value from ch, or fails the test once within has
// passed, naming what did not happen (#935).
//
// A bare `<-ch` on an event the subject must produce turns a guard that refuses
// early into a hang: the event never comes, the receive never returns, and
// `go test` kills the whole package on its global timeout instead of naming
// the test and the reason. A falsify verdict cut short that way reads like a
// mutation with fewer catchers.
//
// Call it from the test's own goroutine: Fatalf ends the goroutine it runs in,
// and only the test's own goroutine ending fails the test cleanly.
//
// TestAwaitEventFailsFastOnAnEventThatNeverComes fails without the deadline.
func awaitEvent[T any](t fatalReporter, ch <-chan T, within time.Duration, what string) T {
	t.Helper()
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
	}
	t.Fatalf("waited %v and %s", within, what)
	var zero T
	return zero
}

// awaitEventBefore waits for event like awaitEvent, but also watches the
// subject that must produce it: returned carries the subject's error once it
// has returned. A subject that returns before the event refused early, and the
// test fails at once with that error rather than after the whole deadline with
// none: under a guard that refuses an owned machine, the bare deadline said
// "the plug never began" and left "was not created by the emulator" unprinted.
//
// An event that came and a subject that then returned can both be ready when
// the test's goroutine is scheduled late, and select picks between them at
// random, so the return is only a failure if the event has not come.
//
// TestAwaitEventBeforeFailsAtOnceWhenTheSubjectReturnsFirst fails without the
// returned case, and TestAwaitEventBeforeTakesTheEventOverALaterReturn without
// the second look at event.
func awaitEventBefore(t fatalReporter, event <-chan struct{}, returned <-chan error, within time.Duration, what string) {
	t.Helper()
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case <-event:
		return
	case err := <-returned:
		select {
		case <-event:
			return
		default:
		}
		t.Fatalf("%s: the subject returned first, with %v", what, err)
	case <-timer.C:
		t.Fatalf("waited %v and %s", within, what)
	}
}

// fatalRecorder stands in for *testing.T: it records what awaitEvent reports
// instead of ending the goroutine, so a test can read the report.
type fatalRecorder struct {
	mu      sync.Mutex
	reports []string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, fmt.Sprintf(format, args...))
}

func (r *fatalRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reports...)
}

// runAwait runs awaitEvent on a recorder, in a goroutine of its own, and
// reports whether it returned within budget. The budget is the test's own
// bound on the helper: a helper that lost its deadline blocks forever, and
// this test has to fail on that rather than hang the package the way the
// waits it replaces did.
func runAwait[T any](ch <-chan T, within, budget time.Duration, what string) (*fatalRecorder, T, bool) {
	rec := &fatalRecorder{}
	type result struct{ v T }
	out := make(chan result, 1)
	go func() {
		out <- result{awaitEvent[T](rec, ch, within, what)}
	}()
	select {
	case r := <-out:
		return rec, r.v, true
	case <-time.After(budget):
		var zero T
		return rec, zero, false
	}
}

// The refusing half: an event that never comes fails the test, fast, once,
// and says what did not happen.
func TestAwaitEventFailsFastOnAnEventThatNeverComes(t *testing.T) {
	const what = "the plug never began, so the guard refused before the device add"
	never := make(chan struct{})

	begun := time.Now()
	rec, _, returned := runAwait[struct{}](never, 50*time.Millisecond, 2*time.Second, what)
	if !returned {
		t.Fatal("awaitEvent was still blocked 2 s after its 50 ms deadline: " +
			"a wait with no deadline hangs the package instead of failing the test (#935)")
	}
	reports := rec.recorded()
	if len(reports) != 1 {
		t.Fatalf("awaitEvent reported %d failures for one event that never came, want 1: %q", len(reports), reports)
	}
	if !strings.Contains(reports[0], what) {
		t.Fatalf("the failure does not say what did not happen: %q, want it to contain %q", reports[0], what)
	}
	if waited := time.Since(begun); waited < 50*time.Millisecond {
		t.Fatalf("awaitEvent gave up after %v, before its 50 ms deadline", waited)
	}
}

// The accepting half: an event that comes is not a failure, whether it was
// there before the wait, arrives during it, or carries a value. A helper that
// failed every wait would pass the test above and break every caller.
func TestAwaitEventReturnsTheEventThatComes(t *testing.T) {
	t.Run("a channel already closed", func(t *testing.T) {
		closed := make(chan struct{})
		close(closed)
		rec, _, returned := runAwait[struct{}](closed, time.Second, 2*time.Second, "closed")
		if !returned || len(rec.recorded()) != 0 {
			t.Fatalf("returned=%v reports=%q, want an immediate return and no failure", returned, rec.recorded())
		}
	})

	t.Run("an event that arrives inside the deadline", func(t *testing.T) {
		later := make(chan struct{})
		go func() {
			time.Sleep(20 * time.Millisecond)
			close(later)
		}()
		rec, _, returned := runAwait[struct{}](later, time.Second, 2*time.Second, "later")
		if !returned || len(rec.recorded()) != 0 {
			t.Fatalf("returned=%v reports=%q, want the late event received and no failure", returned, rec.recorded())
		}
	})

	t.Run("a value is handed back", func(t *testing.T) {
		values := make(chan string, 1)
		values <- "running"
		rec, v, returned := runAwait[string](values, time.Second, 2*time.Second, "value")
		if !returned || len(rec.recorded()) != 0 || v != "running" {
			t.Fatalf("returned=%v reports=%q value=%q, want %q and no failure",
				returned, rec.recorded(), v, "running")
		}
	})
}

// runAwaitBefore is runAwait for awaitEventBefore.
func runAwaitBefore(event <-chan struct{}, returned <-chan error, within, budget time.Duration, what string) (*fatalRecorder, bool) {
	rec := &fatalRecorder{}
	out := make(chan struct{})
	go func() {
		defer close(out)
		awaitEventBefore(rec, event, returned, within, what)
	}()
	select {
	case <-out:
		return rec, true
	case <-time.After(budget):
		return rec, false
	}
}

// The refusing half of the fail-fast path: a subject that returned without
// producing the event fails the test long before the deadline, and the failure
// carries the subject's own error next to what did not happen.
func TestAwaitEventBeforeFailsAtOnceWhenTheSubjectReturnsFirst(t *testing.T) {
	const what = "the plug never began, so Attach refused before the device add"
	const cause = "instance feint-osc-i-9 was not created by the emulator"
	never := make(chan struct{})
	returned := make(chan error, 1)
	returned <- errors.New(cause)

	begun := time.Now()
	rec, finished := runAwaitBefore(never, returned, time.Minute, 2*time.Second, what)
	if !finished {
		t.Fatal("awaitEventBefore was still waiting 2 s after the subject had returned: " +
			"it sits out its whole deadline instead of failing on the early return")
	}
	if waited := time.Since(begun); waited > time.Second {
		t.Fatalf("awaitEventBefore took %v to notice a subject that had already returned", waited)
	}
	reports := rec.recorded()
	if len(reports) != 1 {
		t.Fatalf("awaitEventBefore reported %d failures for one early return, want 1: %q", len(reports), reports)
	}
	for _, want := range []string{what, cause} {
		if !strings.Contains(reports[0], want) {
			t.Fatalf("the failure %q does not carry %q", reports[0], want)
		}
	}

	// A refusal is not always an error: a subject can return nil without ever
	// producing its event (a guard that answers "nothing to do"), and that
	// must fail just as fast. Without this row, a helper that let a nil return
	// through passed every test here, and a product mutation where Attach
	// returned nil without attaching turned two race tests green.
	t.Run("a subject that returns nil before the event", func(t *testing.T) {
		returned := make(chan error, 1)
		returned <- nil
		rec, finished := runAwaitBefore(never, returned, time.Minute, 2*time.Second, what)
		if !finished {
			t.Fatal("awaitEventBefore was still waiting 2 s after the subject had returned nil")
		}
		reports := rec.recorded()
		if len(reports) != 1 || !strings.Contains(reports[0], what) {
			t.Fatalf("reports=%q, want exactly one failure naming %q for a nil return before the event", reports, what)
		}
	})

	t.Run("and still bounds a subject that neither returns nor produces", func(t *testing.T) {
		rec, finished := runAwaitBefore(never, make(chan error, 1), 50*time.Millisecond, 2*time.Second, what)
		if !finished || len(rec.recorded()) != 1 || !strings.Contains(rec.recorded()[0], what) {
			t.Fatalf("finished=%v reports=%q, want one failure naming %q after 50 ms", finished, rec.recorded(), what)
		}
	})
}

// The accepting half: an event that came is not a failure, even when the
// subject has also returned by the time the test looks. Both cases are then
// ready, select picks at random, and a helper that trusted its first pick
// would fail about one run in two; fifty tries make that a certainty.
func TestAwaitEventBeforeTakesTheEventOverALaterReturn(t *testing.T) {
	for i := 0; i < 50; i++ {
		event := make(chan struct{})
		close(event)
		returned := make(chan error, 1)
		returned <- nil
		rec, finished := runAwaitBefore(event, returned, time.Second, 2*time.Second, "event")
		if !finished || len(rec.recorded()) != 0 {
			t.Fatalf("try %d: finished=%v reports=%q, want the event taken and no failure", i, finished, rec.recorded())
		}
	}

	t.Run("an event with the subject still running", func(t *testing.T) {
		event := make(chan struct{})
		close(event)
		rec, finished := runAwaitBefore(event, make(chan error, 1), time.Second, 2*time.Second, "event")
		if !finished || len(rec.recorded()) != 0 {
			t.Fatalf("finished=%v reports=%q, want the event taken and no failure", finished, rec.recorded())
		}
	})
}
