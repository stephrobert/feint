package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// Outscale errors are an Errors array of {Code, Type, Details} beside the usual
// ResponseContext. The shape is ErrorResponse in the SDK; the field names below
// mirror it exactly, because a client that cannot decode an error reports a
// parsing failure and sends whoever reads it looking in the wrong place.
//
// The numbers are the part that cannot be read from the SDK. What the SDK does
// pin down is the ranges its own helpers branch on, in pkg/osc/errors.go:
// IsNotFound is 5000-5999, IsConflict is 6000-6999 or 9000-9999, IsQuotaOrCapacity
// is 10000-10999, and IsAuthError is an explicit list. So the constants below
// are chosen to land in the range that makes those helpers answer correctly,
// which is what client code actually branches on. The exact number inside a
// range is not verifiable without an account, and is not what anything tests.

const (
	// codeInvalidParameter covers a malformed or missing argument. No SDK helper
	// classifies this range, so the number carries no behaviour.
	codeInvalidParameter = "4001"
	// codeInvalidIDPrefix is the one code of this file read off the wire rather
	// than chosen for a range: corpus/outscale/oapi-cli-refusals.jsonl,
	// 2026-08-21, ReadVms with a VmIds value that is not an identifier answers
	// 400 and Errors [{Code 4104, Type InvalidParameterValue, Details "the
	// provided value does not respect the expected ID prefix"}] (#396).
	codeInvalidIDPrefix = "4104"
	// codeResourceNotFound sits in 5000-5999 so osc.IsNotFound reports true.
	codeResourceNotFound = "5063"
	// codeImageDoesNotExist is what the real cloud answers CreateVms on an
	// ImageId that names nothing, read off the wire: 400, InvalidResource,
	// "The ImageId '…' doesn't exist." (corpus/outscale/oapi-cli-refusals.jsonl,
	// 2026-08-21). Served under a declared catalogue (#126) and nowhere else,
	// since by default an unknown image is accepted on purpose (#392).
	codeImageDoesNotExist = "5023"
	// codeResourceConflict sits in 9000-9999 so osc.IsConflict reports true. It
	// is what a delete blocked by a dependency answers.
	codeResourceConflict = "9029"
	// codeInvalidVolumeState is read off the wire rather than chosen for a
	// range: a real account, 2026-08-08, refused CreateSnapshot on a volume
	// still "creating" with 409 InvalidVolumeState and this code (#124).
	// osc.IsConflict covers 6000-6999 too, so a client branches on it the way
	// it branches on the cloud's.
	codeInvalidVolumeState = "6007"
)

const (
	typeInvalidParameter   = "InvalidParameterValue"
	typeInvalidResource    = "InvalidResource"
	typeResourceConflict   = "ResourceConflict"
	typeInvalidVolumeState = "InvalidVolumeState"
)

// writeError emits the Outscale error envelope.
func (p *Pack) writeError(w http.ResponseWriter, status int, code, errType, details string) {
	emulator.WriteJSON(w, status, map[string]any{
		"Errors": []map[string]string{{
			"Type":    errType,
			"Code":    code,
			"Details": details,
		}},
		"ResponseContext": p.context(),
	})
}

// badRequest is the answer to an argument the API will not take. Outscale
// answers 400 for these, which is what the client's own retry logic treats as
// final rather than worth another attempt.
func (p *Pack) badRequest(w http.ResponseWriter, details string) {
	p.writeError(w, http.StatusBadRequest, codeInvalidParameter, typeInvalidParameter, details)
}

// notFound is the answer to an identifier that names nothing.
func (p *Pack) notFound(w http.ResponseWriter, kind, id string) {
	p.writeError(w, http.StatusBadRequest, codeResourceNotFound, typeInvalidResource,
		"the "+kind+" "+id+" does not exist")
}

// conflict is the answer to an operation a dependency forbids: deleting a subnet
// a machine still sits on, or a security group a machine still carries.
func (p *Pack) conflict(w http.ResponseWriter, details string) {
	p.writeError(w, http.StatusConflict, codeResourceConflict, typeResourceConflict, details)
}
