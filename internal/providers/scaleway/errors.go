package scaleway

import (
	"fmt"
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The Scaleway SDK decodes errors by reading the "type" field of the body and
// unmarshalling into a matching struct (scw/custom_errors.go). Emitting the
// wrong shape turns a clean typed error into an opaque one, and callers that
// branch on errors.As stop working. Field names below mirror the SDK structs.

// APIError is the wire shape of a Scaleway error.
type APIError struct {
	Type         string          `json:"type"`
	Message      string          `json:"message,omitempty"`
	Resource     string          `json:"resource,omitempty"`
	ResourceID   string          `json:"resource_id,omitempty"`
	CurrentState string          `json:"current_state,omitempty"`
	Precondition string          `json:"precondition,omitempty"`
	HelpMessage  string          `json:"help_message,omitempty"`
	Details      []ArgumentError `json:"details,omitempty"`
}

// ArgumentError is one entry of an invalid_arguments error.
type ArgumentError struct {
	ArgumentName string `json:"argument_name"`
	Reason       string `json:"reason"`
	HelpMessage  string `json:"help_message,omitempty"`
}

func writeNotFound(w http.ResponseWriter, kind, id string) {
	emulator.WriteJSON(w, http.StatusNotFound, APIError{
		Type:       "not_found",
		Message:    "resource is not found",
		Resource:   kind,
		ResourceID: id,
	})
}

func writeInvalidArguments(w http.ResponseWriter, details ...ArgumentError) {
	emulator.WriteJSON(w, http.StatusBadRequest, APIError{
		Type:    "invalid_arguments",
		Message: "invalid argument(s)",
		Details: details,
	})
}

// writeNotEmulated answers a request that landed in Scaleway's URL space with no
// route to serve it.
//
// The type is deliberately not one the SDK knows. unmarshalStandardError maps a
// recognised type onto a typed error, so answering "not_found" would tell a
// caller a resource is missing when the truth is that an operation is not
// served, and errors.As(&ResourceNotFoundError{}) would agree. An unknown type
// falls through to a plain *ResponseError carrying the type and the message,
// which is exactly what this is.
//
// What matters most is the content type. The SDK reads it first: anything that
// is not application/json makes it drop the body and set the message to
// res.Status, so today a caller hitting an unmounted route gets "404 Not Found"
// and nothing else.
func writeNotEmulated(w http.ResponseWriter, path string) {
	emulator.WriteJSON(w, http.StatusNotImplemented, APIError{
		Type:    "not_emulated",
		Message: "feint does not serve " + path + "; see /_feint/routes for what it does",
	})
}

func writeTransientState(w http.ResponseWriter, kind, id, state string) {
	emulator.WriteJSON(w, http.StatusConflict, APIError{
		Type:         "transient_state",
		Message:      "resource is in a transient state",
		Resource:     kind,
		ResourceID:   id,
		CurrentState: state,
	})
}

// writePreconditionFailed answers an action the server's own condition forbids.
//
// Body and status are copied from a measurement against fr-par-1 rather than
// derived from the SDK, which gives the shape of the error but never says which
// one an endpoint picks. Four actions on a protected server answered, verbatim:
//
//	400 {"type": "precondition_failed", "message": "precondition is not
//	     respected", "precondition": "protected_resource",
//	     "help_message": "server is protected"}
//
// 400 and not 412, which is the status the name would suggest and the reason
// this is worth a comment.
func writePreconditionFailed(w http.ResponseWriter, precondition, help string) {
	emulator.WriteJSON(w, http.StatusBadRequest, APIError{
		Type:         "precondition_failed",
		Message:      "precondition is not respected",
		Precondition: precondition,
		HelpMessage:  help,
	})
}

// writeParseFailure answers a query value the route could not read, in the
// dialect the newer products use for it (#630).
//
// Measured against fr-par on 2026-09-01, because the two shapes are not one and
// choosing between them by taste would be inventing a format:
//
//	ipam/v1, vpc/v2, vpc-gw/v2, lb/v1
//	  400 {"message":"parsing field \"attached\": strconv.ParseBool: parsing
//	       \"banana\": invalid syntax"}
//	instance/v1
//	  400 {"type":"invalid_arguments", …, "details":[{"argument_name":"public",
//	       "reason":"format","help_message":"expected boolean"}]}
//
// So instance/v1 keeps writeInvalidArguments and everything else comes here. The
// message is not copied: the real API is a Go service surfacing strconv's own
// error, and this pack parses with the same function, so passing err through
// reproduces it exactly — including the day Go rewords it.
//
// No "type" key at all, which is why this does not go through APIError: the
// field has no omitempty and an empty one would be a shape the cloud never
// answers.
func writeParseFailure(w http.ResponseWriter, field string, err error) {
	emulator.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"message": fmt.Sprintf("parsing field %q: %s", field, err),
	})
}
