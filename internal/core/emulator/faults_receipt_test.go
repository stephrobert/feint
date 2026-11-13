package emulator_test

// The receipt's three guarantees, each one written after it was found broken by
// a review of #851 and each one reproduced here before it was fixed.
//
// The line is reserved under the lock that produces the rank, so the receipt is
// in arrival order and a firing whose campaign was replaced mid-flight is
// dropped rather than attributed to the campaign that replaced it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// slowPack: one operation whose handler sleeps when asked, so two calls to the
// SAME rule can finish in an order opposite to their arrival.
func auditPack() stubPack {
	return stubPack{name: "stub", routes: []emulator.Route{
		{Method: "GET", Path: "/stub/things", Operation: "stub/v1/API.ListThings",
			Handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("slow") == "1" {
					time.Sleep(400 * time.Millisecond)
				}
				emulator.WriteJSON(w, http.StatusOK, map[string]any{"things": []string{"one"}})
			}},
	}}
}

func firedLines(t *testing.T, ts *httptest.Server) []any {
	t.Helper()
	body, status := faults(t, ts, http.MethodGet, "")
	if status != http.StatusOK {
		t.Fatalf("GET /_feint/faults: %d %v", status, body)
	}
	lines, _ := body["fired"].([]any)
	return lines
}

// AUDIT 1 — a firing in flight when the set is replaced lands in the NEW
// campaign's receipt, which faultSet.replace claims cannot happen.
func TestAFiringInFlightDoesNotLandInTheNextCampaign(t *testing.T) {
	ts := faultServer(t, faultingPack{stubPack: auditPack(), statuses: []int{503}})
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503,"delay":"400ms"}]}`)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = call(t, ts, http.MethodGet, "/stub/things", "", false)
	}()
	time.Sleep(100 * time.Millisecond)
	// `feint fault clear` while the delayed answer is still being served.
	if body, status := faults(t, ts, http.MethodDelete, ""); status != http.StatusOK {
		t.Fatalf("clear: %d %v", status, body)
	}
	if got := firedLines(t, ts); len(got) != 0 {
		t.Fatalf("the receipt was not empty right after the clear: %v", got)
	}
	wg.Wait()

	if got := firedLines(t, ts); len(got) != 0 {
		t.Errorf("after a clear, the receipt holds %d line(s) from the previous campaign: %v", len(got), got)
	}
}

// AUDIT 2 — the receipt is in completion order, not arrival order, for two
// calls of the same rule.
func TestTheReceiptIsInArrivalOrder(t *testing.T) {
	ts := faultServer(t, faultingPack{stubPack: auditPack(), statuses: []int{503}})
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","truncate_bytes":4}]}`)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = call(t, ts, http.MethodGet, "/stub/things?slow=1", "", false)
	}()
	time.Sleep(150 * time.Millisecond) // the slow call has taken rank 1 by now
	_, _ = call(t, ts, http.MethodGet, "/stub/things", "", false)
	wg.Wait()

	got := firedLines(t, ts)
	if len(got) != 2 {
		t.Fatalf("the receipt holds %d line(s), want 2: %v", len(got), got)
	}
	first, _ := got[0].(map[string]any)
	rank, _ := first["rank"].(float64)
	if rank != 1 {
		t.Errorf("the first line of a receipt documented \"oldest first\" has rank %v: %v", rank, got)
	}
}

// AUDIT 3 — the read-modify-write `feint fault add` performs resets every
// rule's call rank, so an `nth` campaign restarts from zero.
func TestArmingAgainRestartsEveryCampaign(t *testing.T) {
	ts := faultServer(t, faultingPack{stubPack: auditPack(), statuses: []int{503}})
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503,"nth":3}]}`)

	for range 2 {
		if _, status := call(t, ts, http.MethodGet, "/stub/things", "", false); status != 200 {
			t.Fatalf("a call before the rank answered %d", status)
		}
	}
	rule := armed(t, ts)[0].(map[string]any)
	if calls, _ := rule["calls"].(float64); calls != 2 {
		t.Fatalf("the rule saw %v call(s), want 2", rule["calls"])
	}

	// What `feint fault add <another op>` sends back: the armed set, re-PUT.
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503,"nth":3}]}`)
	rule = armed(t, ts)[0].(map[string]any)
	if calls, _ := rule["calls"].(float64); calls != 0 {
		t.Errorf("after a re-PUT the rule reads %v call(s), want 0. This test holds the documented "+
			"behaviour, not a wish: a PUT rebuilds the rules, so a campaign restarts. If that "+
			"changes, the section of docs/limits.md that states it changes with this line",
			rule["calls"])
	}
	// The third call of the *original* campaign is the first of the new one,
	// so the rule waits for two more. An operator who armed a second rule
	// mid-campaign gets this, and the limits page says so.
	_, status := call(t, ts, http.MethodGet, "/stub/things", "", false)
	if status != 200 {
		t.Errorf("the call after a re-PUT answered %d, want 200: the restarted campaign is at rank 1, "+
			"not rank 3", status)
	}
}

// A client that walks away during a `delay` rule: the line says abandoned
// rather than leaving a reader with `status: 0`, which is not an HTTP status.
func TestAnAbandonedCallIsRecordedAsAbandoned(t *testing.T) {
	ts := faultServer(t, faultingPack{stubPack: auditPack(), statuses: []int{503}})
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503,"delay":"2s"}]}`)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/stub/things", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Client().Do(req) //nolint:bodyclose // the call is cancelled; there is no body
	if err == nil {
		_ = res.Body.Close()
		t.Fatal("the call was not cancelled, so this test proves nothing")
	}
	time.Sleep(100 * time.Millisecond)
	got := firedLines(t, ts)
	if len(got) != 1 {
		t.Fatalf("the receipt holds %d line(s), want 1: %v", len(got), got)
	}
	line, _ := got[0].(map[string]any)
	if abandoned, _ := line["abandoned"].(bool); !abandoned {
		t.Errorf("the receipt does not mark the line abandoned, so a reader meets `status: 0` and "+
			"has to guess what it means: %v", got)
	}
	if status, _ := line["status"].(float64); status != 0 {
		t.Errorf("an abandoned call recorded status %v; nothing was answered", line["status"])
	}
}

// The epoch guard, and the only shape that measures it: the new campaign must
// have reserved the SAME slot, or nothing can be overwritten and the slot bound
// alone explains the empty receipt.
//
// Found by a falsification pass: the first test written for this guard passed
// with the guard removed.
func TestAStaleFiringDoesNotOverwriteTheNewCampaignsLine(t *testing.T) {
	ts := faultServer(t, faultingPack{stubPack: auditPack(), statuses: []int{503}})
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503,"delay":"400ms"}]}`)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = call(t, ts, http.MethodGet, "/stub/things", "", false)
	}()
	time.Sleep(100 * time.Millisecond)
	// A new campaign while the 503 is still being delayed.
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","truncate_bytes":4}]}`)
	// One call of the new campaign: it takes slot 0 of the new receipt.
	if _, status := call(t, ts, http.MethodGet, "/stub/things", "", false); status != 200 {
		t.Fatalf("the call of the new campaign answered %d, want 200", status)
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond)

	got := firedLines(t, ts)
	if len(got) != 1 {
		t.Fatalf("the receipt holds %d line(s), want 1: %v", len(got), got)
	}
	line, _ := got[0].(map[string]any)
	if status, _ := line["status"].(float64); status != 200 {
		t.Errorf("the only line of this campaign reads status %v: a firing from the previous "+
			"campaign wrote into the slot this one reserved: %v", line["status"], got)
	}
}

// The counter that protects the evidence is published at arrival, like the
// receipt line, and not when the answer finishes.
//
// Counted at the end, a rule with a long `delay` left `/_feint/conformance`
// saying `injected` was empty while a fault was being injected into the run —
// and `tools/conformance/score.sh` reads exactly that map to refuse a pass
// carrying an injected answer. Meanwhile `/_feint/faults` already showed the
// reserved line, so the two published views contradicted each other.
//
// Found by a security review of this lot, which named this measurement rather
// than running it.
func TestTheInjectedCounterIsVisibleWhileTheFaultIsStillInFlight(t *testing.T) {
	ts := faultServer(t, faultingPack{stubPack: auditPack(), statuses: []int{503}})
	arm(t, ts, `{"faults":[{"operation":"stub/v1/API.ListThings","status":503,"delay":"400ms"}]}`)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = call(t, ts, http.MethodGet, "/stub/things", "", false)
	}()

	// Halfway through the delay: the answer has not been written yet.
	time.Sleep(150 * time.Millisecond)
	view := conformanceView(t, ts)
	if view.Injected["stub/v1/API.ListThings"] == 0 {
		t.Errorf("a fault is being injected and /_feint/conformance reports none: a score read in "+
			"this window would call the run clean. injected=%v", view.Injected)
	}

	wg.Wait()
}
