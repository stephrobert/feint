// Package probe drives every mounted route from the provider's own API
// description, and checks what comes back against it.
//
// It exists because writing one conformance case per operation does not scale.
// The suites under tools/conformance/ drive the real clients and assert
// behaviour — the address answers, the firewall filters, a second Terraform plan
// is empty — and each of their assertions is written by hand. There are around
// 150 operations still to serve across three providers, so the protocol half of
// that work has to come from somewhere else.
//
// The contract already holds it: for each operation, a path, a method, a request
// schema and a response schema. That is enough to build a valid call and to
// judge the answer.
//
// # What this proves, and what it does not
//
// A probe proves the protocol: the route answers, and its answer matches the
// schema the provider publishes. Both halves of the protocol: a success is
// validated against the operation's response schema, and a refusal against the
// error shape the provider declares (Doc.ErrorSchema) — errors are the half a
// real client's retry loop actually decodes, and a 4xx nobody read used to
// count here as if somebody had (#156). Where the contract declares a
// page-size parameter, the operation is called with it too, and the answer is
// held to the bound it asked. Cursors and filters are not exercised, and
// nothing pretends they were.
//
// It proves nothing about behaviour. An emulator that returned a well-shaped
// empty object for everything would pass every probe, and that is precisely
// the failure of the emulators this project measures itself against.
//
// So the two are counted apart and never added together. The emulator marks
// synthetic requests with emulator.ProbeHeader, keeps a separate counter, and
// computes the backlog of unproven routes from real client calls alone. Probing
// a route never takes it off that list. The probe is a check that fails, not a
// score that rises.
//
// # Where it refuses to guess
//
// An identifier is never invented. Values come from what earlier calls in the
// plan returned, and an operation needing one that nothing produces makes the
// planning fail with a message naming it. Inventing an id would be an invented
// format dressed up as automation, which is the one thing this project never
// does: a shape comes from the provider's own SDK or not at all.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
)

// Runner drives one provider's routes against a running emulator.
type Runner struct {
	// Doc is the provider's contract.
	Doc *contract.Doc
	// Base is the emulator's address, e.g. "http://127.0.0.1:4599".
	Base string
	// Client is the HTTP client to use. Nil means a default with a short
	// timeout: every call here is local and answers immediately or is stuck.
	Client *http.Client
}

// Result is what one probed operation produced.
type Result struct {
	// Operation is the route's declared operation name.
	Operation string
	// Status is the HTTP status the emulator answered.
	Status int
	// Refused says the emulator declined the synthetic call with a 4xx, and
	// the refusal was read: its body validated against the error shape the
	// provider declares (Doc.ErrorSchema). Refusing is not a failure — the
	// emulator is entitled to be stricter than the schema, and often is on
	// purpose: it refuses to generate a keypair rather than hand out a private
	// key that only looks usable. Refusing in the wrong shape is: a 4xx a real
	// client cannot decode is precisely the answer #74 measured (text/plain
	// "404 page not found" where a Scaleway error envelope was due), and it
	// lands in Violations and Failures like any other bad answer.
	// TestARefusedProbeIsReadAgainstTheErrorShape fails without the
	// validation.
	Refused bool
	// RefusalUnread says the refusal could not be held to anything, because
	// the contract declares no error shape. Not a failure — there is nothing
	// upstream to disagree with — but not a validation either, and it must
	// never be counted as one: that silence is the defect this field exists to
	// name (TestAnUnreadRefusalIsNotAValidation).
	RefusalUnread bool
	// PageSized names the page-size parameter this operation's paged call
	// exercised, empty when the contract declares none. The answer to that
	// call was validated like any other and additionally held to the bound it
	// asked (TestAnAnswerIgnoringPageSizeIsAViolation).
	PageSized string
	// Skipped says why the operation was not called, empty when it was.
	Skipped string
	// Violations are the ways the response disagreed with the contract.
	Violations contract.Violations
	// Err is a transport or decoding failure.
	Err error
}

// Report is what a run produced.
type Report struct {
	Provider string
	Results  []Result
}

// Probed counts the operations that were called and answered a valid response.
func (r Report) Probed() int {
	n := 0
	for _, res := range r.Results {
		if res.Skipped == "" && !res.Refused && res.Err == nil &&
			len(res.Violations) == 0 && res.Status < 300 {
			n++
		}
	}
	return n
}

// Failures are the results a run must fail on: a violated contract, a transport
// error, or a status the operation should not have answered.
//
// A skip is not a failure. Refusing to call an operation whose identifiers
// nothing produces is the honest outcome, and it is reported as such. A
// refusal is not a failure either — unless it came in the wrong shape or in no
// shape at all, in which case its Violations or Err make it one, the same as
// any success that disagreed with the contract.
func (r Report) Failures() []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Skipped != "" {
			continue
		}
		if res.Err != nil || len(res.Violations) > 0 {
			out = append(out, res)
			continue
		}
		if !res.Refused && res.Status >= 300 {
			out = append(out, res)
		}
	}
	return out
}

// Proven counts the operations that were actually called and answered.
//
// It exists because Failures() alone cannot fail on the one regression that
// matters most to a probe: a change that makes every synthetic body rejected
// turns every result into a refusal, Failures() returns nothing, and the run is
// green with nothing measured. "0 probed, 0 failed" and "34 probed, 0 failed"
// printed the same verdict.
func (r Report) Proven() int {
	count := 0
	for _, res := range r.Results {
		if res.Skipped == "" && !res.Refused && res.Err == nil && res.Status < 300 {
			count++
		}
	}
	return count
}

// Barren reports a run that proved nothing at all despite having work to do.
//
// A provider whose every route is skipped for want of identifiers is not barren:
// nothing was attempted, and the plan says so. A provider whose every attempt
// came back refused is — something answers, and it answers no to everything,
// which is a broken emulator or a broken probe and never a healthy run.
func (r Report) Barren() bool {
	attempted := 0
	for _, res := range r.Results {
		if res.Skipped == "" {
			attempted++
		}
	}
	return attempted > 0 && r.Proven() == 0
}

// Run executes the plan and returns what each step produced.
func (r *Runner) Run(ctx context.Context, routes []contract.MountedRoute) (Report, error) {
	plan, err := Plan(r.Doc, routes)
	if err != nil {
		return Report{}, err
	}

	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	pool := newPool()
	report := Report{Provider: r.Doc.Provider}
	for _, step := range plan {
		report.Results = append(report.Results, r.call(ctx, client, step, pool))
	}
	return report, nil
}

func (r *Runner) call(ctx context.Context, client *http.Client, step Step, pool *pool) Result {
	result := Result{Operation: step.Operation}
	if step.Skip != "" {
		result.Skipped = step.Skip
		return result
	}

	body, err := minimalBody(r.Doc, step.Request, pool)
	if err != nil {
		// Not a failure: the contract does not let us build this call honestly,
		// and inventing the missing value is the one thing this must not do.
		result.Skipped = err.Error()
		return result
	}

	path, err := pool.fill(step.Path)
	if err != nil {
		result.Skipped = err.Error()
		return result
	}

	status, decoded, err := r.exchange(ctx, client, step, path, "", body)
	result.Status = status
	var refused refusal
	switch {
	case err == nil:
	case errors.As(err, &refused):
		result.Refused = true
		result.RefusalUnread = refused.unread
		result.Violations = refused.violations
		result.Err = refused.err
		return result
	default:
		result.Err = err
		return result
	}
	if status >= 300 || decoded == nil {
		return result
	}
	result.Violations = r.Doc.ValidateResponse(step.Contract, decoded)

	// Whatever came back feeds the calls that follow: this is how a create finds
	// the identifier a delete needs, without a scenario written by hand.
	pool.harvest(decoded)

	if len(result.Violations) > 0 {
		return result
	}
	r.paged(ctx, client, step, path, body, &result)
	return result
}

// pageAsked is the page size every paged call asks for. One, because it is the
// only value with a bound the emptiest inventory can still violate: an answer
// with two items has ignored it whatever the account holds.
const pageAsked = 1

// paged makes the operation's second call — the same call, carrying the
// page-size parameter the contract declares — and holds the answer to the
// bound it asked, on top of the schema.
//
// Nothing else in the request is invented: the parameter's name comes from the
// document (per_page in Scaleway's query, ResultsPerPage in Outscale's request
// body), and everything but that one addition is the exact call that just
// succeeded. An operation whose contract declares no page size makes no second
// call, and its result says so by leaving PageSized empty: what was not
// exercised is not counted (TestAnAnswerIgnoringPageSizeIsAViolation fails
// without the bound check, and without the call there is nothing to check).
//
// Cursors (page, NextPageToken) and filters are deliberately not exercised: a
// cursor's value would have to be invented, and a filter's verdict depends on
// inventory this probe does not control. The evidence legend says so.
func (r *Runner) paged(ctx context.Context, client *http.Client, step Step, path string, body map[string]any, result *Result) {
	op := r.Doc.Operations[step.Contract]
	name, inBody, declared := r.Doc.PageSize(op)
	if !declared {
		return
	}

	query := ""
	if inBody {
		sized := make(map[string]any, len(body)+1)
		for k, v := range body {
			sized[k] = v
		}
		sized[name] = pageAsked
		body = sized
	} else {
		query = fmt.Sprintf("%s=%d", name, pageAsked)
	}

	result.PageSized = name
	status, decoded, err := r.exchange(ctx, client, step, path, query, body)
	if err != nil || status >= 300 {
		// The plain call just answered this same request without the one
		// parameter the document declares. Refusing or erroring on it is the
		// parameter not being served, and that is a finding, not a pass.
		result.Violations = append(result.Violations, contract.Violation{
			Path:   name,
			Reason: fmt.Sprintf("the same call with %s=%d answered %d (%v) where the contract declares the parameter", name, pageAsked, status, err),
		})
		return
	}
	result.Violations = append(result.Violations, r.Doc.ValidateResponse(step.Contract, decoded)...)
	result.Violations = append(result.Violations, r.Doc.PageBound(op, pageAsked, decoded)...)
}

// refusal is how exchange reports a 4xx: read, and validated when the contract
// gives it a shape to be validated against.
type refusal struct {
	// unread says no error shape is declared, so nothing was checked.
	unread bool
	// violations is how the body disagreed with the declared error shape.
	violations contract.Violations
	// err is a refusal body that could not even be decoded.
	err error
}

func (refusal) Error() string { return "refused" }

// exchange makes one HTTP call and decodes what came back. A 4xx returns a
// refusal carrying the error-shape verdict; any other status returns the
// decoded body, nil when there is none.
func (r *Runner) exchange(ctx context.Context, client *http.Client, step Step, path, query string, body map[string]any) (int, any, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("encode body: %w", err)
	}
	url := strings.TrimRight(r.Base, "/") + r.Doc.PathPrefix + path
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, step.Method, url, bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// What keeps this out of the number that counts.
	req.Header.Set(emulator.ProbeHeader, "1")

	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, nil, fmt.Errorf("read body: %w", err)
	}
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		return res.StatusCode, nil, r.readRefusal(raw)
	}
	if len(raw) == 0 {
		return res.StatusCode, nil, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return res.StatusCode, nil, fmt.Errorf("response is not JSON: %w", err)
	}
	return res.StatusCode, decoded, nil
}

// readRefusal validates a 4xx body against the error shape the provider
// declares. Before this existed the body was read and discarded, and a
// text/plain "404 page not found" — the exact answer #74 measured on an
// unmounted corner — counted the same as a typed error envelope.
func (r *Runner) readRefusal(raw []byte) refusal {
	if r.Doc.ErrorSchema == "" {
		return refusal{unread: true}
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return refusal{err: fmt.Errorf("the refusal is not JSON: %w", err)}
	}
	return refusal{violations: r.Doc.Validate(r.Doc.ErrorSchema, decoded)}
}

// String renders a report the way the conformance scripts print theirs.
//
// Refusals are two numbers, not one: a refusal read and validated against the
// declared error shape, and a refusal nothing could be checked against. Folding
// them back into one figure would repeat the defect #156 names — a 4xx nobody
// read, printed as if somebody had.
func (r Report) String() string {
	lines := make([]string, 0, len(r.Results)+1)
	probed, paged, skipped, refused, unread := 0, 0, 0, 0, 0
	for _, res := range r.Results {
		switch {
		case res.Skipped != "":
			skipped++
		case res.Err != nil:
			lines = append(lines, "  FAIL "+res.Operation+": "+res.Err.Error())
		case len(res.Violations) > 0:
			lines = append(lines, "  FAIL "+res.Operation+": "+res.Violations.Error())
		case res.Refused && res.RefusalUnread:
			unread++
		case res.Refused:
			refused++
		case res.Status >= 300:
			lines = append(lines, fmt.Sprintf("  FAIL %s: answered %d", res.Operation, res.Status))
		default:
			probed++
			if res.PageSized != "" {
				paged++
			}
		}
	}
	sort.Strings(lines)
	head := fmt.Sprintf("probe %s: %d probed (%d page-sized), %d refused and read, %d refused unread, %d skipped, %d failed",
		r.Provider, probed, paged, refused, unread, skipped, len(lines))
	return strings.Join(append([]string{head}, lines...), "\n")
}
