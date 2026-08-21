package scaleway

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// What an injected failure looks like in Scaleway's dialect.
//
// The core decides when a call fails and with which status (internal/core/
// emulator/faults.go); this file is the other half — what the client actually
// decodes. Everything here is read off scaleway-sdk-go/scw/errors.go, because a
// body that does not match it turns a typed error into an opaque one and every
// caller branching on errors.As stops working.
//
// The dispatch is the whole mechanism: hasResponseError reads Content-Type,
// unmarshals into ResponseError, and hands the body to unmarshalStandardError,
// which switches on the `type` field into one custom struct per known type.
// So `type` is not decoration, it is the key, and there are exactly two kinds
// of value it can honestly carry here:
//
//   - a type the SDK's switch names. Then the client gets the typed error its
//     own code was written against, and this emulator is telling the truth
//     about Scaleway. 401 and 403 are in that case, and they are the ones #356
//     is about: a consumer must be able to tell "I am not allowed" from "it
//     does not exist" from "try again later".
//   - a type the SDK's switch does not name, which falls through to a plain
//     *ResponseError carrying the status — which is exactly what a client sees
//     for any type this SDK generation has not learned yet.
//
// For 429 and the 5xx family the SDK names no type at all, so any Scaleway-ish
// value invented here would be this project publishing a fact about Scaleway
// that nobody measured — rule 4 forbidding an invented value as much as an
// invented shape. The type is therefore visibly the emulator's own, the
// fall-through path is the same one a genuine unknown type takes, and the
// client's behaviour on these statuses is decided by the status, which is the
// thing being exercised. TestScalewayFaultsCarryTheTypeTheSDKDispatchesOn and
// TestEveryPackRendersItsFaultsInItsOwnDialect hold both halves.
//
// The Content-Type matters more than the body: hasResponseError drops the body
// entirely for anything that is not application/json and sets the message to
// the bare HTTP status. emulator.WriteJSON sets it.

// faultTypeInjected is the `type` for a status the SDK's dispatch does not name.
//
// Deliberately not a Scaleway spelling, and deliberately not one the SDK maps —
// the same decision, for the same reason, as writeNotEmulated's "not_emulated"
// in errors.go: an unknown type falls through to *ResponseError, which is what
// this is. A reader of the body can see the value did not come from Scaleway.
const faultTypeInjected = "feint_injected_fault"

// deniedAuthentication mirrors scw.DeniedAuthenticationError, field for field.
// It is a type of its own rather than a reuse of APIError because the SDK's
// `details` key carries a different item shape per error type, and a struct
// that tried to hold all of them would be a shape no error actually has.
type deniedAuthentication struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Method  string `json:"method"`
	Reason  string `json:"reason"`
}

// permissionsDenied mirrors scw.PermissionsDeniedError: `details` is an array
// of {resource, action}, which is not the {argument_name, reason} that
// invalid_arguments puts under the same key.
type permissionsDenied struct {
	Type    string             `json:"type"`
	Message string             `json:"message,omitempty"`
	Details []permissionDetail `json:"details"`
}

type permissionDetail struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

// FaultStatuses implements emulator.Faulter.
//
// 401 and 403 are the refusals #356 needs observable; 429, 500, 502 and 503 are
// the transient family #26 was written about. 408 is absent on purpose: a
// request timeout is a client-side deadline, and what this emulator can produce
// is the delay that causes one, which a rule expresses with `delay` alone.
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
func (p *Pack) WriteFault(w http.ResponseWriter, r *http.Request, status int) {
	switch status {
	case http.StatusUnauthorized:
		// DeniedAuthenticationError{Method, Reason}. Both values are taken from
		// the SDK's own switch in DeniedAuthenticationError.Error(): Method is
		// one of "unknown_method", "jwt", "api_key" and Reason one of
		// "unknown_reason", "invalid_argument", "not_found", "expired". An API
		// key the API does not know is what a junk token is, so api_key and
		// not_found are the pair, and the client prints "API key does not
		// exist" rather than an untyped HTTP error.
		emulator.WriteJSON(w, status, deniedAuthentication{
			Type:    "denied_authentication",
			Message: "denied authentication",
			Method:  "api_key",
			Reason:  "not_found",
		})
	case http.StatusForbidden:
		// PermissionsDeniedError{Details: []{Resource, Action}}. The two fields
		// are free-form strings in the SDK, and what fills them here is the
		// request that was refused: no value is claimed to be a Scaleway
		// constant, and errors.As(&PermissionsDeniedError{}) matches — which is
		// the whole of what a consumer needs to degrade the right control
		// instead of concluding the resource is absent.
		emulator.WriteJSON(w, status, permissionsDenied{
			Type:    "permissions_denied",
			Message: "insufficient permissions",
			Details: []permissionDetail{{
				Resource: r.URL.Path,
				Action:   r.Method,
			}},
		})
	default:
		emulator.WriteJSON(w, status, APIError{
			Type:    faultTypeInjected,
			Message: "feint answered " + http.StatusText(status) + " because a fault rule is armed for this operation; GET /_feint/faults lists them",
		})
	}
}
