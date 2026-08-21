package emulator_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// Fault injection, and the four bounds that keep it from lying (#26, #356).
//
// The tests below are each named in a comment beside the code they hold, which
// is what this repository means by a control: off by default, deterministic,
// refused when it names nothing, and — the one that matters most — incapable of
// earning any evidence axis.

// faultingPack is a stub that renders errors in a dialect of its own, the way a
// real pack does. `dialect` is the field a test reads to prove the body came
// from the pack and not from the core.
type faultingPack struct {
	stubPack
	statuses []int
}

func (f faultingPack) FaultStatuses() []int { return f.statuses }

func (f faultingPack) WriteFault(w http.ResponseWriter, _ *http.Request, status int) {
	emulator.WriteJSON(w, status, map[string]any{"dialect": f.name, "status": status})
}

// mutePack renders no dialect at all: it implements Pack and not Faulter, which
// is what a fourth provider looks like before anybody writes its error shapes.
func faultServer(t *testing.T, packs ...emulator.Pack) *httptest.Server {
	t.Helper()
	srv, err := emulator.NewServer(emulator.DefaultEnv(), packs...)
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func echoPack(name, prefix string, statuses []int) faultingPack {
	return faultingPack{
		stubPack: stubPack{name: name, routes: []emulator.Route{
			{Method: "GET", Path: prefix + "/things", Operation: name + "/v1/API.ListThings",
				Handler: func(w http.ResponseWriter, _ *http.Request) {
					emulator.WriteJSON(w, http.StatusOK, map[string]any{
						"things": []string{"one", "two"}, "provider": name,
					})
				}},
			{Method: "GET", Path: prefix + "/catalog", Operation: name + "/v1/API.GetCatalog",
				Handler: func(w http.ResponseWriter, _ *http.Request) {
					emulator.WriteJSON(w, http.StatusOK, map[string]any{"offer": "small"})
				}},
		}},
		statuses: statuses,
	}
}

func faults(t *testing.T, ts *httptest.Server, method, body string) (map[string]any, int) {
	t.Helper()
	return call(t, ts, method, "/_feint/faults", body, false)
}

func armed(t *testing.T, ts *httptest.Server) []any {
	t.Helper()
	body, status := faults(t, ts, http.MethodGet, "")
	if status != http.StatusOK {
		t.Fatalf("GET /_feint/faults: status %d, body %v", status, body)
	}
	rules, _ := body["faults"].([]any)
	return rules
}

// TestAServerStartsWithNoFaultArmed is bound 1: off by default. An emulator
// that refuses because somebody forgot is an emulator that gets uninstalled.
func TestAServerStartsWithNoFaultArmed(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))

	if rules := armed(t, ts); len(rules) != 0 {
		t.Errorf("a fresh emulator arms %d rules, want none", len(rules))
	}
	body, status := call(t, ts, http.MethodGet, "/stub/things", "", false)
	if status != http.StatusOK {
		t.Fatalf("a fresh emulator answered %d, want 200: %v", status, body)
	}
}

// TestAFaultFiresExactlyTimesTimes is bound 2: deterministic. "The first two
// calls, then the client recovers" is the whole scenario this feature exists
// for, and it has to be exact — a rule firing three times would fail a suite
// that asserts the client's own retry budget.
func TestAFaultFiresExactlyTimesTimes(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503,"times":2}]}`)

	for attempt, want := range []int{503, 503, 200} {
		body, status := call(t, ts, http.MethodGet, "/stub/things", "", false)
		if status != want {
			t.Fatalf("attempt %d answered %d, want %d: %v", attempt+1, status, want, body)
		}
	}

	rule := armed(t, ts)[0].(map[string]any)
	if hits, _ := rule["hits"].(float64); hits != 2 {
		t.Errorf("the rule reports %v hits, want 2: a script asserting \"it fired twice\" reads this", rule["hits"])
	}
	if spent, _ := rule["spent"].(bool); !spent {
		t.Error("the rule is not marked spent after firing its last time")
	}
}

// TestAFaultOnAnUnmountedOperationIsRefused is bound 3, and the reason it is
// refused when the rule is written rather than ignored when it would fire: a
// rule that never fires is indistinguishable, from outside, from a client that
// survived the fault. A suite would report a success nobody produced.
func TestAFaultOnAnUnmountedOperationIsRefused(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))

	// A delay and no status, deliberately: a status rule is refused a second
	// time further down, because an unmounted operation has no pack and no pack
	// renders a status. Falsifying the mount check alone showed that — the
	// mutation stayed green through the other refusal — and a guard whose
	// removal changes nothing observable is not a guard. A delay reaches no
	// pack, so this is the mount check and only the mount check.
	body, status := faults(t, ts, http.MethodPut,
		`{"faults":[{"operation":"stub/v1/API.ListThingz","delay":"1ms"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a rule on an unmounted operation answered %d, want 400: %v", status, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "ListThingz") {
		t.Errorf("the refusal does not name the operation: %v", body)
	}
	if rules := armed(t, ts); len(rules) != 0 {
		t.Errorf("a refused rule was armed anyway: %v", rules)
	}

	// And the same for a status rule, which is the form a suite writes.
	if body, status := faults(t, ts, http.MethodPut,
		`{"faults":[{"operation":"stub/v1/API.ListThingz","status":503}]}`); status != http.StatusBadRequest {
		t.Errorf("a status rule on an unmounted operation answered %d, want 400: %v", status, body)
	}
}

// TestAnInjectedAnswerDoesNotDriveItsOperation is half of bound 4. A route that
// only ever answered injected faults must read exactly as un-driven, or the
// coverage story stops being trustworthy — the risk #26 named as the one to
// close by design.
func TestAnInjectedAnswerDoesNotDriveItsOperation(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503}]}`)

	for range 3 {
		if _, status := call(t, ts, http.MethodGet, "/stub/things", "", false); status != 503 {
			t.Fatalf("the armed rule did not fire: status %d", status)
		}
	}

	view := conformanceView(t, ts)
	if n := view.Calls["stub/v1/API.ListThings"]; n != 0 {
		t.Errorf("an operation answered only by injected faults counts %d calls, want 0", n)
	}
	if view.Evidence["stub/v1/API.ListThings"].Driven {
		t.Error("an operation answered only by injected faults reads as driven")
	}
	if n := view.Injected["stub/v1/API.ListThings"]; n != 3 {
		t.Errorf("injected counts %d, want 3: the one counter an injected answer moves", n)
	}
	for _, op := range view.Untouched {
		if op == "stub/v1/API.ListThings" {
			return
		}
	}
	t.Error("an operation answered only by injected faults is missing from the backlog")
}

// TestAnInjectedRefusalEarnsNoNegativeEvidence is the other half of bound 4,
// and the one this whole lot would be worthless without: `negative` is the axis
// standing at 34 of 357, and a fault somebody armed must not be able to raise
// it. A span whose only refusals were injected is refused outright rather than
// closed on nothing, because proving nothing silently reads to a suite exactly
// like proving something.
func TestAnInjectedRefusalEarnsNoNegativeEvidence(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusForbidden}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":403}]}`)

	id := openSpan(t, ts, "negative")
	body, status := call(t, ts, http.MethodGet, "/stub/things", "", false)
	if status != http.StatusForbidden {
		t.Fatalf("the armed 403 did not fire: status %d, body %v", status, body)
	}
	closed, status := closeSpan(t, ts, id)
	if status != http.StatusConflict {
		t.Fatalf("a span closed on an injected refusal answered %d, want 409: %v", status, closed)
	}
	if msg, _ := closed["error"].(string); !strings.Contains(msg, "injected") {
		t.Errorf("the refusal does not say the 4xx was injected: %v", closed)
	}
	if conformanceView(t, ts).Evidence["stub/v1/API.ListThings"].Negative {
		t.Error("an injected 403 earned the negative axis: this feature would be proving itself")
	}
}

// And the control that keeps the test above from being vacuous: a real refusal,
// with no rule armed, still earns the axis. Without this, deleting the whole
// negative axis would pass.
func TestARealRefusalStillEarnsTheNegativeAxis(t *testing.T) {
	ts, _ := assertServer(t)

	id := openSpan(t, ts, "negative")
	if _, status := call(t, ts, http.MethodGet, "/stub/things/nope", "", false); status != http.StatusNotFound {
		t.Fatalf("the stub did not refuse: status %d", status)
	}
	if body, status := closeSpan(t, ts, id); status != http.StatusOK {
		t.Fatalf("a real refusal did not close the span: %d %v", status, body)
	}
	if !conformanceView(t, ts).Evidence["stub/v1/API.GetThing"].Negative {
		t.Error("a real 404 inside a negative span earned nothing")
	}
}

// TestAFaultDoesNotFireOnTheProbe: the probe's verdict is about the route's own
// answer, so a fault firing there would report an emulator defect that is not
// one — and `feint probe` stays usable while rules are armed.
func TestAFaultDoesNotFireOnTheProbe(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503}]}`)

	if body, status := call(t, ts, http.MethodGet, "/stub/things", "", true); status != http.StatusOK {
		t.Fatalf("a probe met the armed fault: status %d, body %v", status, body)
	}
	if rule := armed(t, ts)[0].(map[string]any); rule["hits"].(float64) != 0 {
		t.Errorf("a probe consumed a fault's budget: %v", rule)
	}
}

// The pack renders the failure, the core only decides there is one. A core that
// wrote the body itself would be inventing a format, which is the whole reason
// this seam exists — and the one thing a client never sees from its cloud.
func TestThePackRendersTheInjectedFailure(t *testing.T) {
	ts := faultServer(t,
		echoPack("alpha", "/alpha", []int{http.StatusServiceUnavailable}),
		echoPack("beta", "/beta", []int{http.StatusServiceUnavailable}))
	arm(t, ts, `{"faults":[
		{"operation":"alpha/v1/API.ListThings","status":503},
		{"operation":"beta/v1/API.ListThings","status":503}]}`)

	for _, p := range []string{"alpha", "beta"} {
		body, status := call(t, ts, http.MethodGet, "/"+p+"/things", "", false)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("%s answered %d, want 503", p, status)
		}
		if got, _ := body["dialect"].(string); got != p {
			t.Errorf("the %s 503 was rendered by %q: each pack answers in its own dialect", p, got)
		}
	}
}

// A status the pack does not render is refused when the rule is written, never
// answered with a body the core made up. That is rule 4 at the seam: the core
// decides when, the pack decides what it looks like, and where the pack cannot
// say, nobody may.
func TestAStatusThePackCannotRenderIsRefused(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))

	body, status := faults(t, ts, http.MethodPut,
		`{"faults":[{"operation":"stub/v1/API.ListThings","status":418}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an unrenderable status answered %d, want 400: %v", status, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "418") {
		t.Errorf("the refusal does not name the status: %v", body)
	}
}

// A pack that renders no dialect at all carries no status rule. A fourth
// provider arrives that way, and the honest answer is a refusal naming it
// rather than a body in nobody's dialect.
func TestAPackWithNoDialectCarriesNoStatusFault(t *testing.T) {
	mute := stubPack{name: "mute", routes: []emulator.Route{
		{Method: "GET", Path: "/mute/things", Operation: "mute/v1/API.ListThings", Handler: noop},
	}}
	ts := faultServer(t, mute)

	body, status := faults(t, ts, http.MethodPut,
		`{"faults":[{"operation":"mute/v1/API.ListThings","status":503}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a status rule on a dialect-less pack answered %d, want 400: %v", status, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "mute") {
		t.Errorf("the refusal does not name the pack: %v", body)
	}

	// A delay carries no body, so it is available on every pack. Refusing it
	// too would make the whole feature hostage to the error shapes.
	if body, status := faults(t, ts, http.MethodPut,
		`{"faults":[{"operation":"mute/v1/API.ListThings","delay":"1ms"}]}`); status != http.StatusOK {
		t.Errorf("a delay on a dialect-less pack answered %d, want 200: %v", status, body)
	}
}

// The scoped half of #356: one endpoint refuses while the rest answers. A switch
// that broke everything at once would test nothing interesting, because a client
// that fails wholesale is easy to get right.
func TestAFaultIsScopedToItsOperation(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusForbidden}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":403}]}`)

	if _, status := call(t, ts, http.MethodGet, "/stub/things", "", false); status != http.StatusForbidden {
		t.Fatalf("the targeted operation answered %d, want 403", status)
	}
	if _, status := call(t, ts, http.MethodGet, "/stub/catalog", "", false); status != http.StatusOK {
		t.Errorf("an untargeted operation answered %d, want 200: a fault that breaks everything tests nothing", status)
	}
}

// A truncated body: the handler's own answer, cut short. It is what an
// interrupted page looks like on the wire, and what a client's decoder has to
// survive.
func TestATruncatedAnswerIsCutShort(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))

	full, status := rawCall(t, ts, "/stub/things")
	if status != http.StatusOK {
		t.Fatalf("the whole answer: status %d", status)
	}
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","truncate_bytes":12}]}`)

	cut, status := rawCall(t, ts, "/stub/things")
	if status != http.StatusOK {
		t.Errorf("a truncated answer moved the status to %d: the handler's own status stands", status)
	}
	if len(cut) != 12 {
		t.Fatalf("the answer is %d bytes, want 12 (whole answer: %d)", len(cut), len(full))
	}
	if json.Valid(cut) {
		t.Error("the truncated answer still decodes: nothing was cut")
	}
}

// A delay alone is a slow but correct answer, and it is cancelled with the
// request rather than held to term: a client that gave up must not leave the
// emulator sleeping out a rule.
func TestADelayedAnswerIsStillTheHandlersOwn(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","delay":"120ms"}]}`)

	started := time.Now()
	body, status := call(t, ts, http.MethodGet, "/stub/things", "", false)
	elapsed := time.Since(started)
	if status != http.StatusOK {
		t.Fatalf("a delay changed the status to %d: %v", status, body)
	}
	if provider, _ := body["provider"].(string); provider != "stub" {
		t.Errorf("a delayed answer is not the handler's own: %v", body)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("the answer came back in %s, faster than the 120ms the rule asked for", elapsed)
	}
}

// Every injected answer says so on the wire. Decided out loud (#26): no client
// branches on an unknown response header, and the alternative is an operator
// unable to tell an injected 500 from an emulator defect.
func TestAnInjectedAnswerCarriesItsMarker(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503}]}`)

	res, err := ts.Client().Get(ts.URL + "/stub/things") //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if got := res.Header.Get(emulator.FaultHeader); got != "stub/v1/API.ListThings" {
		t.Errorf("%s is %q, want the operation the rule targeted", emulator.FaultHeader, got)
	}

	clean, err := ts.Client().Get(ts.URL + "/stub/catalog") //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clean.Body.Close() }()
	if got := clean.Header.Get(emulator.FaultHeader); got != "" {
		t.Errorf("an ordinary answer carries %s: %q", emulator.FaultHeader, got)
	}
}

// Clearing puts the emulator back where it starts, which is what a suite does
// when it is done and what an operator does when a run left something armed.
func TestClearingDisarmsEverything(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503}]}`)

	if body, status := faults(t, ts, http.MethodDelete, ""); status != http.StatusOK {
		t.Fatalf("DELETE answered %d: %v", status, body)
	}
	if rules := armed(t, ts); len(rules) != 0 {
		t.Errorf("%d rules survived the clear", len(rules))
	}
	if _, status := call(t, ts, http.MethodGet, "/stub/things", "", false); status != http.StatusOK {
		t.Errorf("the operation still answers %d after the clear", status)
	}
}

// A rule set is replaced whole rather than appended to, so a suite that PUTs a
// committed file gets the same emulator whatever ran before it. That is the
// replayability #356 asked for, and the reason two rules on one operation are
// refused: which one fires must never be a question.
func TestARuleSetIsReplacedWhole(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusForbidden, http.StatusServiceUnavailable}))
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":403}]}`)
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.GetCatalog","status":503}]}`)

	if rules := armed(t, ts); len(rules) != 1 {
		t.Fatalf("%d rules armed after a replacing PUT, want 1", len(rules))
	}
	if _, status := call(t, ts, http.MethodGet, "/stub/things", "", false); status != http.StatusOK {
		t.Errorf("the replaced rule still fires: status %d", status)
	}

	body, status := faults(t, ts, http.MethodPut, `{"faults":[
		{"operation":"stub/v1/API.ListThings","status":403},
		{"operation":"stub/v1/API.ListThings","status":503}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("two rules on one operation answered %d, want 400: %v", status, body)
	}
}

// A rule that asks for nothing is refused: it would arm, list, report zero hits
// and mean nothing at all.
func TestARuleMustAskForSomething(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{http.StatusServiceUnavailable}))

	for _, rule := range []string{
		`{"operation":"stub/v1/API.ListThings"}`,
		`{"operation":"stub/v1/API.ListThings","status":503,"truncate_bytes":10}`,
		`{"operation":"stub/v1/API.ListThings","delay":"nonsense"}`,
		`{"operation":"stub/v1/API.ListThings","delay":"1h"}`,
		`{"operation":"stub/v1/API.ListThings","status":503,"times":-1}`,
		`{"status":503}`,
	} {
		if body, status := faults(t, ts, http.MethodPut, `{"faults":[`+rule+`]}`); status != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400: %v", rule, status, body)
		}
	}
}

// arm PUTs a rule set and fails the test when the emulator refuses it.
func arm(t *testing.T, ts *httptest.Server, body string) {
	t.Helper()
	answer, status := faults(t, ts, http.MethodPut, body)
	if status != http.StatusOK {
		t.Fatalf("arm %s: status %d, body %v", body, status, answer)
	}
}

// rawCall returns the answer's bytes rather than its decoded form, which is the
// only way to see a body that no longer decodes.
func rawCall(t *testing.T, ts *httptest.Server, path string) ([]byte, int) {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + path) //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var body strings.Builder
	if _, err := io.Copy(&body, res.Body); err != nil {
		t.Fatal(err)
	}
	return []byte(body.String()), res.StatusCode
}

// conformanceView decodes /_feint/conformance, which is where the evidence
// axes are published.
func conformanceView(t *testing.T, ts *httptest.Server) emulator.ConformanceView {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + "/_feint/conformance") //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var view emulator.ConformanceView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatalf("decode the conformance view: %v", err)
	}
	return view
}
