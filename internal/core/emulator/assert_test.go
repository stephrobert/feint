package emulator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The assertion span is what lets a suite say "this block proves a lifecycle"
// or "this block demanded a refusal" without naming one operation by hand: the
// suite brackets the block, and the emulator resolves what happened inside from
// its own observations — and refuses the claim when it observed nothing that
// supports it. Every test here holds one half of that bargain.

// lifecyclePack is a stub provider whose handlers really use the store, the way
// every pack does: create writes, get reads, list lists, delete deletes. The
// catalogue route touches no resource at all, which is exactly the kind of read
// a real CLI performs on its way to a create.
func lifecyclePack(env *emulator.Env) stubPack {
	const provider, kind = "stub", "thing"
	tenant := resource.Tenant{Provider: provider}
	return stubPack{name: provider, routes: []emulator.Route{
		{Method: "POST", Path: "/stub/things", Operation: "stub/v1/API.CreateThing",
			Handler: func(w http.ResponseWriter, _ *http.Request) {
				id := env.NewID()
				env.Store.Put(&resource.Resource{ID: id, Kind: kind, Tenant: tenant})
				emulator.WriteJSON(w, http.StatusCreated, map[string]string{"id": id})
			}},
		{Method: "GET", Path: "/stub/things", Operation: "stub/v1/API.ListThings",
			Handler: func(w http.ResponseWriter, _ *http.Request) {
				emulator.WriteJSON(w, http.StatusOK, map[string]int{"count": len(env.Store.List(kind, tenant))})
			}},
		{Method: "GET", Path: "/stub/things/{id}", Operation: "stub/v1/API.GetThing",
			Handler: func(w http.ResponseWriter, r *http.Request) {
				if _, ok := env.Store.Get(provider, kind, r.PathValue("id")); !ok {
					emulator.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such thing"})
					return
				}
				emulator.WriteJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id")})
			}},
		{Method: "DELETE", Path: "/stub/things/{id}", Operation: "stub/v1/API.DeleteThing",
			Handler: func(w http.ResponseWriter, r *http.Request) {
				if !env.Store.Delete(provider, kind, r.PathValue("id")) {
					emulator.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such thing"})
					return
				}
				emulator.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
			}},
		{Method: "GET", Path: "/stub/catalog", Operation: "stub/v1/API.GetCatalog",
			Handler: func(w http.ResponseWriter, _ *http.Request) {
				emulator.WriteJSON(w, http.StatusOK, map[string]string{"offer": "small"})
			}},
	}}
}

func assertServer(t *testing.T) (*httptest.Server, *emulator.Env) {
	t.Helper()
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, lifecyclePack(env))
	if err != nil {
		t.Fatalf("mount the lifecycle pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, env
}

// call issues one request and returns the decoded body and status.
func call(t *testing.T, ts *httptest.Server, method, path, body string, probe bool) (map[string]any, int) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body)) //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	if probe {
		req.Header.Set(emulator.ProbeHeader, "1")
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return decoded, resp.StatusCode
}

func openSpan(t *testing.T, ts *httptest.Server, proves string) string {
	t.Helper()
	body, status := call(t, ts, http.MethodPost, "/_feint/assert", `{"proves":"`+proves+`"}`, false)
	if status != http.StatusCreated {
		t.Fatalf("open a %s span: status %d, body %v", proves, status, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("no span id in %v", body)
	}
	return id
}

func closeSpan(t *testing.T, ts *httptest.Server, id string) (map[string]any, int) {
	t.Helper()
	return call(t, ts, http.MethodPost, "/_feint/assert/"+id, "", false)
}

func TestABehaviourSpanNeedsAnObservedLifecycle(t *testing.T) {
	ts, _ := assertServer(t)
	id := openSpan(t, ts, "behaviour")

	// Plenty of traffic, no lifecycle: the catalogue is read, nothing is
	// created and destroyed.
	call(t, ts, http.MethodGet, "/stub/catalog", "", false)

	body, status := closeSpan(t, ts, id)
	if status != http.StatusConflict {
		t.Fatalf("a lifecycle claim with no observed lifecycle must be refused, got %d: %v", status, body)
	}
	if ev := viewOf(t, ts).Evidence["stub/v1/API.GetCatalog"]; ev.Behaviour {
		t.Error("a refused span must mark nothing")
	}
}

func TestABehaviourSpanMarksTheLifecycleAndNotTheCatalogue(t *testing.T) {
	ts, _ := assertServer(t)
	id := openSpan(t, ts, "behaviour")

	created, _ := call(t, ts, http.MethodPost, "/stub/things", "", false)
	thing, _ := created["id"].(string)
	if thing == "" {
		t.Fatalf("no id in the create answer: %v", created)
	}
	call(t, ts, http.MethodGet, "/stub/things/"+thing, "", false)
	call(t, ts, http.MethodGet, "/stub/things", "", false)
	call(t, ts, http.MethodGet, "/stub/catalog", "", false)
	call(t, ts, http.MethodDelete, "/stub/things/"+thing, "", false)

	body, status := closeSpan(t, ts, id)
	if status != http.StatusOK {
		t.Fatalf("a full lifecycle was observed and the close was refused: %d %v", status, body)
	}

	ev := viewOf(t, ts).Evidence
	for _, op := range []string{
		"stub/v1/API.CreateThing", "stub/v1/API.GetThing",
		"stub/v1/API.ListThings", "stub/v1/API.DeleteThing",
	} {
		if !ev[op].Behaviour {
			t.Errorf("%s took part in the observed lifecycle and is not marked", op)
		}
	}
	// The read a client performs on its way to a create proves nothing about
	// behaviour, and marking it would dilute the axis into a copy of `client`.
	if ev["stub/v1/API.GetCatalog"].Behaviour {
		t.Error("the catalogue read earned a behaviour it did not prove")
	}
}

func TestABehaviourSpanRefusesACleanupOnlyDelete(t *testing.T) {
	ts, _ := assertServer(t)

	// Created before the span: deleting it inside the span is cleanup, not a
	// lifecycle this span observed end to end.
	created, _ := call(t, ts, http.MethodPost, "/stub/things", "", false)
	thing, _ := created["id"].(string)

	id := openSpan(t, ts, "behaviour")
	call(t, ts, http.MethodDelete, "/stub/things/"+thing, "", false)
	body, status := closeSpan(t, ts, id)
	if status != http.StatusConflict {
		t.Fatalf("a delete of something created before the span is not a lifecycle, got %d: %v", status, body)
	}
}

func TestANegativeSpanNeedsARefusal(t *testing.T) {
	ts, _ := assertServer(t)
	id := openSpan(t, ts, "negative")

	// Success only — and a probe-driven 404, which must not count either: the
	// probe is not a client, on this axis as on every other.
	call(t, ts, http.MethodGet, "/stub/catalog", "", false)
	call(t, ts, http.MethodGet, "/stub/things/absent", "", true)

	body, status := closeSpan(t, ts, id)
	if status != http.StatusConflict {
		t.Fatalf("a refusal claim with no observed refusal must be refused, got %d: %v", status, body)
	}
}

func TestANegativeSpanMarksTheRefusedOperation(t *testing.T) {
	ts, _ := assertServer(t)
	id := openSpan(t, ts, "negative")

	call(t, ts, http.MethodGet, "/stub/catalog", "", false)
	if _, status := call(t, ts, http.MethodGet, "/stub/things/absent", "", false); status != http.StatusNotFound {
		t.Fatalf("the stub should refuse a missing thing, got %d", status)
	}

	body, status := closeSpan(t, ts, id)
	if status != http.StatusOK {
		t.Fatalf("a refusal was observed and the close was refused: %d %v", status, body)
	}

	ev := viewOf(t, ts).Evidence
	if !ev["stub/v1/API.GetThing"].Negative {
		t.Error("the operation that refused is not marked")
	}
	if ev["stub/v1/API.GetCatalog"].Negative {
		t.Error("an operation that answered 200 must not read negative")
	}
}

func TestASpanCannotBeClosedTwiceAndAnUnknownAxisIsRefused(t *testing.T) {
	ts, _ := assertServer(t)

	if body, status := call(t, ts, http.MethodPost, "/_feint/assert", `{"proves":"excellence"}`, false); status != http.StatusBadRequest {
		t.Fatalf("an axis this record does not have must be refused, got %d: %v", status, body)
	}

	id := openSpan(t, ts, "negative")
	call(t, ts, http.MethodGet, "/stub/things/absent", "", false)
	if _, status := closeSpan(t, ts, id); status != http.StatusOK {
		t.Fatalf("first close failed")
	}
	if _, status := closeSpan(t, ts, id); status != http.StatusNotFound {
		t.Fatalf("a closed span must be gone, got %d", status)
	}
}
