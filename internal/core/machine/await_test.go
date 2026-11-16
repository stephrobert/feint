package machine

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// eventDeadline bounds the waits in this package's tests for an event the code
// under test must produce: begin a plug, begin an edit, issue a command, call a
// look. It also bounds the waits for a subject to return: where the return
// travels on a channel (Attach and Detach in incus_attach_lock_test.go, a build
// in incus_imagebuild_test.go, the first Observe in observe_test.go), and where
// it is a sync.WaitGroup over goroutines running the subject, through
// awaitWaitGroup (#963). And it bounds the second operation of each race test,
// which the test used to call directly from its own goroutine and now runs
// through awaitReturn: a second operation that hangs fails the test instead of
// holding it until the package timeout (#963).
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

// awaitWaitGroup is wg.Wait() with a deadline: it returns once every goroutine
// counted on wg is done, or fails the test once within has passed, naming what
// did not finish (#963).
//
// The goroutines a test counts on a WaitGroup run the subject, so a bare
// wg.Wait() is a wait on the subject: a subject that deadlocks holds it until
// `go test` kills the whole package, ten minutes later, without naming the test.
// The goroutine that runs wg.Wait() here is left behind when the deadline
// passes; the test has failed by then, and it cannot be ended from outside.
//
// Call it from the test's own goroutine, like awaitEvent.
//
// TestAwaitWaitGroupFailsFastOnAGoroutineThatNeverFinishes fails without the
// deadline, and TestAwaitWaitGroupReturnsOnceEveryGoroutineIsDone without the
// wait itself.
func awaitWaitGroup(t fatalReporter, wg *sync.WaitGroup, within time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	awaitEvent[struct{}](t, done, within, what)
}

// awaitReturn runs op on a goroutine of its own and hands back its error, or
// fails the test once within has passed, naming what did not return (#963).
//
// It is for the second operation of a race test, the one the test used to call
// from its own goroutine once the first had reached its event. Called directly,
// a second operation that blocks for good (a lock the first never releases)
// held the test until the package timeout, and the bounded wait on the first
// operation, placed after it, never ran: measured on 2026-11-15 with
// IsolateNetwork made to keep its network lock, the direct RemoveNetwork of
// TestAnIsolationDetachDoesNotOrphanTheNetworkBeingDeleted hung until
// -timeout. op is left behind when the deadline passes, so it must not call t.
//
// Call it from the test's own goroutine, like awaitEvent.
//
// TestAwaitReturnFailsFastOnAnOperationThatNeverReturns fails without the
// deadline, and TestAwaitReturnHandsBackTheOperationsError without the error.
func awaitReturn(t fatalReporter, within time.Duration, what string, op func() error) error {
	t.Helper()
	returned := make(chan error, 1)
	go func() { returned <- op() }()
	return awaitEvent(t, returned, within, what)
}

// errorReporter is what goroutineErrors.report needs of a test: *testing.T
// satisfies it, and so does errorRecorder, so the helper's own test can read
// what it reports.
type errorReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// goroutineErrors is where a goroutine running the subject puts its failures,
// in place of t.Errorf, and the test reports them once its bounded wait has
// returned (#963).
//
// A bounded wait gives up while the goroutine still runs. If that goroutine
// then calls t.Errorf or t.Fatalf, the testing package panics the whole binary
// ("Fail in goroutine after TestX has completed"), and every test after it in
// the package goes unrun: a subject slower than the deadline costs the package
// instead of one red test. Before the waits were bounded, the same slow run
// just passed late. Here a late failure lands in the collector of a test that
// has already failed on its deadline, and nobody reads it.
//
// Call report from the test's own goroutine, after the wait returned: only
// then is every goroutine done writing.
//
// TestGoroutineErrorsReportsEveryFailureOnceTheWaitReturns fails if report
// drops what was collected.
type goroutineErrors struct {
	mu   sync.Mutex
	errs []string
}

// Errorf records one failure; it is safe to call from any goroutine, at any
// time, including after the test has ended.
func (g *goroutineErrors) Errorf(format string, args ...any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.errs = append(g.errs, fmt.Sprintf(format, args...))
}

// report fails t once per failure recorded so far.
func (g *goroutineErrors) report(t errorReporter) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.errs {
		t.Errorf("%s", e)
	}
}

// errorRecorder stands in for *testing.T in goroutineErrors' own tests.
type errorRecorder struct {
	mu      sync.Mutex
	reports []string
}

func (r *errorRecorder) Helper() {}

func (r *errorRecorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, fmt.Sprintf(format, args...))
}

func (r *errorRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reports...)
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

// runAwaitWaitGroup is runAwait for awaitWaitGroup.
func runAwaitWaitGroup(wg *sync.WaitGroup, within, budget time.Duration, what string) (*fatalRecorder, bool) {
	rec := &fatalRecorder{}
	out := make(chan struct{})
	go func() {
		defer close(out)
		awaitWaitGroup(rec, wg, within, what)
	}()
	select {
	case <-out:
		return rec, true
	case <-time.After(budget):
		return rec, false
	}
}

// The refusing half: a goroutine that never finishes, which is what a
// deadlocked subject looks like from the WaitGroup, fails the test fast, once,
// and says what did not finish.
func TestAwaitWaitGroupFailsFastOnAGoroutineThatNeverFinishes(t *testing.T) {
	const what = "the eight deletes never all returned, so RemoveNetwork deadlocked"
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(wg.Done) // release the helper's own goroutine once the test is over

	begun := time.Now()
	rec, returned := runAwaitWaitGroup(&wg, 50*time.Millisecond, 2*time.Second, what)
	if !returned {
		t.Fatal("awaitWaitGroup was still blocked 2 s after its 50 ms deadline: " +
			"a bare wg.Wait() hangs the package instead of failing the test (#963)")
	}
	reports := rec.recorded()
	if len(reports) != 1 {
		t.Fatalf("awaitWaitGroup reported %d failures for one goroutine that never finished, want 1: %q", len(reports), reports)
	}
	if !strings.Contains(reports[0], what) {
		t.Fatalf("the failure does not say what did not finish: %q, want it to contain %q", reports[0], what)
	}
	if waited := time.Since(begun); waited < 50*time.Millisecond {
		t.Fatalf("awaitWaitGroup gave up after %v, before its 50 ms deadline", waited)
	}
}

// The accepting half: goroutines that finish are not a failure, whether they
// had finished before the wait or finish during it. And the helper does wait:
// it does not return while one is still running, or the assertions after it
// would read what the subject has not written yet.
func TestAwaitWaitGroupReturnsOnceEveryGoroutineIsDone(t *testing.T) {
	t.Run("nothing left to wait for", func(t *testing.T) {
		var wg sync.WaitGroup
		rec, returned := runAwaitWaitGroup(&wg, time.Second, 2*time.Second, "empty")
		if !returned || len(rec.recorded()) != 0 {
			t.Fatalf("returned=%v reports=%q, want an immediate return and no failure", returned, rec.recorded())
		}
	})

	t.Run("goroutines that finish inside the deadline", func(t *testing.T) {
		var wg sync.WaitGroup
		var finished atomic.Int32
		const goroutines = 3
		for range goroutines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(20 * time.Millisecond)
				finished.Add(1)
			}()
		}
		rec, returned := runAwaitWaitGroup(&wg, time.Second, 2*time.Second, "later")
		if !returned || len(rec.recorded()) != 0 {
			t.Fatalf("returned=%v reports=%q, want the goroutines awaited and no failure", returned, rec.recorded())
		}
		if got := finished.Load(); got != goroutines {
			t.Fatalf("awaitWaitGroup returned with %d of %d goroutines finished: it did not wait", got, goroutines)
		}
	})
}

// The collector loses nothing: what goroutines recorded reaches the test once
// report runs, each failure once. Replacing
// t.Errorf by a collector that dropped its failures would turn every site that
// uses it green on a subject that fails.
func TestGoroutineErrorsReportsEveryFailureOnceTheWaitReturns(t *testing.T) {
	var errs goroutineErrors
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs.Errorf("create fnt-batch%d: refused", i)
		}()
	}
	awaitWaitGroup(t, &wg, eventDeadline, "the three recording goroutines never returned")

	rec := &errorRecorder{}
	errs.report(rec)
	reports := rec.recorded()
	if len(reports) != 3 {
		t.Fatalf("report failed the test %d times for three recorded failures, want 3: %q", len(reports), reports)
	}
	for i := range 3 {
		want := fmt.Sprintf("create fnt-batch%d: refused", i)
		found := 0
		for _, r := range reports {
			if r == want {
				found++
			}
		}
		if found != 1 {
			t.Errorf("%q was reported %d times, want once: %q", want, found, reports)
		}
	}
}

// runAwaitReturn is runAwait for awaitReturn.
func runAwaitReturn(within, budget time.Duration, what string, op func() error) (*fatalRecorder, error, bool) {
	rec := &fatalRecorder{}
	out := make(chan error, 1)
	go func() {
		out <- awaitReturn(rec, within, what, op)
	}()
	select {
	case err := <-out:
		return rec, err, true
	case <-time.After(budget):
		return rec, nil, false
	}
}

// The refusing half: a second operation that never returns, which is what one
// blocked on a lock the first never releases looks like, fails the test fast,
// once, and says what did not return.
func TestAwaitReturnFailsFastOnAnOperationThatNeverReturns(t *testing.T) {
	const what = "RemoveNetwork never returned, so the delete deadlocked behind the detach"
	never := make(chan struct{})
	t.Cleanup(func() { close(never) }) // release the left-behind operation once the test is over

	begun := time.Now()
	rec, _, returned := runAwaitReturn(50*time.Millisecond, 2*time.Second, what, func() error {
		<-never
		return nil
	})
	if !returned {
		t.Fatal("awaitReturn was still blocked 2 s after its 50 ms deadline: " +
			"a second operation called directly hangs the package instead of failing the test (#963)")
	}
	reports := rec.recorded()
	if len(reports) != 1 || !strings.Contains(reports[0], what) {
		t.Fatalf("reports=%q, want exactly one failure naming %q", reports, what)
	}
	if waited := time.Since(begun); waited < 50*time.Millisecond {
		t.Fatalf("awaitReturn gave up after %v, before its 50 ms deadline", waited)
	}
}

// The accepting half: an operation that returns is not a failure, and the test
// gets its error back, nil or not. The race tests assert on that error (a
// detach that failed for a reason other than the network being gone), so a
// helper that lost it would turn those assertions blind.
func TestAwaitReturnHandsBackTheOperationsError(t *testing.T) {
	gone := fmt.Errorf("detach fnt-a1b2c3d4e5f: %w", ErrNetworkGone)
	for _, want := range []error{nil, gone} {
		rec, got, returned := runAwaitReturn(time.Second, 2*time.Second, "op", func() error { return want })
		if !returned || len(rec.recorded()) != 0 {
			t.Fatalf("returned=%v reports=%q, want the return received and no failure", returned, rec.recorded())
		}
		if !errors.Is(got, want) || (want == nil) != (got == nil) {
			t.Fatalf("awaitReturn handed back %v, want %v", got, want)
		}
	}
}
