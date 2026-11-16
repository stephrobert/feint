package outscale

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/stephrobert/feint/internal/core/emulator"
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
// that is deliberate: ReadLoadBalancersRequest declares neither ResultsPerPage
// nor NextPageToken (api.yaml lists DryRun and Filters, additionalProperties
// false), so the handler reads no page size at all and returns the whole list.
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
// which paginate reads as "no limit". Every value it can return other than zero
// has already passed refusePageSize.
func pageSize(size *int) int {
	if size == nil {
		return 0
	}
	return *size
}

// errPageToken is the refusal for a continuation token this emulator did not
// issue. Distinct from "no token", because folding the two is what turns a
// refused page into a page that says the inventory ended.
var errPageToken = errors.New("NextPageToken is not one this emulator issued")

// encodePageToken names the page after the one served, by the offset of its
// first row. The spelling is opaque to clients, as the vendor's is: a client
// echoes it back and nothing else.
//
// Standard padded base64, because that is what every response schema declares
// for the field: `NextPageToken` is `type: string, format: byte` in the SDK's
// api.yaml, which is base64.StdEncoding's output. A raw URL encoding would
// answer "Mg" where a client generated with byte semantics decodes and
// re-encodes "Mg==" on the next request, and this decoder would then refuse the
// page it had just issued.
//
// An offset, not a snapshot, so a page costs nothing to remember. What it does
// not carry is the filter set the offset was computed against, so a client that
// changes its filters between pages reads a different window. That is the
// client's error on a stateless token, and the alternative — a token naming a
// stored page — would make a read accumulate state this emulator then has to
// expire.
func encodePageToken(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// decodePageToken reads a token this package wrote, and nothing else. The
// re-encoding check refuses alternate spellings of the same number, so a token
// has one form and a test can assert on the bytes.
func decodePageToken(token string) (int, error) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0, errPageToken
	}
	text := string(raw)
	offset, err := strconv.Atoi(text)
	if err != nil || offset < 0 || strconv.Itoa(offset) != text {
		return 0, errPageToken
	}
	return offset, nil
}

// paginate applies the page size and the continuation token one Read* request
// carries, and answers the rows to serve with the token naming the page after
// them (empty on the last page).
//
// It exists because the pack used to do neither. It truncated to
// ResultsPerPage and answered no NextPageToken, so a client that asked for two
// rows out of three was told two was the whole inventory — the silent subset
// this repository refuses everywhere else. And the request schemas declare
// NextPageToken on the same operations, which the pack read nowhere: a client
// that sent one was served the first page again, so a paginating client could
// not reach the second page at all.
//
// A token past the end is an empty page with no token rather than an error: the
// page exists and holds nothing, which is what a client that paged past the end
// asked for.
func paginate[T any](rows []T, size *int, token *string) ([]T, string, error) {
	offset := 0
	if token != nil && *token != "" {
		decoded, err := decodePageToken(*token)
		if err != nil {
			return nil, "", err
		}
		offset = decoded
	}
	if offset > len(rows) {
		return rows[:0], "", nil
	}
	rest := rows[offset:]
	limit := pageSize(size)
	if limit <= 0 || len(rest) <= limit {
		return rest, "", nil
	}
	return rest[:limit], encodePageToken(offset + limit), nil
}

// writePaged answers one Read* collection with the page its request asked for.
//
// NextPageToken is absent rather than empty on the last page. A client loops
// while the key is present — octl does exactly that, which is how the token was
// measured against a real client — and osc-sdk-go itself carries no paginator,
// so an empty string would be a token a client can follow for ever.
func writePaged[T any](p *Pack, w http.ResponseWriter, collection string, rows []T, size *int, token *string) {
	served, next, err := paginate(rows, size, token)
	if err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if served == nil {
		// A nil slice marshals to null, and a client ranging over null is a
		// client that stops.
		served = make([]T, 0)
	}
	body := map[string]any{collection: served, "ResponseContext": p.context()}
	if next != "" {
		body["NextPageToken"] = next
	}
	emulator.WriteJSON(w, http.StatusOK, body)
}
