package exoscale

import (
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The account-level reads, and the two field resets (#173).
//
// Five operations that belonged to no batch and to no family. get-organization
// is the one the pack already refused to decline — TestQuotasAndOrganisationAreNotDeclined
// keeps it in scope as "a read several clients make" — and it was still
// unserved, which is the shape a decision left half-taken always takes: a test
// forbidding the refusal, and nothing requiring the service.

// getOrganization answers the single organization this emulator hosts.
//
// One, and the identifier is fixed, because an emulator that invented an account
// boundary would let a client believe in an isolation it does not have. The same
// reasoning as the Scaleway pack's defaultOrganization, and the same value shape.
func (p *Pack) getOrganization(w http.ResponseWriter, _ *http.Request) {
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"id":       emulatedOrganizationID,
		"name":     "feint",
		"address":  "",
		"balance":  0,
		"country":  "CH",
		"currency": "EUR",
	})
}

// emulatedOrganizationID is stable across restarts on purpose: a client that
// stored it must find the same one, and Terraform stores everything it reads.
const emulatedOrganizationID = "88888888-8888-4888-8888-888888888888"

// listEvents answers the account's audit trail, and answers it empty.
//
// Empty and served, rather than declined, and the difference is what a client
// does with each. `exo` and the Terraform provider read events to explain a
// failure; a 404 there turns "nothing happened" into "this emulator is broken",
// which sends a user looking in the wrong place. An empty list is the honest
// answer: this emulator records no audit trail, and docs/limits.md says so.
//
// Inventing entries would be worse than either — a client would read a history
// that never happened.
//
// The from/to window their document declares on this operation
// (.upstream/exoscale-openapi.yaml:12234-12246, both format date-time) is read
// and checked rather than dropped: any window over an empty trail is the empty
// trail, so honouring the filter costs nothing, and a malformed timestamp is
// refused the way the real API refuses a value outside its declared format —
// not swallowed by a handler that never looked (#271 names the class).
//
// TestEventWindowIsValidated fails without the check.
func (p *Pack) listEvents(w http.ResponseWriter, r *http.Request) {
	for _, key := range []string{"from", "to"} {
		value := r.URL.Query().Get(key)
		if value == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			writeError(w, http.StatusBadRequest, key+" must be a date-time")
			return
		}
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"events": []any{}})
}

// enableTPM turns on the virtual TPM an instance declares.
//
// Recorded, and honoured by nothing below: no runtime this project drives
// presents a TPM to a guest. What makes that acceptable rather than a lie is
// that the field is the client's own and round-trips — and what would make it a
// lie is claiming the guest has one. docs/limits.md carries the line.
//
// Refused on a running instance, which is upstream's own precondition: a TPM is
// attached at boot.
//
// TestEnablingTPMOnARunningInstanceIsRefused fails without this.
func (p *Pack) enableTPM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindInstance, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if res.State == runningState {
		writeError(w, http.StatusBadRequest, "the instance must be stopped before a TPM can be attached")
		return
	}
	if err := p.env.Store.Update(Name, kindInstance, id, func(stored *resource.Resource) error {
		stored.Attrs["tpm-enabled"] = true
		stored.Updated = p.env.Now()
		return nil
	}); err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

// resettableInstanceFields is what DELETE /instance/{id}/{field} may clear, and
// the list is closed on purpose.
//
// An open one would let a client delete any attribute it named, including the
// identifiers the emulator's own bookkeeping reads — and Attrs is restored
// verbatim from a snapshot, so "the client named it" is not a reason to trust it.
var resettableInstanceFields = map[string]string{
	"labels":      "labels",
	"user-data":   "user-data",
	"name":        "name",
	"tpm-enabled": "tpm-enabled",
}

var resettableElasticIPFields = map[string]string{
	"description": "description",
	"healthcheck": "healthcheck",
	"labels":      "labels",
}

func (p *Pack) resetInstanceField(w http.ResponseWriter, r *http.Request) {
	p.resetField(w, r, kindInstance, nounInstance, resettableInstanceFields)
}

func (p *Pack) resetElasticIPField(w http.ResponseWriter, r *http.Request) {
	p.resetField(w, r, kindElasticIP, nounElasticIP, resettableElasticIPFields)
}

// resetField clears one named attribute, refusing anything not on the list.
//
// TestResettingAFieldNobodyDeclaredIsRefused fails without the closed list.
func (p *Pack) resetField(w http.ResponseWriter, r *http.Request, kind, noun string, allowed map[string]string) {
	id := r.PathValue("id")
	field := r.PathValue("field")
	attr, ok := allowed[field]
	if !ok {
		writeError(w, http.StatusBadRequest, "the field "+field+" cannot be reset")
		return
	}
	if err := p.env.Store.Update(Name, kind, id, func(stored *resource.Resource) error {
		delete(stored.Attrs, attr)
		stored.Updated = p.env.Now()
		return nil
	}); err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(noun, id))
}
