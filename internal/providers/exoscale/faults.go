package exoscale

import (
	"net/http"
)

// What an injected failure looks like in Exoscale's dialect.
//
// The core decides when a call fails and with which status (internal/core/
// emulator/faults.go); this file decides what the client decodes. Exoscale is
// the easy one of the three, and the reason is worth stating: their API answers
// every error through a single envelope with a bare `message` field, which is
// what the pack's own writeError already emits and what contracts/exoscale.json
// records as the document's errorSchema (egoscale.APIError, `message`
// required). There is no type key, no code, no per-error struct — so there is
// nothing here that could be invented, and every status renders the same way.
//
// That also means the only thing an Exoscale client can branch on for an
// injected fault is the HTTP status, which is what egoscale does: its generated
// client compares the status and wraps the decoded message. So the message says
// plainly that a rule is armed, and the status carries the meaning.

// FaultStatuses implements emulator.Faulter. The same set as the other two
// packs — every one of them renders through the single envelope, so the list is
// not a capability question here, only a statement of what the core may ask
// for. 408 is absent for the reason the other packs give: a request timeout is
// what a rule's `delay` produces, not a status this side chooses.
func (p *Pack) FaultStatuses() []int {
	return []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	}
}

// WriteFault implements emulator.Faulter.
func (p *Pack) WriteFault(w http.ResponseWriter, _ *http.Request, status int) {
	writeError(w, status, "feint answered "+http.StatusText(status)+
		" because a fault rule is armed for this operation; GET /_feint/faults lists them")
}
