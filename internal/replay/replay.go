// Package replay reissues a recorded exchange at this emulator and says
// whether the answer is the one the recording carries.
//
// Three things already prove this emulator, and none of them compares its
// answer with the provider's. `mise run conformance` proves a real client is
// satisfied, and says nothing about a field the client happens not to read.
// internal/probe validates a response against the provider's own API
// description, and its own package comment states the ceiling: an emulator
// answering a well-shaped empty object for everything would pass every probe.
// Rule 4 sends a doubt to the SDK, and the SDK gives the type — never the
// value, never which optional field the real API always populates.
//
// A transcript written by `feint proxy` is the only artefact in this project
// that says what a real cloud actually returned. This package is what consumes
// it actively: it takes each recorded request, sends it here, and compares.
//
// # What is compared, and what is deliberately not
//
// A byte diff would be noise. Identifiers, timestamps and addresses differ by
// construction between two runs, so the comparison is graded:
//
//	status          exact
//	fields present  exact, minus what a pack's DeclinedFields() excuses
//	types           exact
//	values          only where a pack declares emulator.InvariantValue
//	ordering        only where a pack declares emulator.InvariantOrder
//
// The last two lines are the ones a first version would get wrong in both
// directions. #270 measured two vpc/v2 creates answering 201 where the cloud
// answers 200, which the status line catches without an account. #320 measured
// Server.public_ips coming back in store order rather than the order the create
// named, which *only* the ordering line catches — a replay that ignored order
// everywhere would have called that run clean.
//
// # Identifiers are rebound, not compared
//
// A recorded run created a server and then addressed it by the identifier the
// cloud minted. This emulator mints its own, so replaying the recorded path
// verbatim would answer 404 on every read, and the whole transcript would read
// as divergent for a reason that is not a defect.
//
// So the replay learns: wherever the recorded answer and this emulator's answer
// hold different values at the same field path, and the recorded one has the
// shape of an identifier a cloud mints (a UUID, an address, an Outscale
// "i-<hex>"), the pair is remembered, and every later recorded request has that
// value substituted before it is sent. That is what makes the identity case of
// #73 reachable at all: a transcript recorded against this emulator replays
// against a *fresh* one with zero divergences, which it cannot do without
// rebinding, because the recording's own identifiers no longer exist anywhere.
//
// That rebinding is also what pairs the elements of a list. A recording made
// against an account that already holds resources lists somebody else's objects
// beside the one the run created, so walking two lists by index grades one
// object against another and reports every field they do not share.
// [compareElements] carries the rule, the two halves of "no counterpart" it has
// to keep apart, and what re-running the corpus said about the count an audit
// had estimated for it.
//
// # Nothing from the recording reaches the output
//
// A transcript is redacted of credentials and is not anonymous: docs/proxy.md
// states, field by field, that the bodies hold the account's inventory. So a
// finding names a path, a type, a status and a *position* — never a value read
// from either side. TestNoFindingCarriesAValueFromTheRecording holds it, and it
// is the reason an out-of-order list is reported as "0,1 answered as 1,0"
// rather than by naming the identifiers that moved.
package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/proxy"
	"github.com/stephrobert/feint/internal/shape"
	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/transcript"
)

// Verdict is what one recorded exchange came to. The three are never summed:
// a divergence is a finding, an unserved operation is a work item (#74), and
// adding them would make the day somebody records a new product look like the
// day the emulator broke.
type Verdict string

const (
	// Matched: same status, every recorded field present with the same type,
	// and every declared invariant held.
	Matched Verdict = "matched"
	// Divergent: the status differs, or a field the recording carries is
	// absent or retyped here, or a declared value or order does not hold.
	Divergent Verdict = "divergent"
	// Unserved: no route is mounted for that request. Not a divergence — it is
	// the work queue, and the day it counts as a failure is the day somebody
	// stops recording.
	Unserved Verdict = "unserved"
	// Skipped: the recording carries no response for that request, so there
	// was nothing to compare. A defect of the recording, counted out loud
	// rather than dropped, because a total that shrinks in silence is the
	// failure `feint shapes --check` already has a paragraph about.
	Skipped Verdict = "skipped"
	// Refused: a [Guard] the caller supplied would not let the request go out.
	// Never a divergence and never a pass — the call was not made, so nothing
	// was measured, and #359's third verdict is exactly that distinction. It
	// exists because the same replay that grades an emulator also reissues a
	// recording at a real account, where a DELETE is a real deletion.
	Refused Verdict = "refused"
)

// FindingKind names what went wrong at one place.
type FindingKind string

const (
	KindStatus FindingKind = "status"
	KindAbsent FindingKind = "absent"
	KindType   FindingKind = "type"
	KindValue  FindingKind = "value"
	KindOrder  FindingKind = "order"
	// KindRedacted is not a divergence either: the recorder replaced the value
	// before writing it, so there is no recorded type to compare against. It is
	// carried so the arithmetic stays legible — a comparison that quietly
	// skipped fields would report "all matched" over a recording it half read.
	KindRedacted FindingKind = "redacted"
	// KindExcused is not a divergence: a field the recording carries, this
	// emulator does not serve, and the pack has declined with a reason. It is
	// carried in the result so that what the verdict subtracts stays visible —
	// a gate that quietly excuses is a gate that hollows out.
	KindExcused FindingKind = "excused"
)

// Finding is one difference, named without quoting either side's data.
//
// Want and Got hold a status, a JSON type name, or a sequence of positions.
// They never hold a value read from a body: see the package comment.
type Finding struct {
	Kind FindingKind `json:"kind"`
	Path string      `json:"path,omitempty"`
	Want string      `json:"want,omitempty"`
	Got  string      `json:"got,omitempty"`
	// Reason is the pack's own words, for KindExcused only.
	Reason string `json:"reason,omitempty"`
}

// Result is the verdict on one recorded exchange.
type Result struct {
	// Seq is the line of the recording, from 1, so a finding can be looked up
	// in the file the operator holds.
	Seq    int    `json:"seq"`
	Method string `json:"method"`
	// Path is anonymised (internal/shape): a replay reports where it went
	// without republishing the identifiers it went to.
	Path      string    `json:"path"`
	Operation string    `json:"operation,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Verdict   Verdict   `json:"verdict"`
	Findings  []Finding `json:"findings,omitempty"`
	// Status is what the endpoint answered, 0 when nothing was sent. A status
	// is the endpoint's own word and not a value read from the recording, so it
	// may be published where a body may not: a caller replaying at a real cloud
	// has to tell "the answer differs" from "the answer never came" (a 401, a
	// 429, a 502), and reading that off a KindStatus finding only works while
	// there is one.
	Status int `json:"status,omitempty"`
	// Refused is the [Guard]'s own sentence for a request it would not let go
	// out, empty for every other verdict. Written by the caller, so it carries
	// whatever the caller put in it — every guard in this repository names an
	// operation and a rule, never a value of the account.
	Refused string `json:"refused,omitempty"`
}

// Report is the whole run.
type Report struct {
	Results   []Result `json:"results"`
	Matched   int      `json:"matched"`
	Divergent int      `json:"divergent"`
	Unserved  int      `json:"unserved"`
	Skipped   int      `json:"skipped"`
	// RefusedCount is how many requests a [Guard] would not let go out.
	RefusedCount int `json:"refused"`
	// Excused counts fields subtracted by a pack's DeclinedFields().
	Excused int `json:"excused"`
	// Redacted counts fields the recorder replaced before writing, whose type
	// therefore could not be compared. Reported for the reason Excused is: what
	// a verdict subtracts has to stay visible.
	Redacted int `json:"redacted"`
	// Values and Orders are how many declared comparisons of each kind actually
	// ran. A declaration whose subject the recording does not carry, or whose
	// elements no earlier exchange bound, is not counted — so a run that
	// evaluated none says so instead of reading as a clean pass on a check that
	// never happened.
	//
	// Two numbers rather than one, and the falsification is why: with a single
	// total, breaking the ordering declaration left the two value declarations
	// still counting, the total stayed above zero, and the test that was meant
	// to prove the order check ran stayed green
	// (tools/falsify/specs/replay-compares.json, run of 2026-08-20).
	Values int `json:"values_checked"`
	Orders int `json:"orders_checked"`
	// Rebound is how many recorded identifiers were bound to one this
	// emulator minted. Reported because it is the difference between "the
	// recording replayed" and "the recording was read": a run that rebinds
	// nothing on a transcript full of creates has not followed the causality
	// it claims to.
	Rebound int `json:"rebound"`
	// Ambiguous is how many recorded values two different field names bound to
	// two different answers — project_id and organization_id spelled the same
	// on the account #352 recorded. Reported rather than silently resolved: it
	// is the count of places where the field name, and not the value, decided
	// what the request carried, and a reader is entitled to know the recording
	// said less than the replay needed.
	Ambiguous int `json:"ambiguous"`
}

// Options is what a run needs.
type Options struct {
	// Endpoint is the running emulator, e.g. http://127.0.0.1:4599.
	Endpoint string
	// Client is the HTTP client. Required: a caller sets its timeout, and a
	// default here would be a policy this package has no business having.
	Client *http.Client
	// Table names the operation a request addresses and reports whether
	// anything serves it. It is emulator.NewTable over the mounted packs — the
	// same table `feint proxy` uses to name an exchange, rather than a second
	// answer to the same question.
	Table *emulator.Table
	// Declined and Invariants are what the packs declare, gathered by the
	// caller because it is the caller that holds the packs.
	Declined   []emulator.FieldDecline
	Invariants []emulator.Invariant
	// Bind seeds the rebinding table before the first request: a field name,
	// and the value every recorded identifier sitting at that field must be
	// sent as.
	//
	// Rebinding otherwise learns from what the endpoint answers, which needs a
	// read before the first write. A recording that opens on a create has none
	// — corpus/scaleway/terraform.jsonl starts with POST /vpc/v2/…/vpcs — so
	// its project_id would go out as the identifier the recording carries,
	// which belongs to no account the replay is pointed at. Seeding it is the
	// caller saying "this account is the one that stands in for the recorded
	// one", and only for identifiers: a seed never rewrites a free-text field,
	// because a name is not something a later request refers back to.
	Bind map[string]string
	// Guard, when set, is asked before every request goes out and is handed
	// every answer that comes back. nil means send everything, which is what
	// replaying at an emulator wants.
	//
	// It exists because the same comparison runs in two directions (#359): at
	// this emulator, where a request costs nothing and a DELETE deletes a
	// fixture, and at the provider itself, where a DELETE deletes an account's
	// property. The refusal has to sit where the rebound path is known — a
	// caller inspecting the recording sees the identifier the *cloud minted
	// last time*, not the one this run just created — and that is here.
	Guard Guard
}

// Guard is consulted around every request a run would issue.
//
// Two methods rather than one, because they answer different questions and only
// the caller can answer either: whether this request may be sent at all, and
// what the answer means for the ledger of objects the caller will have to
// destroy. Neither belongs in this package — a [Report] is published, and the
// identifiers a real account minted are not.
type Guard interface {
	// Before reports why this request must not be sent, or "" to allow it. A
	// refused request is not issued and its exchange is [Refused].
	Before(Attempt) string
	// After is handed the status and the decoded body the endpoint answered,
	// so the caller can record what the request created.
	After(a Attempt, status int, body any)
}

// Attempt is one request as it will actually go out: after rebinding, after
// seeding, with the operation the route table names it by.
//
// Body is the decoded request body, nil when there is none. It is the *sent*
// body rather than the recorded one, which is the whole point for a guard that
// has to tell a value this run resolved from a value the recording invented.
type Attempt struct {
	Seq       int    `json:"seq"`
	Operation string `json:"operation,omitempty"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Query     string `json:"query,omitempty"`
	Body      any    `json:"-"`
}

// Run replays a recording in order and reports on each exchange.
//
// In order and one at a time, deliberately: a transcript is a causal sequence —
// the create that mints an identifier comes before the read that uses it — and
// replaying it concurrently would break the very rebinding that makes it
// replayable.
func Run(ctx context.Context, exs []trace.Exchange, opt Options) (Report, error) {
	if opt.Client == nil {
		return Report{}, fmt.Errorf("replay needs an HTTP client with a timeout its caller chose")
	}
	if opt.Table == nil {
		return Report{}, fmt.Errorf("replay needs the emulator's route table to tell an unserved operation from a divergent one")
	}
	b := newBindings()
	for field, value := range opt.Bind {
		if field == "" || value == "" {
			// A seed with no field would apply to path segments, which is the
			// one place [bindings.resolve] falls back to when nothing else
			// names the value: it would rewrite every identifier of every URL
			// to one string. Refused rather than ignored.
			// TestASeedWithNoFieldIsRefused fails without this.
			return Report{}, fmt.Errorf("a seeded binding needs both a field name and a value to send under it")
		}
		b.forced[field] = value
	}
	var rep Report
	for i := range exs {
		res, ran, err := replayOne(ctx, &exs[i], i+1, b, opt)
		if err != nil {
			return rep, err
		}
		rep.Values += ran.values
		rep.Orders += ran.orders
		for _, f := range res.Findings {
			switch f.Kind {
			case KindExcused:
				rep.Excused++
			case KindRedacted:
				rep.Redacted++
			}
		}
		switch res.Verdict {
		case Matched:
			rep.Matched++
		case Divergent:
			rep.Divergent++
		case Unserved:
			rep.Unserved++
		case Skipped:
			rep.Skipped++
		case Refused:
			rep.RefusedCount++
		}
		rep.Results = append(rep.Results, res)
	}
	rep.Rebound = b.count()
	rep.Ambiguous = b.ambiguities()
	return rep, nil
}

// leafField is the JSON key an invariant's path ends at, which is the field
// name its values were learned under. "server.public_ips[].id" is a sequence of
// values the recording carried at "id", so that is the scope to resolve them
// in — the same reasoning applyQuery uses for a parameter name.
func leafField(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		path = path[i+1:]
	}
	return strings.TrimSuffix(path, "[]")
}

// checked counts the declared comparisons one exchange actually ran, per kind.
type checked struct{ values, orders int }

func replayOne(ctx context.Context, x *trace.Exchange, seq int, b *bindings, opt Options) (Result, checked, error) {
	path := b.applyPath(x.Path)
	query := b.applyQuery(x.Query)
	res := Result{Seq: seq, Method: x.Method, Path: shape.AnonymisePath(path)}

	if x.Res == nil {
		res.Verdict = Skipped
		return res, checked{}, nil
	}

	req, err := http.NewRequestWithContext(ctx, x.Method, opt.Endpoint+path, nil)
	if err != nil {
		return res, checked{}, fmt.Errorf("line %d: build the request: %w", seq, err)
	}
	req.URL.RawQuery = query

	// The route is resolved before the request goes out, and an unserved one is
	// never sent. Sending it would add an exchange to the emulator's own
	// observations for an operation no pack claims, which is a fact `feint
	// status` reports and this tool has no business manufacturing.
	mounted, ok := opt.Table.Lookup(req)
	if !ok {
		res.Verdict = Unserved
		return res, checked{}, nil
	}
	res.Operation = mounted.Route.Operation
	res.Provider = mounted.Provider

	attempt := Attempt{Seq: seq, Operation: mounted.Route.Operation, Method: x.Method, Path: path, Query: query}
	if x.Req != nil && x.Req.Body != nil {
		attempt.Body = b.applyValue("", x.Req.Body)
	}
	// Asked before anything is sent, and after rebinding, because what a guard
	// has to judge is the request that would actually go out. A DELETE whose
	// path still carries the identifier the recording holds addresses nothing;
	// the same DELETE after rebinding addresses whatever this run just created,
	// or — if the rebinding went wrong — something that was already there.
	// TestAGuardRefusalIsNotSentAndIsNotADivergence fails without this.
	if opt.Guard != nil {
		if why := opt.Guard.Before(attempt); why != "" {
			res.Verdict = Refused
			res.Refused = why
			return res, checked{}, nil
		}
	}

	if attempt.Body != nil {
		body, err := encodeBody(attempt.Body)
		if err != nil {
			return res, checked{}, fmt.Errorf("line %d: encode the recorded request body: %w", seq, err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	setHeaders(req, x)

	resp, err := opt.Client.Do(req)
	if err != nil {
		return res, checked{}, fmt.Errorf("line %d: %s %s: %w", seq, x.Method, res.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := readBody(resp)
	if err != nil {
		return res, checked{}, fmt.Errorf("line %d: read the answer: %w", seq, err)
	}
	res.Status = resp.StatusCode
	// Handed over before the comparison, so that a caller keeping a ledger of
	// what it created holds the identifier even when the exchange goes on to be
	// graded divergent. An object created by a request whose answer was not the
	// recorded one is still an object somebody has to destroy.
	if opt.Guard != nil {
		opt.Guard.After(attempt, resp.StatusCode, got)
	}

	// Compared first, learned from second.
	//
	// The reasoning: an order invariant maps the recorded sequence through the
	// bindings and then compares, so folding this exchange's own answer in first
	// could bind each recorded identifier to whichever one sat at its position,
	// and a reordered list would then match by construction.
	//
	// What is *proven* is narrower, and this comment says so rather than
	// claiming a control it does not have. Swapping these two statements was
	// falsified on 2026-08-20 and every test stayed green: the property is
	// already carried by the first-binding-wins rule in [bindings.learn], which
	// refuses to rebind an identifier an earlier exchange already mapped —
	// #320's shape has the addresses minted by earlier POST /ips calls. So this
	// order is defence in depth for the case where a create mints and orders in
	// one answer, and that case has no fixture here.
	findings, evaluated := compare(x, resp.StatusCode, got, mounted.Route.Operation, b, opt)
	res.Findings = findings
	b.learn("", x.Res.Body, got)
	res.Verdict = Matched
	for _, f := range res.Findings {
		if f.Kind != KindExcused && f.Kind != KindRedacted {
			res.Verdict = Divergent
			break
		}
	}
	return res, evaluated, nil
}

// setHeaders sends what can honestly be reissued.
//
// Content-Type and Accept, and nothing else. Every other recorded header has
// had its value dropped by the proxy's redaction — the value is "REDACTED" on
// disk — so reissuing it would send a string the client never sent, and this
// emulator authenticates nobody, so nothing is lost. A Content-Length or a
// Content-Encoding copied from the recording would describe a body that was
// re-encoded here, which is worse than absent.
func setHeaders(req *http.Request, x *trace.Exchange) {
	if x.Req == nil {
		return
	}
	for name, value := range x.Req.Headers {
		switch strings.ToLower(name) {
		case "content-type", "accept":
			req.Header.Set(name, value)
		}
	}
	if req.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
}

// encodeBody renders a decoded recorded body back onto the wire. A body the
// proxy could not decode as JSON was recorded as a string and goes out as those
// bytes, not as a quoted JSON string.
func encodeBody(v any) ([]byte, error) {
	if s, isText := v.(string); isText {
		return []byte(s), nil
	}
	return json.Marshal(v)
}

// readBody decodes the emulator's answer with UseNumber, for the reason the
// proxy recorded with it: a 19-digit identifier read through float64 comes back
// changed, and a comparison that says "number" must not have altered the value
// it read to say so.
func readBody(resp *http.Response) (any, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		// Not JSON: the string is what the emulator answered, and comparing it
		// as a scalar is more honest than failing the whole run on it.
		return string(raw), nil
	}
	return out, nil
}

// compare grades one answer against the recorded one.
func compare(x *trace.Exchange, status int, got any, operation string, b *bindings, opt Options) ([]Finding, checked) {
	var out []Finding
	if status != x.Status {
		out = append(out, Finding{
			Kind: KindStatus,
			Want: strconv.Itoa(x.Status),
			Got:  strconv.Itoa(status),
		})
	}
	out = append(out, compareShape("", x.Res.Body, got, operation, b, opt)...)
	invariants, evaluated := compareInvariants(x.Res.Body, got, operation, b, opt)
	out = append(out, invariants...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	return out, evaluated
}

// compareShape walks the recorded answer and this emulator's answer together.
//
// A parallel walk rather than two flattened path sets, and the difference is
// what it reports: a subtree the emulator does not serve is named once, at its
// root, instead of once per leaf underneath it. The first version flattened,
// and a `servers` catalogue holding four commercial types against a recording
// holding ninety produced several hundred lines, none of which was a distinct
// decision.
//
// Only what the recording carries is checked. A field this emulator adds and
// the recording lacks is not a finding here: internal/contract already refuses
// a field the provider's own description does not define, and a recording made
// against a sparse account legitimately carries less than a full one.
func compareShape(path string, want, got any, operation string, b *bindings, opt Options) []Finding {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []Finding{{Kind: KindType, Path: path, Want: "object", Got: jsonType(got)}}
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// A map whose keys are the cloud's inventory rather than its vocabulary
		// is graded on the entries both sides carry, and an entry only the
		// recording has is not a missing field: it is a value, and values are
		// compared only where a pack declares an invariant. The recognition is
		// transcript.DataKeyed, shared with `feint shapes --check` because the
		// two gates disagreeing about what counts as a field is exactly what
		// this fixes — the emulated catalogue serves 18 of the 136 commercial
		// types fr-par-1 publishes, deliberately, and the corpus reported 127
		// of its 136 findings on that one decision while the shapes gate
		// reported none of them.
		//
		// TestAnInventoryKeyTheRecordingHasIsNotAMissingField fails without
		// this.
		inventory := transcript.DataKeyedObject(w)
		var out []Finding
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			nested, present := g[k]
			if !present {
				if inventory {
					continue
				}
				out = append(out, absentOrExcused(child, jsonType(w[k]), operation, opt))
				continue
			}
			out = append(out, compareShape(child, w[k], nested, operation, b, opt)...)
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return []Finding{{Kind: KindType, Path: path, Want: "array", Got: jsonType(got)}}
		}
		element := path + "[]"
		if len(w) > 0 && len(g) == 0 {
			// The recording saw elements and this emulator answered none. One
			// finding at the element, never one per field of the element: the
			// shape underneath was not observed, it was not omitted.
			return []Finding{absentOrExcused(element, "array element", operation, opt)}
		}
		return compareElements(element, w, g, operation, b, opt)
	default:
		if proxy.IsPlaceholder(want) {
			// The recorder replaced this value, so its type is erased rather
			// than observed: a redacted number reads as a string on disk.
			// Comparing it would manufacture a divergence out of the proxy's
			// own hygiene, which is the opposite of what this tool is for. The
			// field's *presence* was still checked one level up, and the count
			// below keeps the subtraction visible.
			//
			// TestARedactedValueIsNotComparedAndIsCounted fails without this.
			return []Finding{{Kind: KindRedacted, Path: path, Want: jsonType(got)}}
		}
		if wt, gt := jsonType(want), jsonType(got); wt != gt {
			// A recorded null carries no type: the field was there and empty,
			// and an emulator answering a concrete value is not a defect. The
			// converse is: a null here where the cloud answered a string is an
			// unpopulated field, and that is reported.
			if wt == "null" {
				return nil
			}
			return []Finding{{Kind: KindType, Path: path, Want: wt, Got: gt}}
		}
		return nil
	}
}

// compareElements walks two lists together, pairing each recorded element with
// the answered element that carries the same identifier rather than with the
// one that happens to sit at its index.
//
// # Why position is not an answer
//
// A recording taken against an account that already holds resources lists the
// objects that were there beside the one the run created. Compared by index,
// element 0 of the recording — somebody else's volume, minted months ago with
// its own set of optional fields populated — is graded against element 0 here,
// which is the volume this run just created, and every field the two do not
// share is reported as a divergence. Nothing in that is a fact about this
// emulator, and #427 would have recorded 212 more operations through it (#469).
//
// # What the re-run said, against what the audit had estimated
//
// The audit of 2026-08-25 attributed 26 of the 59 findings of #433 and more
// than 20 of the 92 of #434 to this defect, by reading the recordings. That
// attribution does not survive re-running them. `corpus/scaleway/scw-billed-shapes.jsonl`
// is the recording both issues measure, and it produces the *same 169 findings*
// through this comparison and through the positional one it replaced — nothing
// moved, because its findings sit on single objects (GetVolume, GetSnapshot,
// GetLB, GetServer) and not on the members of a list.
//
// What the re-run did find is one manufactured finding, in another file, and
// its own acceptance entry had already named the cure verbatim: "It goes when
// the replay matches list elements by the identifier it has already rebound
// instead of by position" (#436, exoscale/v2.list-security-groups). The defect
// is real and the fix is right; the number attached to it was an estimate, and
// this paragraph is here so that nobody re-derives 26 and 20 from a comment.
//
// # What identifies an element, and why no field name is written here
//
// The replay already rebinds identifiers — that is how it follows a resource
// across a run — so where a recorded value has a binding, the object it names
// and the one this emulator minted for it are already known to be the same. An
// element is therefore named by the minted values it carries, mapped through
// the bindings, and a value names an element only where it names *exactly one*
// on each side: project_id is carried by every element of a list and identifies
// none of them, an id is carried by one. Uniqueness is what tells those apart,
// which is why no field name is hard-coded — a rule written on names would have
// to learn Scaleway's id, Outscale's VmId, Exoscale's id, and whatever a fourth
// pack mints.
//
// # The two halves of "no counterpart", and only one of them is a finding
//
// This is the half a first version gets wrong in the other direction, and it
// was measured here before it was written down: reporting every unpaired
// recorded element produced 64 findings over the committed corpus, 57 of them
// saying that a real account has twelve Exoscale zones where this emulator
// serves one.
//
// The distinction is whether the replay knows what the counterpart should have
// been. An element whose identifier an earlier exchange *bound* has a
// counterpart in this run by construction — this run created it — so its
// absence from a list is a fact about this emulator and is reported. An element
// nothing ever bound is an object of the recorded account that this run never
// asked for: a zone, a public template, a security group that was already
// there. It has no counterpart by construction, and calling it absent would
// accuse this emulator of omitting something nobody asked it to make, which is
// as false as comparing it by index.
//
// # The fallback
//
// Where no recorded element pairs with any answered one, the two lists are not
// in the same namespace and identity says nothing at all — the marketplace
// catalogue is the measured case, 90 images recorded against 18 served and not
// one of the 90 identifiers ever minted here. The walk then falls back to
// position, which is what it did before and no worse.
//
// TestListElementsAreMatchedByReboundIdentifier and
// TestAnElementThisRunCreatedAndTheListOmitsIsOneFinding fail without this;
// TestAListWithNothingToIdentifyItsElementsIsStillComparedByPosition holds the
// fallback and TestAnAccountsOwnObjectIsNotReportedAsAbsent holds the half
// above.
func compareElements(element string, want, got []any, operation string, b *bindings, opt Options) []Finding {
	matched, byIdentity, blanked := matchElements(want, got, b)

	var out []Finding
	if blanked {
		// Named once, and never folded into "this element carries no
		// identifier". Those are two different sentences: one says the element
		// has nothing to be known by, the other says it has something and the
		// recorder replaced it before writing. Reported for the reason an
		// excused field is reported — what a comparison subtracts has to stay
		// visible — and as [KindRedacted], which is not a divergence, because
		// the proxy's own hygiene is not a defect of this emulator.
		//
		// TestAnElementWhoseIdentifierIsRedactedIsNotFiledAsUnidentified fails
		// without this.
		out = append(out, Finding{Kind: KindRedacted, Path: element, Want: "identifier"})
	}
	if !byIdentity {
		for i := range want {
			if i >= len(got) {
				break
			}
			out = append(out, compareShape(element, want[i], got[i], operation, b, opt)...)
		}
		return out
	}

	taken := make([]bool, len(got))
	for _, m := range matched {
		if m.at >= 0 {
			taken[m.at] = true
		}
	}
	next := 0
	spare := func() int {
		for next < len(got) {
			j := next
			next++
			if !taken[j] {
				taken[j] = true
				return j
			}
		}
		return -1
	}

	orphan := false
	for i := range want {
		j := matched[i].at
		if j < 0 {
			switch {
			case matched[i].outcome == identityRedacted:
				// Not placed by position, and that is the whole point: an
				// element nobody could read is not an element that belongs
				// where the index puts it. The line above already named it.
				continue
			case matched[i].outcome == identityNone:
				// Nothing identifies it, so position is all there is for it —
				// the same answer the whole list falls back to, applied to the
				// one element that needs it.
				j = spare()
			case matched[i].known:
				// An object this run created, and this list does not carry it.
				orphan = true
				continue
			default:
				// The recorded account's own object. See the doc comment.
				continue
			}
		}
		if j < 0 {
			continue
		}
		out = append(out, compareShape(element, want[i], got[j], operation, b, opt)...)
	}
	if orphan {
		// One finding for the list, never one per element: the statement is
		// "the recording carries elements this emulator does not answer", and
		// it is the same statement the empty-list branch makes. Saying it once
		// per element is how nine operations turned into several hundred lines
		// the first time a list was graded here.
		out = append(out, absentOrExcused(element, "array element", operation, opt))
	}
	return out
}

// identityOutcome is what reading one list element's identifier came to.
//
// Three and never two, which is rule 2 of the measurement-integrity skill: a
// reader that maps its input to "identified" / "not identified" files
// *unreadable* under *not identified*, and the element whose identifier the
// recorder replaced would then be paired by position in silence — which is the
// defect this file is fixing, arriving through the door the proxy's redaction
// has already opened once (the first row of that skill's own table).
type identityOutcome int

const (
	// identityNone: the element carries nothing a cloud mints. Position is all
	// there is for it, and saying so is honest.
	identityNone identityOutcome = iota
	// identityFound: the element carries at least one minted value, mapped
	// through the bindings into this emulator's namespace.
	identityFound
	// identityRedacted: the element carries a value the recorder replaced
	// before writing it. Something was there and it cannot be read, so this
	// walk refuses to assert the element has no identity — it cannot tell
	// whether what was blanked was one.
	identityRedacted
)

// identityKey is one name a list element answers to, in this emulator's
// namespace.
type identityKey struct {
	// key is "<field>\x00<value>". The field name is part of it because one
	// recorded value under two names is two different things here — the
	// project_id and organization_id of [bindings] — and a bare value would
	// merge them.
	key string
	// rebound reports that an earlier exchange actually bound this value, so
	// the object it names is one this run created rather than one the recorded
	// account already held. It is what separates the two halves of "no
	// counterpart" in [compareElements].
	rebound bool
}

// elementIdentity is what one list element is known by, with which of the three
// outcomes reading it came to.
type elementIdentity struct {
	// keys are sorted by field name, so two runs over the same document pair
	// the same way. Determinism first: a gate that answers differently on two
	// identical inputs is a gate that gets disarmed the first time it seems to
	// lie.
	keys    []identityKey
	outcome identityOutcome
}

// identifyElement reads the identifiers one list element carries.
//
// rebind is true for the recorded side, where a value has to be mapped into
// this emulator's namespace before it can name anything here, and false for
// what the endpoint answered, which is already in it.
//
// Only the element's own top-level string fields, deliberately: an identifier a
// cloud hands out sits there, and walking deeper would let a nested project_id
// — the same on every element — offer a pairing that [matchElements] then has
// to undo by uniqueness anyway.
func identifyElement(el any, b *bindings, rebind bool) elementIdentity {
	var (
		keys    []identityKey
		blanked bool
	)
	consider := func(field, value string) {
		if proxy.IsPlaceholder(value) {
			blanked = true
			return
		}
		if !looksMinted(value) {
			return
		}
		bound := false
		if rebind {
			if to, ok := b.resolve(field, value); ok {
				value, bound = to, true
			}
		}
		keys = append(keys, identityKey{key: field + "\x00" + value, rebound: bound})
	}
	switch e := el.(type) {
	case map[string]any:
		names := make([]string, 0, len(e))
		for k := range e {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if text, isText := e[k].(string); isText {
				consider(k, text)
			}
		}
	case string:
		// A list of bare identifiers, which is how a body names the addresses
		// of a server. No field to scope it by, which is the case
		// [bindings.resolve] falls back to for a path segment.
		consider("", e)
	}
	switch {
	case len(keys) > 0:
		return elementIdentity{keys: keys, outcome: identityFound}
	case blanked:
		return elementIdentity{outcome: identityRedacted}
	default:
		return elementIdentity{outcome: identityNone}
	}
}

// match is what became of one recorded element.
type match struct {
	// at is the index in the answered list this element names, or -1.
	at int
	// known reports that at least one identifier naming this element, and
	// naming only it, was bound by an earlier exchange — so this run created
	// its counterpart and a list without it is a finding.
	known   bool
	outcome identityOutcome
}

// matchElements pairs recorded elements with answered ones by identifier.
//
// byIdentity reports whether any pair was made at all: it is what tells "these
// two lists describe the same objects" from "these two lists have nothing in
// common", and only the first justifies reading anything into an element that
// did not pair. blanked reports whether any recorded element's identifier was
// unreadable.
func matchElements(want, got []any, b *bindings) (matched []match, byIdentity, blanked bool) {
	matched = make([]match, len(want))
	identities := make([]elementIdentity, len(want))
	seen := map[string]int{}
	for i := range want {
		identities[i] = identifyElement(want[i], b, true)
		matched[i] = match{at: -1, outcome: identities[i].outcome}
		if identities[i].outcome == identityRedacted {
			blanked = true
		}
		for _, k := range identities[i].keys {
			seen[k.key]++
		}
	}

	// A key that names two answered elements names neither: -1 rather than the
	// last one walked, so a repeated value cannot decide a pairing by the order
	// the list happened to arrive in.
	at := map[string]int{}
	for j := range got {
		for _, k := range identifyElement(got[j], b, false).keys {
			if _, twice := at[k.key]; twice {
				at[k.key] = -1
				continue
			}
			at[k.key] = j
		}
	}

	taken := make([]bool, len(got))
	for i := range identities {
		for _, k := range identities[i].keys {
			// Only a key that names one recorded element says anything about
			// which element this is; a project_id every element carries says
			// only which account the recording was made on.
			if seen[k.key] != 1 {
				continue
			}
			if k.rebound {
				matched[i].known = true
			}
			if matched[i].at >= 0 {
				continue
			}
			j, ok := at[k.key]
			if !ok || j < 0 || taken[j] {
				continue
			}
			matched[i].at = j
			taken[j] = true
			byIdentity = true
		}
	}
	return matched, byIdentity, blanked
}

// absentOrExcused turns a missing field into a finding, or into the pack's own
// argument for not serving it.
func absentOrExcused(path, typ, operation string, opt Options) Finding {
	for _, d := range opt.Declined {
		if d.Matches(operation, path) {
			return Finding{Kind: KindExcused, Path: path, Want: typ, Reason: d.Reason}
		}
	}
	return Finding{Kind: KindAbsent, Path: path, Want: typ}
}

// compareInvariants checks the two aspects a pack has to declare: a value that
// is the same on two runs, and a list whose order is the contract.
func compareInvariants(want, got any, operation string, b *bindings, opt Options) ([]Finding, checked) {
	var out []Finding
	var evaluated checked
	for _, inv := range opt.Invariants {
		if inv.Operation != operation {
			continue
		}
		wantSeq := collect(inv, "", want)
		if len(wantSeq) == 0 {
			// The recording does not carry this field on this exchange —
			// CreateServer without a public IP, for instance. Nothing to hold
			// the emulator to, and inventing a finding here would make the
			// declaration a liability rather than a check.
			continue
		}
		gotSeq := collect(inv, "", got)
		switch inv.Kind {
		case emulator.InvariantValue:
			evaluated.values++
			if !sameValues(wantSeq, gotSeq) {
				out = append(out, Finding{Kind: KindValue, Path: inv.Path,
					Want: fmt.Sprintf("%d value(s) the recording carries", len(wantSeq)),
					Got:  fmt.Sprintf("%d differing here", differing(wantSeq, gotSeq))})
			}
		case emulator.InvariantOrder:
			// The recorded sequence is mapped into this emulator's own
			// namespace before it is compared, using only what earlier
			// exchanges bound. An element nothing bound leaves the check
			// unevaluated rather than passed: see reorder.
			mapped := make([]any, len(wantSeq))
			for i, v := range wantSeq {
				mapped[i] = b.applyValue(leafField(inv.Path), v)
			}
			positions, ran := reorder(mapped, gotSeq)
			if ran {
				evaluated.orders++
			}
			if positions != "" {
				out = append(out, Finding{Kind: KindOrder, Path: inv.Path,
					Want: recordedPositions(len(wantSeq)), Got: positions})
			}
		}
	}
	return out, evaluated
}

// collect gathers, in document order, every value at the path an invariant
// names. Order is the point: a sequence read twice from the same document has
// to come back the same way, so a map is walked by sorted key and a list in
// its own order.
func collect(inv emulator.Invariant, path string, v any) []any {
	if pathMatchesInvariant(inv, path) {
		return []any{v}
	}
	switch value := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []any
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			out = append(out, collect(inv, child, value[k])...)
		}
		return out
	case []any:
		var out []any
		for _, item := range value {
			out = append(out, collect(inv, path+"[]", item)...)
		}
		return out
	default:
		return nil
	}
}

func pathMatchesInvariant(inv emulator.Invariant, path string) bool {
	return path != "" && inv.Matches(inv.Operation, path)
}

func sameValues(want, got []any) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if !sameScalar(want[i], got[i]) {
			return false
		}
	}
	return true
}

func differing(want, got []any) int {
	n := 0
	for i := range want {
		if i >= len(got) || !sameScalar(want[i], got[i]) {
			n++
		}
	}
	if extra := len(got) - len(want); extra > 0 {
		n += extra
	}
	return n
}

// sameScalar compares two decoded JSON values without printing either.
func sameScalar(a, b any) bool {
	ja, ea := json.Marshal(a)
	jb, eb := json.Marshal(b)
	if ea != nil || eb != nil {
		return false
	}
	return bytes.Equal(ja, jb)
}

// reorder reports the order this emulator answered, as positions in the
// recorded sequence, and "" when the order held.
//
// Positions rather than values, and that is a hygiene rule rather than a style:
// the values here are the account's identifiers and addresses. "1,0" says
// exactly what went wrong and republishes nothing.
func reorder(want, got []any) (string, bool) {
	if len(want) != len(got) {
		return fmt.Sprintf("%d element(s) where the recording has %d", len(got), len(want)), true
	}
	at := map[string]int{}
	for i, v := range want {
		if raw, err := json.Marshal(v); err == nil {
			if _, seen := at[string(raw)]; !seen {
				at[string(raw)] = i
			}
		}
	}
	positions := make([]string, 0, len(got))
	same := true
	for i, v := range got {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		p, known := at[string(raw)]
		if !known {
			// An element the mapped recording does not carry at all. Either the
			// set genuinely differs — which the shape comparison owns — or no
			// earlier exchange bound this identifier, so the two sides are not
			// in the same namespace and there is nothing to compare. Reported as
			// not evaluated rather than as a pass: an order check that silently
			// ran on nothing is exactly the shape CLAUDE.md calls a comment
			// standing in for a control, and Report.Orders is what makes it
			// visible.
			return "", false
		}
		positions = append(positions, strconv.Itoa(p))
		if p != i {
			same = false
		}
	}
	if same {
		return "", true
	}
	return strings.Join(positions, ","), true
}

func recordedPositions(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = strconv.Itoa(i)
	}
	return strings.Join(parts, ",")
}

// jsonType names the JSON type of a decoded value, in the vocabulary
// internal/transcript already uses, so a replay finding and a shape catalogue
// say "number" about the same thing.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number, float64:
		return "number"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// bindings maps a value the recording carries to the one this emulator minted
// in its place.
//
// # One recorded value can mean two things, and the field name is what says which
//
// A recording is not a set of globally unique identifiers. On the account #352
// recorded, project_id and organization_id are the *same string* — a Scaleway
// account with one project spells both the same way — while this emulator mints
// two different ones. So one recorded value has two candidate bindings, and a
// map from value to value cannot hold both.
//
// The first version held only [bindings.to] and walked a Go map to fill it,
// which decided the question by iteration order: six replays of
// corpus/scaleway/scw-cli.jsonl against six fresh emulators graded
// vpc/v2/API.ListPrivateNetworks divergent three times and matched three times
// (corpus/README.md records the run). When the organisation won, the create
// filed its network under a project the unfiltered list does not cover.
//
// Two changes, and they answer two different questions:
//
//   - [bindings.byField] scopes a binding to the field name it was observed
//     under, so a recorded value substituted into project_id gets the project
//     this emulator minted and the same value substituted into organization_id
//     gets the organisation. This is reading the recording's own labelling
//     rather than guessing, and it is what makes a red run a defect.
//   - the map walk in [bindings.learn] is sorted, so a value with no field to
//     scope it — a path segment, a query parameter whose name never appeared as
//     a body field — resolves the same way on every run. Determinism first,
//     because a gate that answers differently on two identical inputs is a gate
//     that gets disarmed the first time it seems to lie.
//
// TestOneRecordedValueUnderTwoFieldsBindsByFieldName and
// TestTheSameRecordingBindsTheSameWayOnEveryRun fail without them.
type bindings struct {
	// to maps a recorded value to the one this emulator minted, ignoring where
	// it was seen. It is the fallback for the places that carry no field name —
	// a path segment, and a query parameter no body field ever named.
	to map[string]string
	// byField is the same map, scoped to the field name the pair was observed
	// under: byField["project_id"]["<recorded>"] is what this emulator answered
	// for project_id.
	byField map[string]map[string]string
	// ambiguous holds the recorded values that two field names bound
	// differently. Counted and reported rather than hidden: it is the honest
	// name for "this recording says less than the replay needs", and a run that
	// resolved one by field name should be able to say so.
	ambiguous map[string]struct{}
	// forced is [Options.Bind]: a field name, and the value every recorded
	// identifier at that field is sent as, whatever the endpoint has answered
	// so far. It wins over both maps above, because it is the caller stating
	// which account stands in for the recorded one rather than the replay
	// inferring it.
	forced map[string]string
}

func newBindings() *bindings {
	return &bindings{
		to:        map[string]string{},
		byField:   map[string]map[string]string{},
		ambiguous: map[string]struct{}{},
		forced:    map[string]string{},
	}
}

// substituting reports whether anything could be substituted at all. Both maps
// have to be consulted: a run seeded with [Options.Bind] and no answer yet has
// learned nothing, and testing only the learned map made every seed inert until
// the first identifier moved — which on a recording that opens with a create is
// never.
func (b *bindings) substituting() bool { return len(b.to) > 0 || len(b.forced) > 0 }

func (b *bindings) count() int { return len(b.to) }

func (b *bindings) ambiguities() int { return len(b.ambiguous) }

// learn walks the recorded answer and this emulator's beside it, and remembers
// every identifier that moved, under the field name it moved at.
//
// Only a differing pair binds, and only when the recorded side looks like an
// identifier. Both halves matter: a value the emulator answers identically
// needs no rebinding, and a state name or a status message that differs is not
// something a later request refers back to — substituting one would corrupt the
// request rather than repair it.
//
// field is the name of the JSON key this value sits under, "" at the root. A
// list passes its own field down to each element, because the elements of
// public_ips are still public_ips.
func (b *bindings) learn(field string, want, got any) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return
		}
		// Sorted rather than ranged, and this line is the determinism. Go
		// randomises map iteration on purpose, so the unsorted form let the
		// order of two sibling keys decide which of them claimed a value both
		// carried — the "8 or 9 divergences" of corpus/README.md.
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if peer, present := g[k]; present {
				b.learn(k, w[k], peer)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			return
		}
		for i := range w {
			if i < len(g) {
				b.learn(field, w[i], g[i])
			}
		}
	case string:
		g, ok := got.(string)
		if !ok || g == w || !looksMinted(w) {
			return
		}
		scoped := b.byField[field]
		if scoped == nil {
			scoped = map[string]string{}
			b.byField[field] = scoped
		}
		if _, bound := scoped[w]; !bound {
			scoped[w] = g
		}
		if previous, bound := b.to[w]; !bound {
			b.to[w] = g
		} else if previous != g {
			b.ambiguous[w] = struct{}{}
		}
	}
}

// resolve answers what this emulator minted for a recorded value, preferring
// what it minted for that value *under that field name*.
//
// The fallback is not a second guess, it is the case where there is no field to
// ask about: a path segment, or a query parameter whose name no body field
// carried.
func (b *bindings) resolve(field, value string) (string, bool) {
	// The seed, and only over an identifier. A field name the caller seeded is
	// still an ordinary field for everything else it may carry: `name` is a
	// free-text field on one operation and an identifier on none, and a seed
	// that rewrote free text would send a request the recording never made.
	if to, seeded := b.forced[field]; seeded && looksMinted(value) {
		return to, true
	}
	if to, bound := b.byField[field][value]; bound {
		return to, true
	}
	to, bound := b.to[value]
	return to, bound
}

// applyPath substitutes bound identifiers segment by segment. Whole segments
// only: a substring replacement inside a free-text segment would corrupt a
// request rather than repair it.
func (b *bindings) applyPath(path string) string {
	if !b.substituting() {
		return path
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if to, bound := b.resolve("", seg); bound {
			segments[i] = to
			continue
		}
		// Exoscale writes the verb in the identifier's own segment
		// ("{id}:start"), so the identifier is the part before the colon.
		if id, action, found := strings.Cut(seg, ":"); found {
			if to, bound := b.resolve("", id); bound {
				segments[i] = to + ":" + action
			}
		}
	}
	return strings.Join(segments, "/")
}

// applyQuery substitutes bound identifiers in parameter values, leaving the
// string byte-identical when none is bound — the same reason the proxy's
// redaction does: re-encoding through url.Values sorts and re-escapes, and the
// request that goes out would then not be the one that was recorded.
//
// The parameter name is the field name: `?project_id=<recorded>` asks the same
// question a body field project_id asks, and Scaleway's own list filters are
// spelled exactly like the body fields they filter on. Resolving them globally
// is what sent one list to the organisation and made the verdict flap.
func (b *bindings) applyQuery(raw string) string {
	if raw == "" || !b.substituting() {
		return raw
	}
	pairs := strings.Split(raw, "&")
	changed := false
	for i, pair := range pairs {
		name, value, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			continue
		}
		if to, bound := b.resolve(name, value); bound {
			pairs[i] = name + "=" + to
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return strings.Join(pairs, "&")
}

// applyValue substitutes bound identifiers in a decoded request body, under the
// field name each value sits at. Whole string values only, for the reason
// applyPath keeps to whole segments.
func (b *bindings) applyValue(field string, v any) any {
	if !b.substituting() {
		return v
	}
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, nested := range value {
			out[k] = b.applyValue(k, nested)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = b.applyValue(field, item)
		}
		return out
	case string:
		if to, bound := b.resolve(field, value); bound {
			return to
		}
		return value
	default:
		return v
	}
}

// looksMinted reports whether a recorded value is the kind of thing a cloud
// hands out and a later request refers back to.
//
// The question itself lives in internal/shape, which already owns "is this a
// UUID" for the same reason: internal/corpus asks it of the same values before
// deciding whether a transcript may be committed, and a sanitiser that
// recognised one identifier less than this replay would publish exactly the
// values the replay knows are identifiers.
func looksMinted(s string) bool { return shape.IsMintedIdentifier(s) }
