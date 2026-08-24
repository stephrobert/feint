package outscale

import (
	"fmt"
	"net/http"
)

// Pagination, and the one bound Outscale states about it.
//
// Twenty-one Read* request schemas of their own API description declare
// ResultsPerPage, and all twenty-one describe it in the same words: "The
// maximum number of logs returned in a single response (between `1` and
// `1000`, both included)" (outscale.yaml, osc-api 1.42.0). So the bound is the
// vendor's, not a number chosen here, and a value outside it is a request the
// real API refuses.
//
// This pack used to take any value and treat everything below one as "no
// limit", so `ResultsPerPage: 0` — which the API refuses — was answered with
// the whole inventory. That is the same family as the filter that was ignored
// rather than applied (filters.go): an argument the client sent, the server
// did not honour, and nobody was told.
//
// The bound lives here rather than in each handler for the reason
// refuseUnsupported does: a control copied into twenty handlers is a control
// one handler forgets, and the twenty-first to be written would forget it too.
//
// ReadLoadBalancers is the one Read* of this pack that is NOT bounded here, and
// that is deliberate: ReadLoadBalancersRequest declares no ResultsPerPage at
// all, so asserting a bound on it would be asserting something the API does not
// say. The field it reads is dead — no supported client can send it — and it is
// left alone rather than given a bound nobody published.
const (
	resultsPerPageMin = 1
	resultsPerPageMax = 1000
)

// refusePageSize answers the client when ResultsPerPage is outside the bound
// the API declares, and reports whether it did.
//
// The parameter is a pointer because zero is a value a client can send and the
// API refuses. Decoded into an int, `ResultsPerPage: 0` is indistinguishable
// from a request that never mentioned the field, and the handler would accept
// exactly the value the real cloud rejects — a hole shaped like the Go zero
// value, which is why the shape of the field is part of the fix.
//
// TestAPageSizeOutsideTheBoundIsRefused and
// TestAnAbsentPageSizeIsNotAZeroPageSize fail without this.
func (p *Pack) refusePageSize(w http.ResponseWriter, size *int) bool {
	if size == nil || (*size >= resultsPerPageMin && *size <= resultsPerPageMax) {
		return false
	}
	p.badRequest(w, fmt.Sprintf(
		"ResultsPerPage is %d; this call takes a value between %d and %d, both included",
		*size, resultsPerPageMin, resultsPerPageMax))
	return true
}

// pageSize is the size to truncate to: zero when the client asked for none,
// which page reads as "no limit". Every value it can return other than zero has
// already passed refusePageSize.
func pageSize(size *int) int {
	if size == nil {
		return 0
	}
	return *size
}

// page truncates a list to the size the client asked for.
//
// No NextPageToken is issued: this emulator holds a handful of resources, so
// there is never a second page to fetch, and a token pointing at nothing is
// worse than none. What matters is that a client asking for N rows is not
// handed more — the field was declared and unread, which is the shape that told
// a client its page size was honoured when it was not.
//
// TestResultsPerPageIsHonoured fails without this.
func page[T any](rows []T, size int) []T {
	if size <= 0 || len(rows) <= size {
		return rows
	}
	return rows[:size]
}
