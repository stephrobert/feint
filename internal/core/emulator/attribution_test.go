package emulator_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// #398. Two identical `mise run conformance` runs marked 311 operations each on
// the `behaviour` axis and disagreed on six of them, because a store touch was
// attributed to "the only non-probe request in flight" and terraform runs ten
// requests at a time under one span. The attribution now reads the goroutine
// that made the touch, which the store guarantees is the one handling the
// request (goroutine.go).
//
// Both tests here drive concurrency deliberately: the first proves the axis
// survives it, the second proves that what it still cannot attribute is
// counted and published rather than quietly missing from a number.

// barrierPack mounts two operations that each run a whole lifecycle — create
// then delete — and hold each other in flight for the entire window in which
// they touch the store. Nothing about the old attribution could survive it: two
// non-probe requests were in flight for every touch, so every touch was
// dropped.
//
// Two barriers, not one, and the second is what makes the falsification
// deterministic. With only the entry barrier, both handlers started together and
// then raced: a handler that finished its touches and returned left the flight
// before the other touched anything, so the other attributed correctly even with
// the goroutine check removed. The mutation went undetected in roughly three
// runs out of five — measured on 2026-09-21 — which reads as "this guard stopped
// working" in a nightly replay and is nothing of the sort.
//
// The exit barrier holds both requests in flight until BOTH have touched, which
// is what the sentence above always claimed and what the guard actually has to
// survive.
//
// The barriers are channels, not WaitGroups, so the test can open them from a
// cleanup: if only one request arrives, its handler would wait on its partner
// forever, and httptest's Close, which waits for every request in flight,
// with it (#963). abort opens both.
func barrierPack(env *emulator.Env, abort <-chan struct{}) stubPack {
	const provider, kind = "stub", "thing"
	tenant := resource.Tenant{Provider: provider}

	// Both handlers meet here before the first store touch and leave together,
	// so the overlap is a fact of the test rather than a hope about timing.
	entry, exit := newBarrier(2), newBarrier(2)
	cycle := func(w http.ResponseWriter, _ *http.Request) {
		entry.arrive(abort)
		id := env.NewID()
		env.Store.Put(&resource.Resource{ID: id, Kind: kind, Tenant: tenant})
		env.Store.Delete(provider, kind, id)
		// Nobody leaves until both have touched, or the last toucher is alone in
		// flight and any attribution rule at all would get it right.
		exit.arrive(abort)
		emulator.WriteJSON(w, http.StatusOK, map[string]string{"id": id})
	}
	return stubPack{name: provider, routes: []emulator.Route{
		{Method: "POST", Path: "/stub/left", Operation: "stub/v1/API.Left", Handler: cycle},
		{Method: "POST", Path: "/stub/right", Operation: "stub/v1/API.Right", Handler: cycle},
	}}
}

// barrier lets n callers through once all n have arrived, or every caller
// once abort is closed.
type barrier struct {
	mu    sync.Mutex
	left  int
	ready chan struct{}
}

func newBarrier(n int) *barrier {
	return &barrier{left: n, ready: make(chan struct{})}
}

// arrive counts the caller in and waits for the others, or for abort.
func (b *barrier) arrive(abort <-chan struct{}) {
	b.mu.Lock()
	b.left--
	if b.left == 0 {
		close(b.ready)
	}
	b.mu.Unlock()
	select {
	case <-b.ready:
	case <-abort:
	}
}

func TestConcurrentClientsKeepTheirAttribution(t *testing.T) {
	env := emulator.DefaultEnv()
	abort := make(chan struct{})
	srv, err := emulator.NewServer(env, barrierPack(env, abort))
	if err != nil {
		t.Fatalf("mount the barrier pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// Registered after ts.Close, so it runs first: a handler still waiting on
	// its partner is let go, and Close does not wait on it forever.
	t.Cleanup(func() { close(abort) })

	span := openSpan(t, ts, "behaviour")

	// Errors travel on a channel and are reported once the wait has returned:
	// a goroutine left behind by a failed wait must not call t (#963).
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, path := range []string{"/stub/left", "/stub/right"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, status, err := callFrom(ts, http.MethodPost, path, "", false)
			switch {
			case err != nil:
				failures <- err
			case status != http.StatusOK:
				failures <- fmt.Errorf("%s answered %d", path, status)
			}
		}()
	}
	awaitGroup(t, &wg, "the two overlapping lifecycles never both returned, so the barrier deadlocked")
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	body, status := closeSpan(t, ts, span)
	if status != http.StatusOK {
		t.Fatalf("two overlapping lifecycles were observed and the close was refused: %d %v", status, body)
	}
	// The heart of it: overlapping is not a reason to lose the attribution, and
	// nothing is left over to confess.
	if n, _ := body["unattributed"].(float64); n != 0 {
		t.Errorf("every touch was made by a request being handled, and %v were not attributed", n)
	}

	ev := viewOf(t, ts).Evidence
	for _, op := range []string{"stub/v1/API.Left", "stub/v1/API.Right"} {
		if !ev[op].Behaviour {
			t.Errorf("%s ran a whole lifecycle inside the span and is not marked", op)
		}
	}
}

// slowPack mounts one operation that waits to be released before touching the
// store, so a request can be in flight *before* a span opens and still make its
// touches inside it — the one window the attribution cannot see through,
// because no identity was recorded when the request began.
func slowPack(env *emulator.Env, entered chan<- struct{}, release <-chan struct{}) stubPack {
	const provider, kind = "stub", "thing"
	tenant := resource.Tenant{Provider: provider}
	return stubPack{name: provider, routes: []emulator.Route{
		{Method: "POST", Path: "/stub/slow", Operation: "stub/v1/API.Slow",
			Handler: func(w http.ResponseWriter, _ *http.Request) {
				entered <- struct{}{}
				<-release
				id := env.NewID()
				env.Store.Put(&resource.Resource{ID: id, Kind: kind, Tenant: tenant})
				env.Store.Delete(provider, kind, id)
				emulator.WriteJSON(w, http.StatusOK, map[string]string{"id": id})
			}},
		{Method: "POST", Path: "/stub/quick", Operation: "stub/v1/API.Quick",
			Handler: func(w http.ResponseWriter, _ *http.Request) {
				id := env.NewID()
				env.Store.Put(&resource.Resource{ID: id, Kind: kind, Tenant: tenant})
				env.Store.Delete(provider, kind, id)
				emulator.WriteJSON(w, http.StatusOK, map[string]string{"id": id})
			}},
	}}
}

func TestASpanReportsTheTouchesItCouldNotAttribute(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, slowPack(env, entered, release))
	if err != nil {
		t.Fatalf("mount the slow pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// Registered after ts.Close, so it runs first: a test that fails while the
	// slow handler is parked frees it, and Close, which waits for every request
	// in flight, does not wait on it forever.
	var releaseOnce sync.Once
	releaseSlow := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseSlow)

	slowFailed := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := callFrom(ts, http.MethodPost, "/stub/slow", "", false); err != nil {
			slowFailed <- err
		}
	}()
	// In flight, and no span is open yet.
	awaitSubject(t, entered, "the slow request never reached its handler")

	span := openSpan(t, ts, "behaviour")
	call(t, ts, http.MethodPost, "/stub/quick", "", false)
	releaseSlow()
	awaitSubject(t, done, "the slow request never returned once released")
	select {
	case err := <-slowFailed:
		t.Fatalf("the slow request: %v", err)
	default:
	}

	body, status := closeSpan(t, ts, span)
	if status != http.StatusOK {
		t.Fatalf("a lifecycle was observed and the close was refused: %d %v", status, body)
	}
	// Two touches — the create and the delete the slow handler made — took part
	// in a lifecycle this span observed and belong to no request it could name.
	// Publishing the figure is the whole point: an axis that says "I could not
	// attribute two of these" is honest, and one that silently marks one
	// operation fewer is the artefact that moves on its own.
	if n, _ := body["unattributed"].(float64); n != 2 {
		t.Errorf("the span made two touches it could not attribute and reports %v", n)
	}
	ev := viewOf(t, ts).Evidence
	if !ev["stub/v1/API.Quick"].Behaviour {
		t.Error("the attributable lifecycle is not marked")
	}
	// And it is still refused the axis rather than guessed onto it.
	if ev["stub/v1/API.Slow"].Behaviour {
		t.Error("an operation whose touches could not be attributed earned the axis anyway")
	}
}

// subjectDeadline bounds this file's waits on something the code under test
// must do: reach the fake runtime, or return once released. The fakes answer
// in microseconds, so five seconds is reached only when the event is not
// coming (#963).
const subjectDeadline = 5 * time.Second

// awaitSubject receives one value from ch, or fails the test once
// subjectDeadline has passed, naming what did not happen (#963). A bare receive
// there turns a subject that refused early or deadlocked into a hang until the
// package timeout, without the test's name. It is a copy of the shape of awaitEvent (internal/core/machine, await_test.go), kept here because a test helper cannot be imported across packages.
// Its limit, measured on 2026-11-15 with Go 1.26.11: when a handler never returns,
// the httptest server's Close, run at cleanup, waits for it and the failure message
// is lost under the package timeout. It names the early refusal, which returns
// its request, not a handler that deadlocks. The slow handler of
// TestASpanReportsTheTouchesItCouldNotAttribute is released at cleanup, ahead
// of Close, so a failure while it is parked is not caught by that limit.
//
// Call it from the test's own goroutine.
func awaitSubject[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	timer := time.NewTimer(subjectDeadline)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
	}
	t.Fatalf("waited %v and %s", subjectDeadline, what)
	var zero T
	return zero
}

// awaitGroup is wg.Wait() bounded by awaitSubject: the goroutines counted on wg
// run the subject.
func awaitGroup(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	awaitSubject(t, done, what)
}

// A request to a handler that never answers gives up, with the call named,
// instead of holding its caller: call runs on the test's own goroutine, where
// no awaitSubject reaches, so the client's timeout is the only bound it has
// (#963). The give-up is played on a client of 50 ms, so the test does not sit
// out the real five seconds on every run; that callClient is bounded by
// subjectDeadline is asserted apart.
func TestARequestToAHandlerThatNeverAnswersGivesUp(t *testing.T) {
	t.Parallel()
	never := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-never }))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(never) }) // registered last, so it frees the handler before Close waits for it
	if got := callClient(ts).Timeout; got != subjectDeadline {
		t.Errorf("callClient gives up after %v, want subjectDeadline (%v): a call from the test's own goroutine is unbounded", got, subjectDeadline)
	}

	failed := make(chan error, 1)
	go func() {
		_, _, err := callWith(boundedClient(ts, 50*time.Millisecond), ts, http.MethodPost, "/stub/slow", "", false)
		failed <- err
	}()
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("a handler that never answered produced no error")
		}
		if !strings.Contains(err.Error(), "/stub/slow") {
			t.Errorf("the failure does not name the call: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the request was still waiting 2 s on a handler that never answers, past its 50 ms timeout: the client does not give up")
	}
}

// The barrier holds a caller until its partner arrives, and lets it go once
// abort is closed: the second half is what lets a cleanup free a handler whose
// partner never came, where a WaitGroup could not be released (#963).
func TestABarrierHoldsUntilItsPartnerOrTheAbort(t *testing.T) {
	b := newBarrier(2)
	abort := make(chan struct{})
	through := make(chan struct{})
	go func() {
		b.arrive(abort)
		close(through)
	}()
	select {
	case <-through:
		t.Fatal("a caller went through the barrier alone, before its partner and with no abort")
	case <-time.After(50 * time.Millisecond):
	}
	close(abort)
	awaitSubject(t, through, "the abort did not let the lone caller through the barrier")

	t.Run("two callers meet", func(t *testing.T) {
		b := newBarrier(2)
		met := make(chan struct{}, 2)
		for range 2 {
			go func() {
				b.arrive(make(chan struct{}))
				met <- struct{}{}
			}()
		}
		awaitSubject(t, met, "the first caller never left once both had arrived")
		awaitSubject(t, met, "the second caller never left once both had arrived")
	})
}
