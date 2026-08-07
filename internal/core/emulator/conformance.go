package emulator

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/stephrobert/feint/internal/contract"
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
	// violations collects contract failures, first occurrence per operation:
	// one bad field repeated across a hundred calls is one defect.
	violations map[string]contract.Violations
	// unread collects the fields clients sent that no handler declared, per
	// operation. Deduplicated, because a client repeating a call repeats the
	// field and one defect must read as one.
	unread map[string]map[string]bool
	// contracts maps a provider to its API description, when one was loaded.
	contracts map[string]*contract.Doc
}

func newObserver(contracts map[string]*contract.Doc) *observer {
	return &observer{
		calls:      map[string]int{},
		probed:     map[string]int{},
		violations: map[string]contract.Violations{},
		unread:     map[string]map[string]bool{},
		contracts:  contracts,
	}
}

// wrap instruments one route: it counts the call, and validates the response
// against the provider's contract when there is one.
func (o *observer) wrap(provider string, r Route) http.HandlerFunc {
	doc := o.contracts[provider]

	return func(w http.ResponseWriter, req *http.Request) {
		o.record(r.Operation, req.Header.Get(ProbeHeader) != "")

		// The report is filed whether or not a contract is loaded: a field the
		// client sent and the handler ignored is a defect on its own, and
		// finding it needs no API description, only the handler's own type.
		rep := &requestReport{}
		req = req.WithContext(contextWithReport(req, rep))

		if doc == nil {
			// No contract for this provider: buffering a response nobody will
			// check would cost memory for nothing. Without a recorder there is
			// no status to read, so every request counts.
			r.Handler(w, req)
			o.recordUnread(r.Operation, rep.fields())
			return
		}

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		r.Handler(rec, req)
		// Only a request the handler accepted. A field named in a 4xx was not
		// ignored, it was refused — which is the opposite of the defect this
		// report exists to find, and the answer told the client so. Counting it
		// made a suite that deliberately probes a refusal fail the gate, and
		// the fix for that must not be to stop probing refusals.
		if rec.status < 400 {
			o.recordUnread(r.Operation, rep.fields())
		}
		o.check(doc, r.Operation, rec)
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

func (o *observer) record(operation string, synthetic bool) {
	o.mu.Lock()
	if synthetic {
		o.probed[operation]++
	} else {
		o.calls[operation]++
	}
	o.mu.Unlock()
}

// check validates a recorded response. Only successful ones: an error body is a
// different schema, and the packs already answer the provider's error shape.
func (o *observer) check(doc *contract.Doc, operation string, rec *recorder) {
	if rec.status < 200 || rec.status >= 300 || rec.body.Len() == 0 {
		return
	}
	_, name, known := doc.OperationFor(operation)
	if !known {
		o.report(operation, contract.Violations{{
			Path: operation, Reason: "no such operation in the contract",
		}})
		return
	}

	var decoded any
	if err := json.Unmarshal(rec.body.Bytes(), &decoded); err != nil {
		o.report(operation, contract.Violations{{
			Path: operation, Reason: "the response is not JSON: " + err.Error(),
		}})
		return
	}
	if vs := doc.ValidateResponse(name, decoded); len(vs) > 0 {
		o.report(operation, vs)
	}
}

func (o *observer) report(operation string, vs contract.Violations) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, seen := o.violations[operation]; !seen {
		o.violations[operation] = vs
	}
}

// recorder captures a response so it can be checked, and passes it through
// unchanged. The body is buffered because a contract check needs the whole of
// it; this is why the check is opt-in and off by default.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.body.Write(b)
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
	// Served is how many routes are mounted.
	Served int `json:"served"`
	// Exercised is how many of them a real client drove at least once. The
	// number in the title, and the only one a probe cannot move.
	Exercised int `json:"exercised"`
	// Probed is how many were only reached by the contract-driven probe:
	// schema-valid, behaviour unproven.
	Probed int `json:"probed"`
	// Calls counts requests per operation, for the ones that were called.
	Calls map[string]int `json:"calls"`
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
}

func (s *Server) handleConformance(w http.ResponseWriter, _ *http.Request) {
	s.observer.mu.Lock()
	calls := make(map[string]int, len(s.observer.calls))
	for op, n := range s.observer.calls {
		calls[op] = n
	}
	probed := make(map[string]int, len(s.observer.probed))
	for op, n := range s.observer.probed {
		probed[op] = n
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
		// probe reached it.
		untouched = append(untouched, r.Operation)
		if probed[r.Operation] > 0 {
			probedOnly++
		}
	}
	sort.Strings(untouched)

	writeJSON(w, http.StatusOK, ConformanceView{
		Served:              len(routes),
		Exercised:           len(routes) - len(untouched),
		Probed:              probedOnly,
		Calls:               calls,
		Untouched:           untouched,
		Contracts:           providers,
		Violations:          violations,
		UnreadRequestFields: unread,
	})
}
