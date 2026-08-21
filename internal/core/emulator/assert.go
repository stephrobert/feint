package emulator

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/store"
)

// The assertion span: how a suite says what an assertion proves, without ever
// naming an operation.
//
// The behaviour and negative axes of the evidence record (#123) need a signal
// at the assertion level, and none of the official clients can carry one: scw,
// terraform, oapi-cli and exo offer no way to attach a header to the requests
// they compose, so the request-level channel is closed. What a suite *can* do
// is bracket an assertion: open a span, run the real client, close the span.
// Everything in between is what the emulator observed on its own — the
// exchanges the observer already logs, and the store touches the packs already
// make — so the operations a span marks are resolved from measured traffic,
// never from a list somebody maintains.
//
// The claim is verified, not trusted. Closing a span whose axis the
// observation does not support is refused with a 4xx, which the suite treats
// as a failure: a suite cannot lie its way onto either axis, and a span left
// open marks nothing at all. That is what keeps this from being the
// comment-that-is-not-a-control published over HTTP:
//
//   - a `behaviour` span must contain a full lifecycle the store itself saw —
//     some resource created and then deleted between open and close. The
//     operations marked are the ones whose handlers touched such a resource
//     (or listed its kind), which keeps the catalogue reads a CLI performs on
//     the way to a create from earning an axis they did not prove.
//   - a `negative` span must contain at least one client-driven exchange the
//     emulator answered with a 4xx. The operations marked are the refused
//     ones.
//
// # What counts as a demanded refusal, and what never will
//
// The axis is written here, where it is computed, because it is the one axis
// somebody could raise without measuring anything (#390).
//
// A refusal counts when all four hold:
//
//  1. **a client asked for it.** The request came in over the wire from
//     something outside this process — one of the official CLIs, a Terraform
//     provider, or `feint replay` reissuing a request one of them made against
//     the provider's real cloud and that the cloud refused
//     (tools/conformance/refusals.sh). A synthetic call the contract-driven
//     probe composed is excluded by [spanExchange.synthetic]: the probe builds
//     its request from a schema, so a 4xx it meets says the schema and the
//     handler disagree, never that a client was refused.
//  2. **this emulator decided it.** The status came out of a handler, from the
//     store it read and the request it parsed.
//  3. **a suite said so first.** The span was opened before the traffic, so the
//     claim is an assertion rather than a reading of whatever happened to fail.
//  4. **it is a 4xx.** A 5xx is this emulator breaking, not refusing, and
//     counting it would turn a defect into evidence.
//
// **An injected refusal never counts, and that bound is what the number is
// worth.** `PUT /_feint/faults` can make any operation answer 403; the answer
// is real on the wire and unreal as evidence, because nobody's rule about
// nobody's account produced it — somebody armed it here. Point 2 is the one it
// fails. If it counted, this axis could be moved to any figure by writing a
// fixture, which is precisely the failure #26 refused and #390 was written to
// avoid: a number to reach is not a measurement. [assertSpan.refusedOperations]
// carries the check, and it refuses the whole span rather than narrowing it,
// because proving nothing reads to a suite exactly like proving something.
//
// What the axis does *not* say, stated so nobody reads more into it: one
// demanded refusal earns it, so an operation marked here has had one of its
// refusals observed and says nothing about the others it owes.
//
// Attribution of a store touch to an operation goes through the in-flight set:
// a touch is attributed only while exactly one non-probe request is being
// handled. Sequential suites — every span-emitting block is one — always
// satisfy that; a parallel client like terraform loses attribution for the
// overlapping windows rather than being guessed about, so the axis can only
// under-claim, never over-claim.
//
// TestABehaviourSpanNeedsAnObservedLifecycle and
// TestANegativeSpanNeedsARefusal fail without the verification;
// TestABehaviourSpanMarksTheLifecycleAndNotTheCatalogue fails without the
// touch-based attribution.

// The two axes a span can claim to prove.
const (
	ProvesBehaviour = "behaviour"
	ProvesNegative  = "negative"
)

// Bounds. A span is meant to bracket one assertion block of a suite; these are
// far above any real block and exist so an emulator left running cannot be
// grown without end through open spans.
const (
	maxOpenSpans    = 32
	maxSpanEntries  = 8192
	spanOverflowMsg = "the span overflowed: it recorded more traffic than one assertion block plausibly produces"
)

// assertSpan is one open bracket.
type assertSpan struct {
	proves string
	// exchanges are the client-visible lines: one per answered request while
	// the span was open.
	exchanges []spanExchange
	// touches are the store's own lines: one per resource the handlers
	// touched while the span was open.
	touches    []spanTouch
	overflowed bool
}

type spanExchange struct {
	operation string
	status    int
	synthetic bool
	// injected marks an answer the fault injector produced rather than the
	// handler (faults.go). It is kept here, on the line itself, because the
	// refusal it carries is real on the wire and unreal as evidence: a client
	// did receive a 403, and the emulator did not decide it — somebody armed
	// it. See refusedOperations.
	injected bool
}

type spanTouch struct {
	// operation is the one non-probe request in flight when the touch
	// happened, or "" when attribution was ambiguous.
	operation string
	action    string
	provider  string
	kind      string
	id        string
}

// flightEntry is one request currently being handled.
type flightEntry struct {
	operation string
	synthetic bool
}

func (o *observer) beginFlight(operation string, synthetic bool) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.flightSeq++
	o.flight[o.flightSeq] = flightEntry{operation: operation, synthetic: synthetic}
	return o.flightSeq
}

func (o *observer) endFlight(token int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.flight, token)
}

// soleClientFlightLocked names the operation of the only non-probe request in
// flight, or "" when there is none or more than one. Callers hold o.mu.
func (o *observer) soleClientFlightLocked() string {
	found := ""
	for _, f := range o.flight {
		if f.synthetic {
			continue
		}
		if found != "" {
			return ""
		}
		found = f.operation
	}
	return found
}

// spanExchange offers one answered request to every open span.
func (o *observer) spanExchange(operation string, status int, synthetic bool) {
	o.offerExchange(spanExchange{operation: operation, status: status, synthetic: synthetic})
}

// spanExchangeInjected offers one answer the fault injector produced. Callers
// hold no lock.
func (o *observer) spanExchangeInjected(operation string, status int) {
	o.offerExchange(spanExchange{operation: operation, status: status, injected: true})
}

func (o *observer) offerExchange(e spanExchange) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, sp := range o.spans {
		if len(sp.exchanges)+len(sp.touches) >= maxSpanEntries {
			sp.overflowed = true
			continue
		}
		sp.exchanges = append(sp.exchanges, e)
	}
}

// storeTouch is the store's observer (store.Observe): it attributes the touch
// and offers it to every open span. It runs on the request path, outside the
// store's lock, and must not call back into the store.
func (o *observer) storeTouch(ev store.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.spans) == 0 {
		return
	}
	touch := spanTouch{
		operation: o.soleClientFlightLocked(),
		action:    ev.Action,
		provider:  ev.Provider,
		kind:      ev.Kind,
		id:        ev.ID,
	}
	for _, sp := range o.spans {
		if len(sp.exchanges)+len(sp.touches) >= maxSpanEntries {
			sp.overflowed = true
			continue
		}
		sp.touches = append(sp.touches, touch)
	}
}

// close computes what a span proved, or explains why it proved nothing. The
// error is the emulator refusing the claim, and the suite must treat it as a
// failure of its own.
func (sp *assertSpan) close() ([]string, error) {
	if sp.overflowed {
		return nil, fmt.Errorf("%s", spanOverflowMsg)
	}
	switch sp.proves {
	case ProvesNegative:
		return sp.refusedOperations()
	case ProvesBehaviour:
		return sp.lifecycleOperations()
	}
	// Unreachable through the handler, which validates the axis on open.
	return nil, fmt.Errorf("unknown axis %q", sp.proves)
}

func (sp *assertSpan) refusedOperations() ([]string, error) {
	ops := map[string]bool{}
	injectedOnly := false
	for _, e := range sp.exchanges {
		if e.synthetic || e.status < 400 || e.status >= 500 {
			continue
		}
		// An injected refusal is staged, not proven. The emulator did not
		// decide to refuse this call — somebody armed a rule saying it should —
		// so crediting the negative axis with it would let this repository
		// raise its own weakest number by writing a fixture. That is the bound
		// #26 and #356 both state, and it lives here because here is where a
		// refusal turns into evidence.
		//
		// The span is not merely narrowed: a span whose *only* refusals were
		// injected is refused outright, naming why, because silently proving
		// nothing reads to a suite exactly like proving something.
		// TestAnInjectedRefusalEarnsNoNegativeEvidence fails without this.
		if e.injected {
			injectedOnly = true
			continue
		}
		ops[e.operation] = true
	}
	if len(ops) == 0 {
		if injectedOnly {
			return nil, fmt.Errorf("the span demanded a refusal and every 4xx inside it was injected: " +
				"a fault somebody armed proves what the client does, never what this emulator refuses")
		}
		return nil, fmt.Errorf("the span demanded a refusal and the emulator answered no client with a 4xx inside it")
	}
	return sortedOps(ops), nil
}

func (sp *assertSpan) lifecycleOperations() ([]string, error) {
	resourceKey := func(t spanTouch) string { return t.provider + "|" + t.kind + "|" + t.id }
	created := map[string]bool{}
	completed := map[string]bool{}
	for _, t := range sp.touches {
		switch t.action {
		case store.EventCreated:
			created[resourceKey(t)] = true
		case store.EventDeleted:
			// A lifecycle is a creation this span saw, later undone. A delete
			// of something created before the span is cleanup, not a cycle.
			if created[resourceKey(t)] {
				completed[resourceKey(t)] = true
			}
		}
	}
	if len(completed) == 0 {
		return nil, fmt.Errorf("the span declared a lifecycle and the store observed no resource created and then destroyed inside it")
	}

	// The kinds whose listing counts as reading the lifecycle back.
	kinds := map[string]bool{}
	for k := range completed {
		provider, kind, _ := splitResourceKey(k)
		kinds[provider+"|"+kind] = true
	}
	ops := map[string]bool{}
	for _, t := range sp.touches {
		if t.operation == "" {
			continue
		}
		if t.action == store.EventListed {
			if kinds[t.provider+"|"+t.kind] {
				ops[t.operation] = true
			}
			continue
		}
		if completed[resourceKey(t)] {
			ops[t.operation] = true
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("a lifecycle was observed but no operation could be attributed to it")
	}
	return sortedOps(ops), nil
}

func splitResourceKey(k string) (provider, kind, id string) {
	parts := strings.SplitN(k, "|", 3)
	return parts[0], parts[1], parts[2]
}

func sortedOps(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for op := range set {
		out = append(out, op)
	}
	sort.Strings(out)
	return out
}

// handleAssertOpen opens a span: POST /_feint/assert {"proves": "behaviour"}.
func (s *Server) handleAssertOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proves string `json:"proves"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Proves != ProvesBehaviour && req.Proves != ProvesNegative {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("proves must be %q or %q, got %q", ProvesBehaviour, ProvesNegative, req.Proves),
		})
		return
	}

	o := s.observer
	o.mu.Lock()
	if len(o.spans) >= maxOpenSpans {
		o.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("%d spans are already open; a suite that opens without closing is broken", maxOpenSpans),
		})
		return
	}
	newID := s.env.NewID
	if newID == nil {
		newID = NewUUID
	}
	id := newID()
	o.spans[id] = &assertSpan{proves: req.Proves}
	o.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "proves": req.Proves})
}

// handleAssertClose closes a span: POST /_feint/assert/{id}. A claim the
// observation does not support is refused, and the suite must fail on it.
func (s *Server) handleAssertClose(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	o := s.observer
	o.mu.Lock()
	sp, known := o.spans[id]
	if !known {
		o.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such span; it was never opened, or already closed"})
		return
	}
	delete(o.spans, id)
	ops, err := sp.close()
	if err == nil {
		mark := o.behaviour
		if sp.proves == ProvesNegative {
			mark = o.negative
		}
		for _, op := range ops {
			mark[op] = true
		}
	}
	o.mu.Unlock()

	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"proves": sp.proves, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proves": sp.proves, "operations": ops})
}
