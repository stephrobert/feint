package outscale

import (
	"net/http"
)

// What an injected failure looks like in Outscale's dialect.
//
// The core decides when a call fails and with which status (internal/core/
// emulator/faults.go); this file decides what the client decodes. The envelope
// is the one errors.go already documents — an Errors array of {Code, Type,
// Details} beside the usual ResponseContext, which is ErrorResponse in the SDK
// — so nothing new is shaped here. What is decided here is which Code and which
// Type each injected status carries, and the same discipline errors.go states
// applies: the number is chosen to land in the range the SDK's own helpers
// branch on, because that is what client code actually reads.
//
// pkg/osc/errors.go is the whole grounding, and it is unusually good grounding
// because the helpers are predicates a consumer calls by name:
//
//	IsNotFound          5000-5999
//	IsConflict          6000-6999, 9000-9999
//	IsQuotaOrCapacity   10000-10999
//	IsAuthError         an explicit list: 1, 5, 7, 14, 20, 4120, 4000
//
// So a 401 or a 403 carries 4120, and osc.IsAuthError answers true on it while
// osc.IsNotFound answers false — which is exactly the distinction #356 needs a
// client to be able to make: "I am not allowed" is not "it does not exist".
//
// For 429 and the 5xx family no helper classifies anything, so the Code is
// empty — the same "not one of theirs" the pack's NotFound already answers with,
// and for the same reason: minting a number in an unclassified range would put a
// value in a client's hands that no upstream check matches and that nobody
// measured. What a client branches on for those statuses is the status, through
// go-retryablehttp, which the SDK wires in pkg/middleware/retry/retry.go.
//
// The Type strings are Outscale's own vocabulary where the SDK publishes one:
// pkg/oks/api.yaml declares ForbiddenError, InternalError, TimeoutError,
// NotFoundError, ResourceConflict and four more as the Type of its error
// examples. Where it declares none — throttling, service unavailable — the Type
// says the answer is the emulator's, which is the same decision the Scaleway
// pack makes about its `type` field and for the same reason.

const (
	// codeAuthDenied is in osc.IsAuthError's explicit list, so a client asking
	// "was I refused for lack of a grant" gets a true answer.
	codeAuthDenied = "4120"

	// typeForbidden and typeInternal come from the SDK's own error vocabulary
	// in pkg/oks/api.yaml.
	typeForbidden = "ForbiddenError"
	typeInternal  = "InternalError"
	// typeInjected is for the statuses that vocabulary does not name. Visibly
	// this emulator's, so nobody reads it as an Outscale constant.
	typeInjected = "FeintInjectedFault"
)

// FaultStatuses implements emulator.Faulter. Same set as the other two packs,
// and for the same reasons: 401 and 403 are the refusals #356 needs observable,
// 429 and the 5xx family are what #26 was written about, and 408 is absent
// because a request timeout is produced by a rule's `delay`, not by a status.
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
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		p.writeError(w, status, codeAuthDenied, typeForbidden,
			"feint answered "+http.StatusText(status)+" because a fault rule is armed for this "+
				"operation; GET /_feint/faults lists them")
	case http.StatusInternalServerError:
		p.writeError(w, status, "", typeInternal,
			"feint answered "+http.StatusText(status)+" because a fault rule is armed for this "+
				"operation; GET /_feint/faults lists them")
	default:
		p.writeError(w, status, "", typeInjected,
			"feint answered "+http.StatusText(status)+" because a fault rule is armed for this "+
				"operation; GET /_feint/faults lists them")
	}
}
