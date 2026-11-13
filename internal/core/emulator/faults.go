package emulator

import (
	"bytes"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Fault injection: the emulator answering the way a cloud answers on a bad day.
//
// Why it exists. coverage/evidence.json carries seven axes per mounted
// operation; six of them stood above 85% while `negative` stood at 34 of 357.
// This emulator proved what its routes answer when everything goes well and
// almost nothing about what they answer when it does not — and a client has two
// families of behaviour, the nominal one and what it does when the API refuses.
// The second family is the one that decides whether a scanner reports a false
// pass or a deploy tool retries (#26, #356).
//
// The division of labour is the whole design, and it is not negotiable:
//
//	the core decides WHEN a call fails and with WHICH status;
//	the pack decides WHAT THAT FAILURE LOOKS LIKE (Faulter, below).
//
// A 503 must reach a Scaleway client as scw's own error shape, an Outscale
// client inside its ResponseContext, an Exoscale client as its own body.
// Otherwise the injected failure is a failure of *this tool*, which is the one
// thing a client never sees from the real cloud — CLAUDE.md's rule 4 applied to
// the error path. Nothing in this file knows a provider name.
//
// Four bounds, each of them a control below rather than a sentence here:
//
//  1. Off by default. The set starts empty and only a request arms it. An
//     emulator that refuses because somebody forgot is an emulator that gets
//     uninstalled. TestAServerStartsWithNoFaultArmed.
//  2. Deterministic. "The first N calls of this operation" is testable; "one
//     call in ten" is not, so no probability knob exists at all — not as an
//     option, because an option that cannot be the subject of a test would
//     become the one somebody reaches for. TestAFaultFiresExactlyTimesTimes.
//  3. Per operation, at the upstream name Route.Operation carries. A rule
//     naming an operation nothing mounts is refused when it is set, rather than
//     never firing while a suite concludes the client survived it.
//     TestAFaultOnAnUnmountedOperationIsRefused.
//  4. An injected fault is not evidence. An answer this injector produced is
//     outside every counter the evidence record reads: it does not make an
//     operation `driven`, it is not contract-checked, its fields join no union,
//     and a `negative` assertion span cannot be closed on it. The only counter
//     it moves is `injected`, published on /_feint/conformance so a reader can
//     see it. Otherwise this feature would manufacture its own success, which is
//     the opposite of its purpose. TestAnInjectedRefusalEarnsNoNegativeEvidence,
//     TestAnInjectedAnswerDoesNotDriveItsOperation.
//
// Attribution is decided out loud, as #26 asked. Every injected answer carries
// X-Feint-Fault naming the operation, which is a header no real cloud sends.
// The trade is deliberate: no client branches on an unknown response header, so
// the claim this project makes — that the client works against a shape its cloud
// emits — is untouched, while the alternative is an operator unable to tell an
// injected 500 from an emulator defect. A certain debugging cost against a
// hypothetical fidelity cost.
//
// Not delivered, and stated rather than discovered: connection resets and true
// transport failures live below the route handler and would need a hook at the
// net/http server; a body cut short (Truncate) is what this offers instead, and
// it is what an interrupted page looks like on the wire. Deterministic control
// over how long an emulated asynchronous transition takes (#26's third comment)
// is a different mechanism — that delay lives in a pack's lifecycle, not in
// front of a handler — and is not folded in here.

// Fault is one rule: an operation, what to answer instead, and for how long.
//
// Exactly one of Status and Truncate may be set; either may carry a Delay, and
// a Delay alone is a slow but correct answer. Times bounds how often the rule
// fires: zero means every call until the rule is cleared.
type Fault struct {
	// Operation is the upstream name, the same key Route.Operation carries
	// ("instance/v1/API.ListServers"). The core holds these as opaque strings,
	// which is what keeps targeting provider-neutral.
	Operation string `json:"operation"`
	// Status is the HTTP status to answer instead of calling the handler. The
	// pack that owns the operation renders the body; a status that pack does
	// not render is refused when the rule is set.
	Status int `json:"status,omitempty"`
	// Delay is a Go duration string ("750ms", "30s") to wait before answering.
	// It is what "the API hangs" looks like from a client, and it is cancelled
	// with the request rather than held to term.
	Delay string `json:"delay,omitempty"`
	// Times is how many calls this rule answers before it stops firing. Zero is
	// every call until the set is replaced or cleared. The rule stays listed
	// once spent, with its hit count, because "it fired twice and then the
	// client recovered" is the assertion a suite wants to make.
	Times int `json:"times,omitempty"`
	// Truncate cuts the handler's own answer to this many bytes. The handler
	// runs and the status line is its own; the body arrives incomplete, which is
	// the partial page and the half-written body a client's decoder has to
	// survive. A pointer so that zero — an empty body — is expressible.
	Truncate *int `json:"truncate_bytes,omitempty"`
	// Nth answers the Nth call to this operation and no other, counting from
	// one. Zero is every call, which is what the rule did before this field
	// existed.
	//
	// **The rank is arrival order, not a replay key**, and the difference is
	// the whole of #851's concurrency rule. Nothing an official client sends
	// carries a stable request identity — measured across the 1088 exchanges in
	// corpus/, the complete set of request header names is nine, and none of
	// them is client-minted — so two concurrent calls to one operation can swap
	// ranks between runs. A rule that fires on a rank is reproducible under one
	// sequential client and nowhere else, and callers are told so rather than
	// sold a seed that cannot hold.
	Nth int `json:"nth,omitempty"`

	// hits, calls and delay are computed here, never read from the request.
	//
	// calls counts every call this rule saw, fired or not, which hits cannot:
	// hits is incremented only on the path that fires, so a spent rule stops
	// counting. Nth needs the rank of the call, so it needs this one.
	hits  int
	calls int
	delay time.Duration
}

// Bounds. Each is far above any real scenario and exists so that a mistyped
// rule cannot wedge a run: a delay somebody meant in milliseconds and wrote in
// minutes is the shape of that mistake.
const (
	maxFaultRules = 64
	maxFaultDelay = 5 * time.Minute
	maxTruncate   = MaxBody
	// maxFaultNth bounds the rank a rule may wait for. A campaign naming the
	// thousandth call of an operation is a campaign whose rule never fires, and
	// a fault that never fires is indistinguishable from a client that survived
	// it — the same reasoning that refuses a rule on an unmounted operation.
	maxFaultNth = 1000
)

// Faulter is the optional half of a Pack that can render a failure in its own
// error dialect.
//
// It is optional rather than mandatory because a pack that renders no error
// shape is a pack whose operations simply cannot carry a fault — refused when
// the rule is set, with the reason — which is a better answer than the core
// writing a body of its own invention. A pack that implements it lists what it
// can render, and the two halves are separate so a rule is refused at the
// moment somebody writes it rather than answered with a wrong shape at the
// moment it fires.
type Faulter interface {
	// FaultStatuses lists the HTTP statuses this pack renders in its own
	// dialect, so a rule naming any other is refused with the list in the
	// message.
	FaultStatuses() []int
	// WriteFault writes this provider's own body for one of those statuses.
	// The core has already written no header and no status: WriteFault owns
	// the whole answer.
	WriteFault(w http.ResponseWriter, r *http.Request, status int)
}

// FaultHeader marks an answer this injector produced, and names the operation
// the rule targeted. No real cloud sends it; see the package comment above for
// why that trade is the right one.
const FaultHeader = "X-Feint-Fault"

// faultSet is the armed rules and what they did. Empty is the only state a
// server starts in.
type faultSet struct {
	mu    sync.Mutex
	rules []*Fault
	// fired is the receipt: which rule answered, against which operation and
	// call rank, and with what status. #851 asks for it because the counters
	// alone cannot say *which* call was hit — "hits: 2" is true of a rule that
	// answered calls 1 and 2 and of one that answered calls 3 and 7, and a test
	// asserting a schedule needs to tell them apart.
	//
	// Bounded like everything else here: a run that fires more than this has
	// stopped being a campaign, and the receipt says so rather than growing
	// until the process dies.
	fired []FiredFault
	// dropped counts the firings the cap refused. Published, because a reader
	// of 256 lines must be able to tell a complete receipt from a truncated
	// one. The comment that claimed "the count keeps rising so a reader can
	// see that the receipt is partial" named no counter, and none existed.
	dropped int
	// epoch names the campaign. replace bumps it, and a line reserved under a
	// previous epoch is dropped rather than written into the set that replaced
	// it: a `delay` rule's answer can still be in flight when somebody arms a
	// new campaign, and that line would read as if a rule nobody armed had
	// fired during this one.
	epoch int
}

// firing is what fire hands back so the answer can be completed later: the
// campaign it belongs to, and the line reserved for it.
//
// The line is reserved **under the lock, at arrival**, which is what makes the
// receipt arrival-ordered. Appending it when the answer finishes ordered it by
// completion instead, so two calls on one rule could land 2 then 1 — and a
// `delay` rule is exactly the shape that makes them cross.
type firing struct {
	epoch int
	slot  int
}

// FiredFault is one line of the receipt.
//
// Rank is arrival order among the calls this rule saw. It is honest about what
// it is: under concurrent clients two calls can swap ranks between runs, so the
// receipt records what happened rather than promising what will happen again.
type FiredFault struct {
	Operation string `json:"operation"`
	Rank      int    `json:"rank"`
	Status    int    `json:"status"`
	// Abandoned says no answer reached the client: it gave up during a delay,
	// which a delayed rule makes ordinary, or the handler never finished.
	// Without it the line reads `status: 0`, and 0 is not an HTTP status, so a
	// reader has to guess. The rule publishes the state the effect produced,
	// and so does the receipt.
	Abandoned bool `json:"abandoned"`
}

// maxFiredReceipt bounds the receipt.
const maxFiredReceipt = 256

// recordFired appends one line to the receipt, and stops appending rather than
// growing without bound. The count keeps rising so a reader can see that the
// receipt is partial instead of believing it complete.
// reserve takes the line this firing will fill, at arrival, under the caller's
// lock. The slot is -1 when the cap refused it, and recordFired then does
// nothing.
func (fs *faultSet) reserve(operation string, rank int) firing {
	if len(fs.fired) >= maxFiredReceipt {
		fs.dropped++
		return firing{epoch: fs.epoch, slot: -1}
	}
	fs.fired = append(fs.fired, FiredFault{Operation: operation, Rank: rank})
	return firing{epoch: fs.epoch, slot: len(fs.fired) - 1}
}

// recordFired completes the line reserved at arrival.
//
// A line whose campaign is gone is dropped: replace has bumped the epoch, and
// writing into the new set would attribute this firing to rules nobody armed.
// TestAStaleFiringDoesNotOverwriteTheNewCampaignsLine fails without that
// check. Not TestAFiringInFlightDoesNotLandInTheNextCampaign, which this
// comment cited first and which passes with the epoch guard removed: after
// a clear the receipt is empty, so the slot bound alone already drops the
// line, and the test cannot tell the two guards apart. A review of this lot
// measured it.
func (fs *faultSet) recordFired(f firing, status int, abandoned bool) {
	if fs == nil || f.slot < 0 {
		return
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if f.epoch != fs.epoch || f.slot >= len(fs.fired) {
		return
	}
	fs.fired[f.slot].Status = status
	fs.fired[f.slot].Abandoned = abandoned
}

// receipt copies what the rules did, in arrival order, and says how many lines
// the cap refused.
//
// Arrival order rather than completion order, because the line is reserved
// under the lock that produced the rank. The previous version appended when the
// answer finished, so a receipt documented "oldest first" could start at rank 2
// — and a delayed rule is the shape that makes two answers cross.
func (fs *faultSet) receipt() ([]FiredFault, int) {
	out := make([]FiredFault, 0)
	if fs == nil {
		return out, 0
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append(out, fs.fired...), fs.dropped
}

// fire reports the rule that answers this call, the rank of the call among
// those this rule has seen, and whether the rule claimed it.
//
// At most one rule per operation exists — duplicates are refused when the set
// is written — so the first match is the only match.
//
// The rank is counted before the decision and on every call, including the ones
// the rule declines: a rule that only counted what it fired could never answer
// "the third call", because the first two would not be counted. That ordering
// is what TestTheRankCountsCallsTheRuleDeclines holds.
//
// The rank is zero when no rule targets the operation, which is how a caller
// tells "no rule here" from "the first call".
func (fs *faultSet) fire(operation string) (Fault, int, firing, bool) {
	if fs == nil {
		return Fault{}, 0, firing{slot: -1}, false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, r := range fs.rules {
		if r.Operation != operation {
			continue
		}
		r.calls++
		if r.Nth > 0 && r.calls != r.Nth {
			return Fault{}, r.calls, firing{slot: -1}, false
		}
		if r.Times > 0 && r.hits >= r.Times {
			return Fault{}, r.calls, firing{slot: -1}, false
		}
		r.hits++
		return *r, r.calls, fs.reserve(operation, r.calls), true
	}
	return Fault{}, 0, firing{slot: -1}, false
}

// armed reports whether any rule is set. It is what a suite asks before
// trusting a run's numbers.
func (fs *faultSet) armed() bool {
	if fs == nil {
		return false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.rules) > 0
}

func (fs *faultSet) snapshot() []faultView {
	out := make([]faultView, 0)
	if fs == nil {
		return out
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, r := range fs.rules {
		out = append(out, faultView{
			Operation:     r.Operation,
			Status:        r.Status,
			Delay:         r.Delay,
			Times:         r.Times,
			Nth:           r.Nth,
			TruncateBytes: r.Truncate,
			Hits:          r.hits,
			Calls:         r.calls,
			Spent:         r.spent(),
		})
	}
	return out
}

// replace arms a new set, and clears the receipt with it.
//
// The receipt belongs to the campaign that produced it. Keeping lines from the
// previous set would make a replay read as if rules that are no longer armed
// had fired during it, which is the kind of mixed population this repository
// has been burned by before.
func (fs *faultSet) replace(rules []*Fault) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.rules = rules
	fs.fired = nil
	fs.dropped = 0
	fs.epoch++
}

// faultView is one rule as the admin plane publishes it: every field present,
// counters included, because a consumer freezing this shape must see the whole
// of it and because "the fault fired twice, then the client recovered" is the
// assertion this endpoint exists to let a script make.
type faultView struct {
	Operation string `json:"operation"`
	Status    int    `json:"status"`
	Delay     string `json:"delay"`
	Times     int    `json:"times"`
	Nth       int    `json:"nth"`
	// Calls is every call this rule saw, fired or not. Hits is the subset it
	// answered. Under `nth` the two differ by construction, and a suite
	// asserting "it fired on the third call" needs both.
	Calls         int  `json:"calls"`
	TruncateBytes *int `json:"truncate_bytes"`
	Hits          int  `json:"hits"`
	Spent         bool `json:"spent"`
}

// spent reports that this rule will never fire again.
//
// Under Times it is the hit count; under Nth it is the rank going past, because
// a rule naming one call is finished the moment that call is behind it. A rule
// that is neither is never spent.
func (f *Fault) spent() bool {
	switch {
	case f.Nth > 0:
		return f.calls >= f.Nth
	case f.Times > 0:
		return f.hits >= f.Times
	}
	return false
}

// faultRequest is what PUT /_feint/faults takes. An object rather than a bare
// array, so the document can grow a field without breaking a consumer — the
// same reasoning emulator/schema.go gives for keeping /_feint/routes an array
// it can never extend.
type faultRequest struct {
	Faults []Fault `json:"faults"`
}

type faultsResponse struct {
	SchemaVersion int         `json:"schema_version"`
	Faults        []faultView `json:"faults"`
	// Fired is the receipt, oldest first. Present and empty when nothing has
	// fired, never absent: a consumer must be able to tell "nothing fired" from
	// "this emulator does not report firings".
	Fired []FiredFault `json:"fired"`
	// FiredDropped is how many firings the cap refused. Zero on an ordinary
	// run; non-zero says `fired` is the first maxFiredReceipt of a longer list.
	FiredDropped int `json:"fired_dropped"`
}

// validateFaults turns a request into the armed set, or refuses it.
//
// Everything here is refused at the moment the rule is written rather than at
// the moment it would fire, and that ordering is the point: a rule that never
// fires is indistinguishable, from the outside, from a client that survived the
// fault. A suite would then report a success nobody produced.
func validateFaults(rules []Fault, owner map[string]string, faulters map[string]Faulter) ([]*Fault, error) {
	if len(rules) > maxFaultRules {
		return nil, fmt.Errorf("%d rules were sent and at most %d are accepted", len(rules), maxFaultRules)
	}
	seen := map[string]bool{}
	out := make([]*Fault, 0, len(rules))
	for i := range rules {
		r := rules[i]
		if r.Operation == "" {
			return nil, fmt.Errorf("rule %d names no operation", i)
		}
		provider, mounted := owner[r.Operation]
		if !mounted {
			return nil, fmt.Errorf("no route serves the operation %q: "+
				"a rule that never fires reads exactly like a client that survived the fault. "+
				"GET /_feint/routes lists the operations this emulator serves", r.Operation)
		}
		if seen[r.Operation] {
			return nil, fmt.Errorf("two rules name the operation %q: one rule per operation, "+
				"so which one fires is never a question", r.Operation)
		}
		seen[r.Operation] = true

		if r.Delay != "" {
			d, err := time.ParseDuration(r.Delay)
			if err != nil {
				return nil, fmt.Errorf("rule %q: delay %q is not a duration (\"750ms\", \"30s\"): %w",
					r.Operation, r.Delay, err)
			}
			if d < 0 {
				return nil, fmt.Errorf("rule %q: delay %s is negative", r.Operation, r.Delay)
			}
			if d > maxFaultDelay {
				return nil, fmt.Errorf("rule %q: delay %s is longer than %s, which is a typo more often "+
					"than an intent", r.Operation, r.Delay, maxFaultDelay)
			}
			r.delay = d
		}
		if r.Times < 0 {
			return nil, fmt.Errorf("rule %q: times %d is negative; 0 means every call until cleared",
				r.Operation, r.Times)
		}
		if r.Nth < 0 {
			return nil, fmt.Errorf("rule %q: nth %d is negative; ranks count from one, and 0 means "+
				"every call", r.Operation, r.Nth)
		}
		if r.Nth > maxFaultNth {
			return nil, fmt.Errorf("rule %q: nth %d is above %d; a rule waiting that long for its call "+
				"is a rule that never fires, and a fault that never fires reads exactly like a client "+
				"that survived it", r.Operation, r.Nth, maxFaultNth)
		}
		if r.Nth > 0 && r.Times > 0 {
			// Refused rather than given a meaning. "nth 3, times 2" could be
			// calls 3 and 4, or call 3 answered twice, and a rule whose reading
			// depends on the reader is worse than one that is missing. Say what
			// it would take instead.
			return nil, fmt.Errorf("rule %q sets both nth %d and times %d: nth names one call and times "+
				"counts from the first, so the two together have no single reading. Use nth for "+
				"\"the third call\", times for \"the first two\"", r.Operation, r.Nth, r.Times)
		}
		if r.Status != 0 && r.Truncate != nil {
			return nil, fmt.Errorf("rule %q sets both a status and a truncation: a status answer is the "+
				"pack's own error body, so there is no handler answer left to cut short", r.Operation)
		}
		if r.Status == 0 && r.Truncate == nil && r.delay == 0 {
			return nil, fmt.Errorf("rule %q asks for nothing: set a status, a truncation or a delay",
				r.Operation)
		}
		if r.Truncate != nil {
			if *r.Truncate < 0 {
				return nil, fmt.Errorf("rule %q: truncate_bytes %d is negative", r.Operation, *r.Truncate)
			}
			if *r.Truncate > maxTruncate {
				return nil, fmt.Errorf("rule %q: truncate_bytes %d is above %d, the largest body this "+
					"emulator handles", r.Operation, *r.Truncate, maxTruncate)
			}
		}
		if r.Status != 0 {
			// The pack decides what a failure looks like, so the pack decides
			// which failures it can express. A core that answered anyway would
			// be inventing a format, which is the one thing this must not do.
			f, renders := faulters[provider]
			if !renders {
				return nil, fmt.Errorf("rule %q: the %s pack renders no error dialect, so no status can "+
					"be injected on its operations", r.Operation, provider)
			}
			if !statusRenderable(f, r.Status) {
				return nil, fmt.Errorf("rule %q: the %s pack does not render %d; it renders %v",
					r.Operation, provider, r.Status, f.FaultStatuses())
			}
		}
		// Hits are the emulator's own count. A client that sends one is
		// ignored rather than refused: it is the shape this endpoint answers
		// with, and round-tripping GET into PUT must not be an error.
		r.hits = 0
		out = append(out, &r)
	}
	return out, nil
}

func statusRenderable(f Faulter, status int) bool {
	for _, s := range f.FaultStatuses() {
		if s == status {
			return true
		}
	}
	return false
}

// serveFault answers one call the injector claimed.
//
// It deliberately does not run any of the observation the ordinary path runs:
// no flight token, no contract check, no unread-field report, no span entry
// beyond the one marked injected. See bound 4 in the package comment — an
// answer this produced must not be able to prove anything about the operation.
func (o *observer) serveFault(w http.ResponseWriter, req *http.Request, provider string, route Route, f Fault) int {
	operation := route.Operation
	if f.delay > 0 {
		// Cancelled with the request. A client that gave up must not leave a
		// goroutine sleeping out a five-minute rule.
		//
		// The hit is already counted by then, deliberately: the rule did fire,
		// and a client that walked away from the answer is exactly the timeout
		// a `delay` rule exists to produce. Counting it only on delivery would
		// make the one scenario the rule is for the one it does not report.
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-req.Context().Done():
			return 0
		}
	}
	w.Header().Set(FaultHeader, operation)

	if f.Status != 0 {
		faulter := o.faulters[provider]
		if faulter == nil {
			// Unreachable: a status rule is refused when it is written unless
			// its pack renders that status. Answering 500 in no dialect at all
			// would be the invented format this whole seam exists to prevent,
			// so the honest thing left is to say so.
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "feint: a status fault is armed on " + operation + " but the " + provider +
					" pack renders no error dialect; this is a defect in feint, not an injected fault",
			})
			return http.StatusInternalServerError
		}
		faulter.WriteFault(w, req, f.Status)
		return f.Status
	}

	// A truncation, or a delay alone. The handler produces its real answer into
	// a buffer, and what reaches the client is the first N bytes of it.
	buf := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
	route.Handler(buf, req)
	body := buf.body.Bytes()
	if f.Truncate != nil && *f.Truncate < len(body) {
		body = body[:*f.Truncate]
	}
	for k, v := range buf.header {
		w.Header()[k] = v
	}
	// Content-Length would contradict a truncated body and make net/http pad or
	// refuse it. The answer is deliberately short of what it announces, so it
	// announces nothing.
	w.Header().Del("Content-Length")
	w.WriteHeader(buf.status)
	_, _ = w.Write(body)
	return buf.status
}

// bufferedResponse collects a handler's whole answer so it can be cut short.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	sent   bool
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.sent {
		return
	}
	b.sent = true
	b.status = status
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.sent = true
	return b.body.Write(p)
}

// handleFaultsRead answers GET /_feint/faults: the armed rules and their hit
// counts. This is the attribution channel #26 called the minimum — a script can
// assert "the fault fired twice", and an operator meeting a 500 can ask whether
// anything was armed at all.
func (s *Server) handleFaultsRead(w http.ResponseWriter, _ *http.Request) {
	fired, dropped := s.faults.receipt()
	writeJSON(w, http.StatusOK, faultsResponse{
		SchemaVersion: FaultsSchemaVersion,
		Faults:        s.faults.snapshot(),
		Fired:         fired,
		FiredDropped:  dropped,
	})
}

// handleFaultsWrite answers PUT /_feint/faults: it replaces the whole set.
//
// Replace rather than append, because the document is meant to live in a file
// next to the fixture it belongs to: a suite that PUTs its rules gets the same
// emulator whatever ran before it, which is the replayability #356 asked for.
func (s *Server) handleFaultsWrite(w http.ResponseWriter, r *http.Request) {
	var req faultRequest
	if err := DecodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rules, err := validateFaults(req.Faults, s.operationOwners(), faultersOf(s.packs))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.faults.replace(rules)
	fired, dropped := s.faults.receipt()
	writeJSON(w, http.StatusOK, faultsResponse{
		SchemaVersion: FaultsSchemaVersion,
		Faults:        s.faults.snapshot(),
		Fired:         fired,
		FiredDropped:  dropped,
	})
}

// handleFaultsClear answers DELETE /_feint/faults: back to the state every
// server starts in.
func (s *Server) handleFaultsClear(w http.ResponseWriter, _ *http.Request) {
	s.faults.replace(nil)
	fired, dropped := s.faults.receipt()
	writeJSON(w, http.StatusOK, faultsResponse{
		SchemaVersion: FaultsSchemaVersion,
		Faults:        s.faults.snapshot(),
		Fired:         fired,
		FiredDropped:  dropped,
	})
}

// operationOwners maps every mounted operation to the pack serving it.
func (s *Server) operationOwners() map[string]string {
	owners := make(map[string]string)
	for _, p := range s.packs {
		for _, r := range p.Routes() {
			owners[r.Operation] = p.Name()
		}
	}
	return owners
}

// faultersOf maps a provider name to the pack's error renderer, for the packs
// that have one. A pack that renders no dialect is simply absent, and every
// status rule on its operations is refused when it is written.
func faultersOf(packs []Pack) map[string]Faulter {
	out := make(map[string]Faulter)
	for _, p := range packs {
		if f, renders := p.(Faulter); renders {
			out[p.Name()] = f
		}
	}
	return out
}

// FaultsArmed reports whether any rule is set. Exported because the question is
// asked from outside the process too: a conformance run whose numbers are meant
// to describe served behaviour must know that nothing was staged.
func (s *Server) FaultsArmed() bool { return s.faults.armed() }
