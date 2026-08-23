package emulator_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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
func barrierPack(env *emulator.Env) stubPack {
	const provider, kind = "stub", "thing"
	tenant := resource.Tenant{Provider: provider}

	// Both handlers meet here before the first store touch and leave together,
	// so the overlap is a fact of the test rather than a hope about timing.
	var barrier sync.WaitGroup
	barrier.Add(2)
	cycle := func(w http.ResponseWriter, _ *http.Request) {
		barrier.Done()
		barrier.Wait()
		id := env.NewID()
		env.Store.Put(&resource.Resource{ID: id, Kind: kind, Tenant: tenant})
		env.Store.Delete(provider, kind, id)
		emulator.WriteJSON(w, http.StatusOK, map[string]string{"id": id})
	}
	return stubPack{name: provider, routes: []emulator.Route{
		{Method: "POST", Path: "/stub/left", Operation: "stub/v1/API.Left", Handler: cycle},
		{Method: "POST", Path: "/stub/right", Operation: "stub/v1/API.Right", Handler: cycle},
	}}
}

func TestConcurrentClientsKeepTheirAttribution(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, barrierPack(env))
	if err != nil {
		t.Fatalf("mount the barrier pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	span := openSpan(t, ts, "behaviour")

	var wg sync.WaitGroup
	for _, path := range []string{"/stub/left", "/stub/right"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, status := call(t, ts, http.MethodPost, path, "", false); status != http.StatusOK {
				t.Errorf("%s answered %d", path, status)
			}
		}()
	}
	wg.Wait()

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

	done := make(chan struct{})
	go func() {
		defer close(done)
		call(t, ts, http.MethodPost, "/stub/slow", "", false)
	}()
	<-entered // in flight, and no span is open yet

	span := openSpan(t, ts, "behaviour")
	call(t, ts, http.MethodPost, "/stub/quick", "", false)
	close(release)
	<-done

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
