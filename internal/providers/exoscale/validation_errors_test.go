package exoscale_test

import (
	"net/http"
	"testing"
)

// A validation refusal carries an errors array (#397).
//
// corpus/exoscale/exo-refusals.jsonl, recorded 2026-08-21 against a real
// ch-gva-2 account, holds two POST /v2/private-network refusals with the range
// declared backwards, both 400 with {"message": …, "errors": [{"message": …}]},
// and five 404s (get-ssh-key, delete-ssh-key) and one 409 (register-ssh-key)
// with {"message": …} alone. So the array is the validation shape, not the
// error shape: it goes on a refusal of a field's value and nowhere else, and
// adding it to writeError would have put an empty one on the six refusals the
// cloud answers without it, the second divergence #397 warned about.
//
// The texts are redacted in the recording, so nothing here holds a wording:
// the assertions are on the keys and their types, which is what the record
// proves.

// aValidationRefusal holds a 400 to the recorded shape: a top-level message
// and a non-empty errors array whose items carry a message.
func aValidationRefusal(t *testing.T, what string, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusBadRequest {
		t.Fatalf("%s answered %d: %v", what, code, body)
	}
	if message, _ := body["message"].(string); message == "" {
		t.Errorf("%s: the refusal carries no top-level message: %v", what, body)
	}
	errs, ok := body["errors"].([]any)
	if !ok || len(errs) == 0 {
		t.Errorf("%s: the refusal carries no errors array, and the cloud's does: %v", what, body)
		return
	}
	for i, entry := range errs {
		item, _ := entry.(map[string]any)
		if message, _ := item["message"].(string); message == "" {
			t.Errorf("%s: errors[%d] carries no message: %v", what, i, entry)
		}
	}
}

func TestARangeRefusalCarriesItsErrorsArray(t *testing.T) {
	h := serve(t)
	for _, c := range []struct{ name, body string }{
		{"end-ip below start-ip",
			`{"name":"pn-backwards","start-ip":"10.90.0.20","end-ip":"10.90.0.10","netmask":"255.255.255.0","options":{}}`},
		{"half a range",
			`{"name":"pn-half","start-ip":"10.90.0.20","options":{}}`},
		{"no name",
			`{"start-ip":"10.90.0.20","end-ip":"10.90.0.200","netmask":"255.255.255.0","options":{}}`},
	} {
		rec, body := call(t, h, "POST", "/v2/private-network", c.body)
		aValidationRefusal(t, "create with "+c.name, rec.Code, body)
	}

	// The update validates the same triple, and answers the same shape.
	id := createTestNetwork(t, h, managedNetworkBody, "pn-managed")
	rec, body := call(t, h, "PUT", "/v2/private-network/"+id,
		`{"start-ip":"10.90.0.200","end-ip":"10.90.0.20","netmask":"255.255.255.0"}`)
	aValidationRefusal(t, "update with end-ip below start-ip", rec.Code, body)
}

// The array is the validation shape and not the error shape: a refusal that is
// not about a field's value carries the message alone, as the five 404s and the
// 409 of the same recording do. Without this half, an errors array on every
// refusal would pass the test above and be the second divergence.
func TestARefusalThatIsNotAValidationCarriesNoErrorsArray(t *testing.T) {
	h := serve(t)
	rec, body := call(t, h, "GET", "/v2/private-network/00000000-0000-4000-8000-000000000000", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown network answered %d: %v", rec.Code, body)
	}
	if message, _ := body["message"].(string); message == "" {
		t.Errorf("the 404 carries no message: %v", body)
	}
	if errs, present := body["errors"]; present {
		t.Errorf("the 404 carries an errors array the cloud's does not: %v", errs)
	}
}
