package emulator_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
)

// The probe axis is a verdict, never an arrival (#156). Every test here holds
// one edge of that line: a reached route whose answer validated against
// nothing publishes no axis, a refusal earns "refusal" only when it validates
// against the declared error shape, and a paged operation earns "response"
// only through the call that carried its page size and stayed within it.

// probeContract declares one plain operation, one paged one, and the error
// shape a refusal is held to.
const probeContract = `{
  "provider": "stub",
  "errorSchema": "StubError",
  "operations": {
    "OpPlain": {"method": "GET", "path": "/stub/plain", "response": "PlainView"},
    "OpList": {
      "method": "GET", "path": "/stub/list", "response": "ListView",
      "query": {"per_page": {"type": "integer"}}
    }
  },
  "schemas": {
    "PlainView": {"closed": true, "properties": {"ok": {"type": "boolean"}}},
    "ListView": {"closed": true, "properties": {"items": {"type": "array", "items": {"type": "string"}}}},
    "StubError": {"closed": false, "required": ["message"], "properties": {"message": {"type": "string"}}}
  }
}`

// probeServer mounts three routes against probeContract: a plain one and a
// paged one answering what each handler is told, and /stub/plain's handler is
// the one each test swaps.
func probeServer(t *testing.T, plain, list http.HandlerFunc) *httptest.Server {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(probeContract))
	if err != nil {
		t.Fatalf("read the stub contract: %v", err)
	}
	env := emulator.DefaultEnv()
	env.Contracts = map[string]*contract.Doc{"stub": doc}

	pack := stubPack{name: "stub", routes: []emulator.Route{
		{Method: "GET", Path: "/stub/plain", Operation: "stub/v1/API.OpPlain", Handler: plain},
		{Method: "GET", Path: "/stub/list", Operation: "stub/v1/API.OpList", Handler: list},
	}}
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("mount the stub pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func answer(status int, contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func okList(items string) http.HandlerFunc {
	return answer(http.StatusOK, "application/json", `{"items": `+items+`}`)
}

// TestProbedIsAVerdictNotAnArrival is the emulator half of #156: a synthetic
// request that reached the route but whose only answer was a text/plain 404 —
// the exact shape #74 measured — must publish no probe axis, while the arrival
// still shows in the Probes map. Reverting the axis to the arrival counter
// fails this.
func TestProbedIsAVerdictNotAnArrival(t *testing.T) {
	ts := probeServer(t,
		answer(http.StatusNotFound, "text/plain", "404 page not found"),
		okList(`["a"]`))

	driveRoute(t, ts, "/stub/plain", true)

	view := viewOf(t, ts)
	if got := view.Evidence["stub/v1/API.OpPlain"].Probed; got != emulator.ProbeNone {
		t.Errorf("an unread refusal must publish no probe axis, got %q", got)
	}
	if view.Probes["stub/v1/API.OpPlain"] == 0 {
		t.Error("the arrival still happened and the Probes map must say so")
	}
	if view.Probed != 0 {
		t.Errorf("the probed count is a verdict too, got %d", view.Probed)
	}
}

func TestAValidatedRefusalIsPublishedAsRefusalNeverAsResponse(t *testing.T) {
	ts := probeServer(t,
		answer(http.StatusNotFound, "application/json", `{"message": "no such thing"}`),
		okList(`["a"]`))

	driveRoute(t, ts, "/stub/plain", true)

	if got := viewOf(t, ts).Evidence["stub/v1/API.OpPlain"].Probed; got != emulator.ProbeRefusal {
		t.Errorf("a refusal validated against the declared error shape reads refusal, got %q", got)
	}
}

func TestARefusalOffTheDeclaredShapeEarnsNothing(t *testing.T) {
	// JSON, but missing the one field the error shape requires: the bare {}
	// the issue names.
	ts := probeServer(t,
		answer(http.StatusNotFound, "application/json", `{}`),
		okList(`["a"]`))

	driveRoute(t, ts, "/stub/plain", true)

	if got := viewOf(t, ts).Evidence["stub/v1/API.OpPlain"].Probed; got != emulator.ProbeNone {
		t.Errorf("a refusal off the declared shape must publish no probe axis, got %q", got)
	}
}

func TestAValidatedSuccessIsPublishedAsResponse(t *testing.T) {
	ts := probeServer(t,
		answer(http.StatusOK, "application/json", `{"ok": true}`),
		okList(`["a"]`))

	driveRoute(t, ts, "/stub/plain", true)

	view := viewOf(t, ts)
	if got := view.Evidence["stub/v1/API.OpPlain"].Probed; got != emulator.ProbeResponse {
		t.Errorf("a validated success reads response, got %q", got)
	}
	if view.Probed != 1 {
		t.Errorf("one undriven route was validated by the probe, the count must say 1, got %d", view.Probed)
	}
}

// TestPagedOperationNeedsItsPageSizeExercised holds the response verdict to
// both halves of what the probe claims: the schema, and the page-size
// parameter the contract declares. A clean answer to a call that never carried
// per_page proves the schema and not the parameter, and must not be published
// as if it had.
func TestPagedOperationNeedsItsPageSizeExercised(t *testing.T) {
	ts := probeServer(t,
		answer(http.StatusOK, "application/json", `{"ok": true}`),
		okList(`["a"]`))

	driveRoute(t, ts, "/stub/list", true)
	if got := viewOf(t, ts).Evidence["stub/v1/API.OpList"].Probed; got != emulator.ProbeNone {
		t.Errorf("a paged operation without its page size exercised earns nothing, got %q", got)
	}

	driveRoute(t, ts, "/stub/list?per_page=1", true)
	if got := viewOf(t, ts).Evidence["stub/v1/API.OpList"].Probed; got != emulator.ProbeResponse {
		t.Errorf("the paged call within its bound earns response, got %q", got)
	}
}

// TestAnAnswerOverThePageSizeBoundEarnsNothing is the emulator-side twin of
// the probe's own bound check: two items where one was asked is per_page
// ignored, whatever the schema says.
func TestAnAnswerOverThePageSizeBoundEarnsNothing(t *testing.T) {
	ts := probeServer(t,
		answer(http.StatusOK, "application/json", `{"ok": true}`),
		okList(`["a", "b"]`))

	driveRoute(t, ts, "/stub/list?per_page=1", true)

	if got := viewOf(t, ts).Evidence["stub/v1/API.OpList"].Probed; got != emulator.ProbeNone {
		t.Errorf("an answer over the bound it was asked earns nothing, got %q", got)
	}
}
