package scaleway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// scwDispatchTypes is the switch in scaleway-sdk-go/scw/errors.go,
// unmarshalStandardError: the whole set of `type` values that SDK generation
// turns into a typed error a caller can match with errors.As. Anything else
// falls through to a plain *ResponseError carrying the status.
//
// Transcribed rather than read from a checkout, the same way
// tools/contract/scaleway-error.yaml transcribes the shape those structs
// require: .upstream/ is cloned by `mise run upstream:sync` and is not
// versioned, so a test reading it would measure whether somebody ran a task.
var scwDispatchTypes = map[string]bool{
	"invalid_arguments":     true,
	"quotas_exceeded":       true,
	"transient_state":       true,
	"not_found":             true,
	"locked":                true,
	"permissions_denied":    true,
	"out_of_stock":          true,
	"resource_expired":      true,
	"denied_authentication": true,
	"precondition_failed":   true,
}

// The `type` field of a Scaleway error is a dispatch key, not decoration, and
// an injected fault has to be honest about which half of the dispatch it is on.
//
// The refusals #356 needs observable — 401 and 403 — carry a type the SDK names,
// so a consumer gets DeniedAuthenticationError or PermissionsDeniedError and can
// tell "I am not allowed" from "it does not exist". The transient family carries
// a type the SDK deliberately does not name: this project has measured no
// Scaleway spelling for a 429 or a 503, and publishing a plausible one would be
// inventing a fact about Scaleway, which rule 4 forbids exactly as much as
// inventing a shape. Both halves are asserted, because either alone is
// satisfiable by an accident.
func TestScalewayFaultsCarryTheTypeTheSDKDispatchesOn(t *testing.T) {
	p := &Pack{}

	named := map[int]string{
		http.StatusUnauthorized: "denied_authentication",
		http.StatusForbidden:    "permissions_denied",
	}
	for status, want := range named {
		body := renderFault(t, p, status)
		got, _ := body["type"].(string)
		if got != want {
			t.Errorf("%d carries type %q, want %q", status, got, want)
		}
		if !scwDispatchTypes[got] {
			t.Errorf("%d carries type %q, which the SDK's dispatch does not name: "+
				"the client would get an untyped error where it expects a typed one", status, got)
		}
	}

	for _, status := range []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		body := renderFault(t, p, status)
		got, _ := body["type"].(string)
		if got == "" {
			t.Errorf("%d carries no type; scw.ResponseError requires one", status)
		}
		if scwDispatchTypes[got] {
			t.Errorf("%d carries type %q, which the SDK maps onto a typed error: "+
				"this emulator would be claiming Scaleway spells a %d that way, "+
				"and nobody measured that", status, got, status)
		}
	}
}

// The values inside a typed error are the SDK's own enumerations, not plausible
// strings: DeniedAuthenticationError.Error() switches on Method over
// {unknown_method, jwt, api_key} and on Reason over {unknown_reason,
// invalid_argument, not_found, expired}, and prints "unknown reason" for
// anything outside them. A junk API key is api_key/not_found, and a client
// prints "API key does not exist" rather than a blank.
func TestScalewayDeniedAuthenticationUsesTheSDKsOwnEnumerations(t *testing.T) {
	body := renderFault(t, &Pack{}, http.StatusUnauthorized)

	methods := map[string]bool{"unknown_method": true, "jwt": true, "api_key": true}
	reasons := map[string]bool{"unknown_reason": true, "invalid_argument": true, "not_found": true, "expired": true}
	if method, _ := body["method"].(string); !methods[method] {
		t.Errorf("method is %q, which DeniedAuthenticationError.Error() renders as \"unknown method\"", body["method"])
	}
	if reason, _ := body["reason"].(string); !reasons[reason] {
		t.Errorf("reason is %q, which DeniedAuthenticationError.Error() renders as \"unknown reason\"", body["reason"])
	}
}

// PermissionsDeniedError reads `details` as an array of {resource, action}, and
// its Error() prints one line per entry. An empty array would print
// "insufficient permissions: " and tell a consumer nothing about what was
// refused — which is the whole of what #356's case needs to name the missing
// grant.
func TestScalewayPermissionsDeniedNamesWhatWasRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/instance/v1/zones/fr-par-1/servers/abc", nil)
	(&Pack{}).WriteFault(rec, req, http.StatusForbidden)

	var body struct {
		Details []struct {
			Resource string `json:"resource"`
			Action   string `json:"action"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the 403 the way PermissionsDeniedError does: %v", err)
	}
	if len(body.Details) != 1 {
		t.Fatalf("details holds %d entries, want 1: an empty one names no grant", len(body.Details))
	}
	if body.Details[0].Resource != "/instance/v1/zones/fr-par-1/servers/abc" {
		t.Errorf("resource is %q, want the path that was refused", body.Details[0].Resource)
	}
	if body.Details[0].Action != http.MethodDelete {
		t.Errorf("action is %q, want the method that was refused", body.Details[0].Action)
	}
}

// Every status the pack declares must render, or a rule the core accepted would
// answer nothing at all. The list and the switch are two places, and this is
// what keeps them one.
func TestScalewayRendersEveryStatusItDeclares(t *testing.T) {
	p := &Pack{}
	for _, status := range p.FaultStatuses() {
		rec := httptest.NewRecorder()
		p.WriteFault(rec, httptest.NewRequest(http.MethodGet, "/instance/v1/zones/fr-par-1/servers", nil), status)
		if rec.Code != status {
			t.Errorf("declared status %d rendered %d", status, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%d answered Content-Type %q: hasResponseError drops any body that is not "+
				"application/json and leaves the caller the bare HTTP status", status, ct)
		}
		var decoded map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Errorf("%d answered a body scw cannot decode: %v", status, err)
		}
	}
}

func renderFault(t *testing.T, p *Pack, status int) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	p.WriteFault(rec, httptest.NewRequest(http.MethodGet, "/instance/v1/zones/fr-par-1/servers", nil), status)
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode the %d body: %v", status, err)
	}
	return decoded
}
