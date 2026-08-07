// Package trace holds the record of one HTTP exchange: what was asked, what was
// answered, and what the emulator thought of it.
//
// It is one shape with one owner, and that is the whole reason the package
// exists rather than a struct inside whichever feature needed it first. Two
// things in this repository record the same fact from opposite sides:
//
//   - the emulator's own ring buffer (internal/core/emulator/events.go), which
//     observes an exchange from inside the process answering it, and serves it
//     to the page over SSE and to a script over GET /_feint/trace;
//   - `feint proxy` (X-2, #72), which observes an exchange from outside, between
//     a real client and a real cloud, and writes it to a file as JSON Lines.
//
// If those two wrote different shapes, a trace pulled from a running emulator
// could not be replayed by `feint replay` (X-3, #73), a transcript recorded
// against a real cloud could not be rendered by the page, and there would be two
// formats to document instead of one. This repository has already paid for that
// mistake twice — most recently with internal/cli hand-copying the shape of
// /_feint/conformance, which made `feint status` report 0 for months — so the
// rule is applied before the second writer exists rather than after.
//
// The field names are the ones X-2 published: t, method, path, operation,
// status, ms, req, res. A writer that needs a field this type does not have adds
// it here.
//
// What the emulator's ring does not fill, and the proxy must: Req.Body and
// Res.Body. Keeping every request and response body of a night's worth of
// applies in memory is exactly the unbounded growth the ring exists to prevent,
// so the ring records the shape of the exchange and not its payload. A
// transcript written to a file has no such constraint, which is why the fields
// are here and empty rather than absent.
package trace

import "time"

// Exchange is one request and its answer.
type Exchange struct {
	// Seq is the position in the recording, from 1 and never reused. A reader
	// that sees it jump knows entries were dropped, which is the honest way to
	// bound a buffer. A file-backed transcript can leave it at zero.
	Seq int64 `json:"seq,omitempty"`
	// At is when the request was answered.
	At time.Time `json:"t"`
	// Method and Path are the request line. Path carries no query string, so
	// that two calls to the same operation read as the same line.
	Method string `json:"method"`
	Path   string `json:"path"`
	// Query is the raw query string, without the leading "?", empty when there
	// is none. Separate from Path because a replay has to reissue the request
	// exactly and a reader wants the line to stay short.
	Query string `json:"query,omitempty"`
	// Operation is the upstream operation this exchange addressed, in the SDK's
	// own naming ("instance/v1/API.ListServers").
	//
	// Empty means nobody could name it, and that is a finding rather than a gap:
	// inside the emulator it means no route is mounted for that path, and in a
	// proxy transcript it means a real client walked a route no pack claims.
	Operation string `json:"operation,omitempty"`
	// Provider is the pack that answered, when one did.
	Provider string `json:"provider,omitempty"`
	Status   int    `json:"status"`
	// Ms is how long the answer took, in milliseconds.
	Ms float64 `json:"ms"`
	// Mounted is false when nothing served the path. A reader shows "no route
	// mounted" rather than leaving a bare 404 to interpret, and X-4 (#74) reads
	// exactly these lines: a path a real client asked for that this emulator
	// does not serve.
	Mounted bool `json:"mounted"`
	// Unread names the fields this request carried that no handler read.
	//
	// Per exchange, not deduplicated per operation the way /_feint/conformance
	// reports it: the point here is which request carried it. It is the only
	// signal in this repository that names a causal defect — an argument the API
	// accepted and then ignored — and it was computed and displayed nowhere.
	Unread []string `json:"unread,omitempty"`
	// Violations names what the answer carried that the provider's own API
	// description does not define, when a contract is loaded.
	//
	// The other half of the verdict on one exchange: Unread is what the client
	// sent and we ignored, this is what we answered and the schema forbids.
	// Showing one without the other is an odd place to stop.
	Violations []string `json:"violations,omitempty"`
	// Req and Res carry the headers and body. The emulator's ring leaves them
	// nil — see the package comment — and a transcript fills them.
	Req *Message `json:"req,omitempty"`
	Res *Message `json:"res,omitempty"`
}

// Message is one side of an exchange.
//
// Headers are a map of name to value rather than to a list of values: a
// transcript is read by humans and by a replay that reissues one value per
// header, and no client of these three APIs sends a repeated header that
// matters. A recorder that meets one joins them.
type Message struct {
	Headers map[string]string `json:"headers,omitempty"`
	// Body is the decoded JSON body when it was JSON, so a transcript stays
	// readable and a replay can compare fields rather than bytes. A body that is
	// not JSON is recorded as a string.
	Body any `json:"body,omitempty"`
}
