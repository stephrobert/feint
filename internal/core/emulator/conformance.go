package emulator

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/trace"
)

// Two numbers about a route, not one.
//
// The coverage report says how much of the upstream surface is implemented. It
// cannot say how much of that is ever exercised by a real client, and the answer
// has been nobody's business so far: 55 routes are mounted and the conformance
// suites touch a fraction of them. Microcks makes the same distinction and names
// it — a conformance index, meaning what the contract could cover, and a
// conformance score, meaning what the last run actually did.
//
// So every route counts its calls, and /_feint/conformance reports served
// against exercised. It costs one atomic increment per request.
//
// The second half is the contract check. With a provider's API description
// loaded, every response is validated against the schema its operation declares,
// and a violation is logged where a human will see it. That turns "did anyone
// invent a field" from a review question into a property of the run, over
// exactly the traffic a real client produced rather than the traffic a test
// author thought of.

// observer records what a run did, and checks it while it happens.
type observer struct {
	mu sync.Mutex
	// calls counts requests a real client made, per route operation. This is the
	// only counter the backlog is computed from.
	calls map[string]int
	// probed counts the synthetic requests the contract-driven probe made.
	//
	// Separate on purpose, and never merged. A probe proves the protocol — the
	// route answers, and its answer matches the provider's own schema — and
	// proves nothing about behaviour: whether the address it published answers,
	// whether the firewall filters. Counting the two together would make the
	// score go up without anything being proven, which is the exact defect the
	// emulators this project measures itself against have.
	//
	// So a probed route stays in the untouched list until a real client drives
	// it. The probe never removes work from the backlog; it only refuses to let
	// a route answer nonsense unnoticed.
	probed map[string]int
	// probeResponse marks the operations where a synthetic call's success was
	// validated clean against the operation's own response schema — and, when
	// the contract declares a page-size parameter, where one such call carried
	// it and the answer stayed within the bound it asked.
	//
	// This is a verdict, where probed above is an arrival. The published probe
	// axis reads only the verdict maps: arrival was what let an operation whose
	// only synthetic answer was an unread text/plain 404 publish probed: true
	// (#156). Computed here rather than trusted from the client, because
	// anything can send the probe header; the same doctrine as assert.go.
	// TestProbedIsAVerdictNotAnArrival fails if the axis reads arrival again.
	probeResponse map[string]bool
	// probeRefusal marks the operations where a synthetic call was refused and
	// the refusal validated clean against the provider's declared error shape.
	// Kept apart from probeResponse and never merged into it: "it refuses
	// properly" and "its success answer holds" are different facts, and the
	// axis publishes which one it has.
	probeRefusal map[string]bool
	// violations collects contract failures, first occurrence per operation:
	// one bad field repeated across a hundred calls is one defect.
	violations map[string]contract.Violations
	// checked counts the responses actually validated against a contract, per
	// operation. It exists because its absence was a half-truth: without it,
	// "no violation" could not tell "checked and conformant" from "never
	// checked", and the evidence record below would have promoted silence to
	// proof (#123).
	checked map[string]int
	// unread collects the fields clients sent that no handler declared, per
	// operation. Deduplicated, because a client repeating a call repeats the
	// field and one defect must read as one.
	unread map[string]map[string]bool
	// served accumulates, per operation, the union of field paths its decoded
	// 2xx answers carried across the run, each with the most container-like
	// type seen there. The other side of the omission check (#88): what the
	// contract declares and this union never shows is a field the emulator
	// forgot, or a decision a pack must argue. See omissions.go.
	served map[string]map[string]string
	// contracts maps a provider to its API description, when one was loaded.
	contracts map[string]*contract.Doc
	// stream is where each answered request is published, in order. The counters
	// above say how many; the stream says what happened, once, with its time and
	// its status.
	stream *stream
	// flight is the set of requests currently being handled, each with the
	// identity of its handler goroutine, so a store touch is attributed to the
	// request that actually made it rather than to whatever else was in flight
	// beside it. See assert.go and goroutine.go.
	flight    map[int64]flightEntry
	flightSeq int64
	// spans are the assertion spans a suite has opened and not yet closed, and
	// spansOpen is len(spans) readable without the lock: the attribution above
	// costs a stack walk, and nothing outside a measurement should pay it.
	// Written under mu, beside every write to spans.
	spans     map[string]*assertSpan
	spansOpen atomic.Int64
	// behaviour and negative record, per operation, the two axes only a closed
	// span can earn. See assert.go for what each requires.
	behaviour map[string]bool
	negative  map[string]bool
	// injected counts, per operation, the answers the fault injector produced
	// instead of the handler's own (faults.go). It is the counter that keeps
	// this feature from manufacturing its own success: none of the maps above
	// moves for an injected answer, and this one is published beside them so a
	// reader can see that a route which looks untouched was in fact refused on
	// purpose. Empty on every run where nobody armed a rule, which is every run
	// by default.
	injected map[string]int
	// faults is the armed rule set, shared with the Server that owns it.
	faults *faultSet
	// faulters maps a provider to the pack that renders its error dialect. The
	// core decides when a call fails; this is how it asks the pack what that
	// failure looks like, and it holds no provider name of its own.
	faulters map[string]Faulter
}

func newObserver(contracts map[string]*contract.Doc, events *stream, faults *faultSet, faulters map[string]Faulter) *observer {
	return &observer{
		stream:        events,
		injected:      map[string]int{},
		faults:        faults,
		faulters:      faulters,
		calls:         map[string]int{},
		probed:        map[string]int{},
		probeResponse: map[string]bool{},
		probeRefusal:  map[string]bool{},
		violations:    map[string]contract.Violations{},
		checked:       map[string]int{},
		unread:        map[string]map[string]bool{},
		served:        map[string]map[string]string{},
		contracts:     contracts,
		flight:        map[int64]flightEntry{},
		spans:         map[string]*assertSpan{},
		behaviour:     map[string]bool{},
		negative:      map[string]bool{},
	}
}

// wrap instruments one route: it counts the call, records it on the log, and
// validates the response against the provider's contract when there is one.
func (o *observer) wrap(provider string, r Route) http.HandlerFunc {
	doc := o.contracts[provider]

	return func(w http.ResponseWriter, req *http.Request) {
		synthetic := req.Header.Get(ProbeHeader) != ""

		// The injector goes first, and an answer it produces leaves this
		// function here — before o.record, before the flight token, before the
		// contract check. That ordering is the control behind bound 4 of
		// faults.go: nothing an injected fault touches can end up on an
		// evidence axis. TestAnInjectedAnswerDoesNotDriveItsOperation and
		// TestAnInjectedRefusalEarnsNoNegativeEvidence fail without it.
		//
		// Synthetic traffic is exempt for the reason every other boundary in
		// this file is drawn the same way: the probe's verdict is about the
		// route's own answer, and a fault firing there would report a defect
		// that is not one. It also keeps `feint probe` usable while rules are
		// armed. TestAFaultDoesNotFireOnTheProbe fails without it.
		if !synthetic {
			if f, claimed := o.faults.fire(r.Operation); claimed {
				started := time.Now()
				status := o.serveFault(w, req, provider, r, f)
				o.recordInjected(r.Operation)
				// Offered to the open spans, marked injected, so a `negative`
				// span cannot close on it — and published on the log, because
				// the operator watching the emulator must see the refusal.
				o.spanExchangeInjected(r.Operation, status)
				o.stream.publishExchange(trace.Exchange{
					Method:    req.Method,
					Path:      req.URL.Path,
					Query:     req.URL.RawQuery,
					Status:    status,
					Ms:        float64(time.Since(started).Microseconds()) / 1000,
					Operation: r.Operation,
					Provider:  provider,
					Mounted:   true,
				})
				return
			}
		}

		o.record(r.Operation, synthetic)
		// In flight for as long as the handler runs, so a store touch the
		// handler makes can be attributed to this operation (assert.go).
		token := o.beginFlight(r.Operation, synthetic)
		defer o.endFlight(token)

		// The report is filed whether or not a contract is loaded: a field the
		// client sent and the handler ignored is a defect on its own, and
		// finding it needs no API description, only the handler's own type.
		rep := &requestReport{}
		req = req.WithContext(contextWithReport(req, rep))

		// The recorder now wraps every request, contract or not, because the log
		// needs a status for each line. It only buffers the body when a contract
		// is loaded, which is what kept it opt-in: a check needs the whole
		// response, a status line does not.
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		if doc != nil {
			rec.body = &bytes.Buffer{}
		}

		// A synthetic request's body is kept so the probe verdict below can see
		// what was asked — Outscale's page size travels in the body. Probe
		// traffic only: buffering every client body would buy nothing.
		var probeBody []byte
		if synthetic && doc != nil && req.Body != nil {
			probeBody, _ = io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(probeBody))
		}

		started := time.Now()
		r.Handler(rec, req)
		elapsed := time.Since(started)

		// Offered to every open assertion span: the negative axis is computed
		// from exactly these lines (assert.go).
		o.spanExchange(r.Operation, rec.status, synthetic)

		unread := rep.fields()
		// Only a request the handler accepted. A field named in a 4xx was not
		// ignored, it was refused — which is the opposite of the defect this
		// report exists to find, and the answer told the client so. Counting it
		// made a suite that deliberately probes a refusal fail the gate, and
		// the fix for that must not be to stop probing refusals.
		//
		// Before the recorder wrapped every request there was no status to read
		// without a contract, so this rule applied to half the runs. It applies
		// to all of them now, which can only remove false positives.
		//
		// And only a request a real client made. The report's question — and
		// the gate's own wording — is "fields a real client sent that no
		// handler read": the seeded probe (#163) fills every optional field
		// the schema declares and the run can satisfy, so its bodies name
		// fields no handler reads by design, and counting them failed the
		// gate on traffic no client ever sends. The same boundary as the
		// score itself: synthetic traffic moves no client-facing number.
		// TestAProbedFieldIsNotAnUnreadField fails without the condition.
		if rec.status < 400 && !synthetic {
			o.recordUnread(r.Operation, unread)
		}
		// The contract verdict is carried on the exchange, not only counted per
		// operation. Unread is what the client sent and no handler read; this is
		// what we answered and the provider's own schema does not define. Both
		// are causal defects, both were already computed, and showing one
		// without the other is an odd place to stop.
		var violations []string
		var vs contract.Violations
		if doc != nil {
			vs = o.check(doc, r.Operation, rec, synthetic)
			for _, v := range vs {
				violations = append(violations, v.String())
			}
		}
		if synthetic && doc != nil {
			o.noteProbe(doc, r.Operation, req.URL.Query(), probeBody, rec, vs)
		}

		o.stream.publishExchange(trace.Exchange{
			Method:     req.Method,
			Path:       req.URL.Path,
			Query:      req.URL.RawQuery,
			Status:     rec.status,
			Ms:         float64(elapsed.Microseconds()) / 1000,
			Operation:  r.Operation,
			Provider:   provider,
			Unread:     unread,
			Violations: violations,
			Mounted:    true,
		})
	}
}

func (o *observer) recordUnread(operation string, fields []string) {
	if len(fields) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	seen := o.unread[operation]
	if seen == nil {
		seen = map[string]bool{}
		o.unread[operation] = seen
	}
	for _, f := range fields {
		seen[f] = true
	}
}

// ProbeHeader marks a request as synthetic. The contract-driven probe sets it,
// no client does, and it is what keeps the two counters apart.
const ProbeHeader = "X-Feint-Probe"

// recordInjected counts one answer the fault injector produced. It is the only
// counter an injected answer moves.
func (o *observer) recordInjected(operation string) {
	o.mu.Lock()
	o.injected[operation]++
	o.mu.Unlock()
}

func (o *observer) record(operation string, synthetic bool) {
	o.mu.Lock()
	if synthetic {
		o.probed[operation]++
	} else {
		o.calls[operation]++
	}
	o.mu.Unlock()
}

// check validates a recorded response and returns what it found.
//
// Only successful ones: an error body is a different schema, and the packs
// already answer the provider's error shape. It both files the deduplicated
// report — one bad field repeated across a hundred calls is one defect — and
// hands the violations back, because the log needs the verdict on this exchange
// rather than on the operation.
func (o *observer) check(doc *contract.Doc, operation string, rec *recorder, synthetic bool) contract.Violations {
	if rec.body == nil || rec.status < 200 || rec.status >= 300 || rec.body.Len() == 0 {
		return nil
	}
	// A response the API does not describe as JSON is not compared as JSON, and
	// this is not a loophole: GetServerUserData answers the raw cloud-init blob
	// a client stored, text/plain, and so does its Outscale and Exoscale
	// equivalent. Decoding it as JSON reported a violation on a route behaving
	// exactly as its own API describes.
	//
	// Found by driving it. The route had been mounted, probed and hardened
	// against YAML injection, and no client had ever walked it (#174) — the
	// probe sends and receives JSON, so the check and the traffic agreed with
	// each other and with nothing else.
	//
	// The Content-Type is the API's own statement about the body, so it is what
	// decides. Not a list of exempt operations: that would need updating every
	// time a pack serves another blob, which is the kind of list one pack
	// forgets.
	//
	// TestANonJSONResponseIsNotComparedAsJSON fails without this.
	if ct := rec.Header().Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		o.markChecked(operation)
		return nil
	}
	// Counted before the verdict, because both verdicts are checks: a
	// validation that failed still looked. What must never count is the early
	// return above, where nothing was compared with anything.
	o.markChecked(operation)
	_, name, known := doc.OperationFor(operation)
	if !known {
		vs := contract.Violations{{
			Path: operation, Reason: "no such operation in the contract",
		}}
		o.report(operation, vs)
		return vs
	}

	var decoded any
	if err := json.Unmarshal(rec.body.Bytes(), &decoded); err != nil {
		vs := contract.Violations{{
			Path: operation, Reason: "the response is not JSON: " + err.Error(),
		}}
		o.report(operation, vs)
		return vs
	}
	// Whatever the verdict below: a violating answer still shows which fields
	// it serves, and the omission check reads the union of all of them.
	//
	// A synthetic answer does not count, and CI is what proved it. The probe
	// leg of conformance.yml drives no client at all, so every object in it is
	// the minimal one the probe's own seeding builds: no address linked, no
	// tags, no user data. The gate then accused ReadVms of omitting
	// PublicIp, Tags and UserData — fields that exist only on a machine a user
	// configured, absent for a reason that is the run's and not the emulator's.
	//
	// So the verdict is over what a client-driven run served, which is the same
	// boundary #163 drew for the unread-fields report one screen up: synthetic
	// traffic moves no client-facing number. The contract check itself still
	// runs on synthetic answers — the probe's whole contract axis depends on
	// it — and only this record is skipped.
	//
	// TestASyntheticAnswerDoesNotVouchForAServedField fails without the guard.
	if !synthetic {
		o.recordServed(operation, decoded)
	}
	if vs := doc.ValidateResponse(name, decoded); len(vs) > 0 {
		o.report(operation, vs)
		return vs
	}
	return nil
}

// noteProbe turns one synthetic exchange into a probe verdict, or into
// nothing. The axis it feeds means "the probe validated this", so every path
// out of here that records nothing is a path where nothing was validated: an
// empty body, an operation with no response schema, a refusal with no declared
// error shape or a body that fails it, a paged answer that overflowed what it
// was asked. Arrival is already counted elsewhere (record), and arrival is not
// a verdict (#156).
func (o *observer) noteProbe(doc *contract.Doc, operation string, query url.Values, reqBody []byte, rec *recorder, checked contract.Violations) {
	if rec.body == nil || rec.body.Len() == 0 {
		return
	}

	switch {
	case rec.status >= 200 && rec.status < 300:
		if len(checked) > 0 {
			return
		}
		op, _, known := doc.OperationFor(operation)
		if !known || op.Response == "" {
			// check() had nothing to hold the answer to, so its silence is not
			// a validation — the same distinction the contract axis draws
			// between "clean" and "unchecked".
			return
		}
		name, inBody, declared := doc.PageSize(op)
		if !declared {
			o.markProbe(o.probeResponse, operation)
			return
		}
		// A paged operation earns the axis only through the call that carried
		// its page size and stayed within it. The plain call — the one the
		// probe harvests identifiers from — proves the schema and not the
		// parameter, and what was not exercised is not counted
		// (TestPagedOperationNeedsItsPageSizeExercised).
		asked, sent := sentPageSize(name, inBody, query, reqBody)
		if !sent {
			return
		}
		var decoded any
		if json.Unmarshal(rec.body.Bytes(), &decoded) != nil {
			return
		}
		if len(doc.PageBound(op, asked, decoded)) > 0 {
			return
		}
		o.markProbe(o.probeResponse, operation)

	case rec.status >= 400 && rec.status < 500:
		if doc.ErrorSchema == "" {
			return
		}
		var decoded any
		if json.Unmarshal(rec.body.Bytes(), &decoded) != nil {
			return
		}
		if len(doc.Validate(doc.ErrorSchema, decoded)) > 0 {
			return
		}
		o.markProbe(o.probeRefusal, operation)
	}
}

func (o *observer) markProbe(into map[string]bool, operation string) {
	o.mu.Lock()
	into[operation] = true
	o.mu.Unlock()
}

// sentPageSize reads the page size a synthetic request asked for, wherever the
// contract said it travels: the query string, or the request body.
func sentPageSize(name string, inBody bool, query url.Values, reqBody []byte) (int, bool) {
	if inBody {
		var body map[string]any
		if json.Unmarshal(reqBody, &body) != nil {
			return 0, false
		}
		asked, ok := body[name].(float64)
		if !ok {
			return 0, false
		}
		return int(asked), true
	}
	raw := query.Get(name)
	if raw == "" {
		return 0, false
	}
	asked, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return asked, true
}

func (o *observer) markChecked(operation string) {
	o.mu.Lock()
	o.checked[operation]++
	o.mu.Unlock()
}

func (o *observer) report(operation string, vs contract.Violations) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, seen := o.violations[operation]; !seen {
		o.violations[operation] = vs
	}
}

// recorder captures a response so it can be checked, and passes it through
// unchanged.
//
// The body is buffered only when body is non-nil, which is when a contract is
// loaded: a check needs the whole response, and the log needs the status line
// alone. That is what keeps the memory cost opt-in while every request still
// reaches the log with a real status.
type recorder struct {
	http.ResponseWriter
	status int
	body   *bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.body != nil {
		r.body.Write(b)
	}
	return r.ResponseWriter.Write(b)
}

// ConformanceView is what /_feint/conformance answers.
// ConformanceView is the shape of /_feint/conformance, exported because more
// than one thing reads it.
//
// It was unexported, so `internal/cli` declared its own copy — with a key
// (`proven_by_a_client`) the server never emitted and `untouched` as an object
// where this one carries an array. The decode failed, both callers fell back to
// empty, and `feint status` printed 0 in the "driven by a client" column
// whatever a client had driven. Nobody noticed, because a silent fallback looks
// exactly like an emulator nobody has used yet.
//
// One shape, one owner. A reader that cannot import it is a reader that will
// copy it.
//
// TestStatusCountsWhatAClientDrove in internal/cli fails without this.
type ConformanceView struct {
	// SchemaVersion says which shape of this document the reader holds, so a
	// pipeline can branch on it. Its meaning is enforced by the frozen fixture
	// (see schema.go): a shape change that does not move
	// ConformanceSchemaVersion fails TestASurfaceChangeDemandsItsVersionBump
	// in internal/cli.
	SchemaVersion int `json:"schema_version"`
	// Served is how many routes are mounted.
	Served int `json:"served"`
	// Exercised is how many of them a real client drove at least once. The
	// number in the title, and the only one a probe cannot move.
	Exercised int `json:"exercised"`
	// Probed is how many routes no client drove and the contract-driven probe
	// nonetheless validated — a success against the response schema, or a
	// refusal against the declared error shape. Reached-but-validated-nothing
	// does not count; the Probes map below still shows the arrivals.
	Probed int `json:"probed"`
	// Calls counts requests per operation, for the ones that were called.
	Calls map[string]int `json:"calls"`
	// Injected counts, per operation, the answers the fault injector produced
	// instead of the handler's (faults.go). None of the other counters here
	// moves for one, and that is the point: without this key a route could look
	// exercised when it only ever answered injected faults, and the whole
	// coverage story would stop being trustworthy — the risk #26 named as the
	// one that must be closed by design rather than by discipline.
	//
	// Empty on any run where nobody armed a rule, which is every run by
	// default. tools/conformance/score.sh fails when it is not, because a run
	// whose numbers describe served behaviour must contain no staged answer.
	Injected map[string]int `json:"injected"`
	// Probes counts synthetic requests per operation, the same way Calls counts
	// real ones.
	//
	// Additive, and it exists because Probed alone is a scalar: a reader could
	// say how many routes were probe-only and never which, so the one question
	// worth asking of that number — which of these has nobody proven — had no
	// answer but a file. Kept in a separate map rather than merged into Calls,
	// for the reason the two counters have always been separate: adding them
	// would make the score go up without anything being proven.
	Probes map[string]int `json:"probes"`
	// Untouched names the routes no real client has driven, which is the list
	// worth acting on: each is an operation the emulator claims and nobody has
	// proven. Computed from the real calls alone, so probing a route never
	// takes it off this list.
	Untouched []string `json:"untouched"`
	// Contracts names the providers whose responses are being checked.
	Contracts []string `json:"contracts"`
	// Violations is what the response check found, per operation.
	Violations map[string][]string `json:"violations"`
	// UnreadRequestFields lists, per operation, the fields a client sent that
	// the handler does not declare. Each one is an argument the API accepted and
	// then ignored, which is the failure nothing else here can see.
	UnreadRequestFields map[string][]string `json:"unread_request_fields"`
	// Fields is the omission check (#88): the declared response fields this
	// run's answers never carried, per operation, next to how many operations
	// the comparison could reach. The mirror of Violations, which only sees
	// what an answer invents.
	Fields FieldGaps `json:"fields"`
	// Machines names the runtime behind this run ("none" when machines are
	// metadata-only). Copied here because the dataplane axis below is defined
	// by it, and a record that depends on a fact must carry the fact.
	Machines string `json:"machines"`
	// Evidence is the per-operation record of independent proofs (#123).
	//
	// Axes, never rungs. Each axis answers its own question and none implies
	// another: a route can be driven by a real client without ever being
	// contract-checked, probed without being driven, dataplane-backed on a
	// path whose contract nobody loaded. The record is a set of named answers
	// rather than an ordered enum precisely so that nothing in this codebase
	// can ever compute "level 4 of 7" by accident — the axes must be listed
	// side by side and never added into one number, the same doctrine the
	// page's bar applies to served, exercised and probed.
	Evidence map[string]Evidence `json:"evidence"`
}

// Evidence names the independent proofs one operation carries.
//
// The behaviour and negative axes were deliberately absent until the suites
// could emit a signal at the assertion level (#123 declined them for exactly
// that reason: without one, any value would have been hand-declared — a
// comment, not a control). The signal now exists: an assertion span, opened
// and closed by a suite against /_feint/assert, whose claim the emulator
// verifies against its own observations before anything is marked. See
// assert.go for what each span must contain.
type Evidence struct {
	// Driven: a real client reached this operation at least once this run.
	// It asserts what the suite asserted, nothing more — #116 and #83 were
	// both driven the whole time they were wrong.
	Driven bool `json:"driven"`
	// Probed: what the contract-driven probe validated here, never whether it
	// merely arrived — an arrival whose only answer was an unread text/plain
	// 404 published probed: true, which was the axis promising more than the
	// code delivered (#156). "response": a success was validated against the
	// operation's own response schema, page size exercised and held where the
	// contract declares one. "refusal": what was validated is that it refuses
	// in the provider's declared error shape — it does not say the success
	// shape was ever seen. "none": the probe validated nothing here. All three
	// are protocol only: a well-shaped empty inventory still passes, cursors
	// and filters are not exercised.
	Probed string `json:"probed"`
	// Contract: "clean" when at least one response was validated against the
	// provider's own description and none violated it, "violating" when one
	// did, "unchecked" when no response of this operation was ever validated
	// — because "no violation" alone cannot tell conformance from silence.
	Contract string `json:"contract"`
	// Dataplane says exactly this and no more: the operation was driven at
	// least once while a machine runtime was configured (Machines above, from
	// the driver's own declaration, never a mode-name comparison). It does
	// not say a machine-level assertion named this operation; that would be
	// the behaviour axis, which does not exist yet.
	Dataplane bool `json:"dataplane"`
	// Shape: "observed" when a real cloud's recorded answer covers this
	// operation and the shapes gate holds the emulator to it, "unobserved"
	// when the catalogue does not cover it, "unknown" when this server was
	// given no catalogue to look in.
	Shape string `json:"shape"`
	// Behaviour: this operation touched a resource whose full lifecycle —
	// created, then destroyed — the store itself observed inside an assertion
	// span a suite declared (assert.go). It does not say the operation's own
	// effect was asserted; the suite's checks around the sequence are what
	// they are, and `driven` above already carries that caveat.
	Behaviour bool `json:"behaviour"`
	// Negative: inside a span where a suite declared it was demanding a
	// refusal, this operation really answered a 4xx to a real client. One
	// demanded refusal earns it; it does not say every refusal this operation
	// owes was reproduced.
	Negative bool `json:"negative"`
}

// Contract verdicts, probe verdicts and shape states, named once.
const (
	ContractClean     = "clean"
	ContractViolating = "violating"
	ContractUnchecked = "unchecked"

	ProbeResponse = "response"
	ProbeRefusal  = "refusal"
	ProbeNone     = "none"

	ShapeObserved   = "observed"
	ShapeUnobserved = "unobserved"
	ShapeUnknown    = "unknown"
)

// SetShapeCovered hands the server the operations an observed real-cloud
// shape covers. The set is computed by the caller from the shapes catalogue —
// this package stays ignorant of providers and of where recordings live, so
// the mapping from a catalogue entry to a mounted operation is not its
// business. Never called, the shape axis honestly answers "unknown" rather
// than demoting every operation to "unobserved".
func (s *Server) SetShapeCovered(operations []string) {
	covered := make(map[string]bool, len(operations))
	for _, op := range operations {
		covered[op] = true
	}
	s.shapeCovered = covered
}

// SetObservedFields hands the server, per operation, the field paths a real
// cloud's recorded answer carried — the corroboration half of the omission
// check (omissions.go). Computed by the caller from the shapes catalogues for
// the same reason SetShapeCovered is: mapping a catalogue entry to a mounted
// operation is not the core's business. Never called, the check publishes
// every declared-but-absent field as unconfirmed and fails on none, because a
// proof nobody handed in is not a proof.
func (s *Server) SetObservedFields(observed map[string][]string) {
	fields := make(map[string]map[string]bool, len(observed))
	for op, paths := range observed {
		set := make(map[string]bool, len(paths))
		for _, p := range paths {
			set[p] = true
		}
		fields[op] = set
	}
	s.observedFields = fields
}

func (s *Server) handleConformance(w http.ResponseWriter, _ *http.Request) {
	s.observer.mu.Lock()
	calls := make(map[string]int, len(s.observer.calls))
	for op, n := range s.observer.calls {
		calls[op] = n
	}
	checked := make(map[string]int, len(s.observer.checked))
	for op, n := range s.observer.checked {
		checked[op] = n
	}
	probed := make(map[string]int, len(s.observer.probed))
	for op, n := range s.observer.probed {
		probed[op] = n
	}
	probeResponse := make(map[string]bool, len(s.observer.probeResponse))
	for op := range s.observer.probeResponse {
		probeResponse[op] = true
	}
	probeRefusal := make(map[string]bool, len(s.observer.probeRefusal))
	for op := range s.observer.probeRefusal {
		probeRefusal[op] = true
	}
	violations := make(map[string][]string, len(s.observer.violations))
	for op, vs := range s.observer.violations {
		reasons := make([]string, 0, len(vs))
		for _, v := range vs {
			reasons = append(reasons, v.String())
		}
		violations[op] = reasons
	}
	unread := make(map[string][]string, len(s.observer.unread))
	for op, fields := range s.observer.unread {
		names := make([]string, 0, len(fields))
		for f := range fields {
			names = append(names, f)
		}
		sort.Strings(names)
		unread[op] = names
	}
	providers := make([]string, 0, len(s.observer.contracts))
	for name := range s.observer.contracts {
		providers = append(providers, name)
	}
	behaviour := make(map[string]bool, len(s.observer.behaviour))
	for op := range s.observer.behaviour {
		behaviour[op] = true
	}
	negative := make(map[string]bool, len(s.observer.negative))
	for op := range s.observer.negative {
		negative[op] = true
	}
	injected := make(map[string]int, len(s.observer.injected))
	for op, n := range s.observer.injected {
		injected[op] = n
	}
	s.observer.mu.Unlock()
	sort.Strings(providers)

	routes := s.AllRoutes()
	untouched := make([]string, 0)
	probedOnly := 0
	for _, r := range routes {
		if calls[r.Operation] > 0 {
			continue
		}
		// Not driven by a client. It goes on the backlog whether or not the
		// probe reached it. The probed count next to it takes a verdict, not
		// an arrival: a route whose synthetic answers validated against
		// nothing is on the backlog with nothing to its name.
		untouched = append(untouched, r.Operation)
		if probeResponse[r.Operation] || probeRefusal[r.Operation] {
			probedOnly++
		}
	}
	sort.Strings(untouched)

	// The dataplane axis reads the driver's own declaration, never a mode
	// name: Noop names itself "none", and that is the whole comparison.
	machines := "none"
	if s.env.Machines != nil {
		machines = s.env.Machines.Name()
	}
	runtimeOn := machines != "none"

	evidence := make(map[string]Evidence, len(routes))
	for _, r := range routes {
		verdict := ContractUnchecked
		switch {
		case len(violations[r.Operation]) > 0:
			verdict = ContractViolating
		case checked[r.Operation] > 0:
			verdict = ContractClean
		}
		shapeState := ShapeUnknown
		if s.shapeCovered != nil {
			shapeState = ShapeUnobserved
			if s.shapeCovered[r.Operation] {
				shapeState = ShapeObserved
			}
		}
		probeVerdict := ProbeNone
		switch {
		case probeResponse[r.Operation]:
			probeVerdict = ProbeResponse
		case probeRefusal[r.Operation]:
			probeVerdict = ProbeRefusal
		}
		evidence[r.Operation] = Evidence{
			Driven:    calls[r.Operation] > 0,
			Probed:    probeVerdict,
			Contract:  verdict,
			Dataplane: calls[r.Operation] > 0 && runtimeOn,
			Shape:     shapeState,
			Behaviour: behaviour[r.Operation],
			Negative:  negative[r.Operation],
		}
	}

	gaps := s.fieldGaps(s.observer.servedCopy())

	writeJSON(w, http.StatusOK, ConformanceView{
		SchemaVersion:       ConformanceSchemaVersion,
		Served:              len(routes),
		Exercised:           len(routes) - len(untouched),
		Probed:              probedOnly,
		Calls:               calls,
		Injected:            injected,
		Probes:              probed,
		Untouched:           untouched,
		Contracts:           providers,
		Violations:          violations,
		UnreadRequestFields: unread,
		Fields:              gaps,
		Machines:            machines,
		Evidence:            evidence,
	})
}
