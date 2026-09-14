package scaleway_test

import (
	"net/http"
	"testing"
)

const lbZone = "/lb/v1/zones/fr-par-1"

// TestAMigrationChangesTheOfferAndTheReadShowsIt is what #762 asked for, and it
// is deliberately the read that asserts rather than the 200.
//
// The call was declined until 2026-09-14 on the ground that answering "would
// confirm a resize nothing performed". That argument did not survive its own
// neighbours: UpdateServer already accepts a new commercial_type and stores it.
// What a Day-2 client needs is the acceptance and the resulting type, which is
// what a real account answers too.
func TestAMigrationChangesTheOfferAndTheReadShowsIt(t *testing.T) {
	ts := newTestServer(t)

	code, body := do(t, ts, "POST", lbZone+"/lbs",
		`{"name":"migrating","type":"lb-s","ip_ids":[]}`)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("creating a balancer answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("no id in the create response: %v", body)
	}
	if got, _ := body["type"].(string); got != "lb-s" {
		t.Fatalf("the create answered type %q, want lb-s", got)
	}

	code, out := do(t, ts, "POST", lbZone+"/lbs/"+id+"/migrate", `{"type":"lb-gp-m"}`)
	if code != http.StatusOK {
		t.Fatalf("a migration answered %d: %v", code, out)
	}
	if got, _ := out["type"].(string); got != "lb-gp-m" {
		t.Errorf("the migration answered type %q, want lb-gp-m", got)
	}

	// The half that matters: the read AFTER the call. A handler that answered
	// the new type without storing it would pass the assertion above.
	_, read := do(t, ts, "GET", lbZone+"/lbs/"+id, "")
	if got, _ := read["type"].(string); got != "lb-gp-m" {
		t.Errorf("the read after the migration shows %q, want lb-gp-m: the call was "+
			"accepted and changed nothing, which is the plausible-wrong answer this "+
			"repository exists to avoid", got)
	}
}

// TestAMigrationLowercasesLikeTheCreate, or `LB-S` and `lb-s` would name two
// offers and the read after a migration would not match the read after a create.
func TestAMigrationLowercasesLikeTheCreate(t *testing.T) {
	ts := newTestServer(t)

	_, body := do(t, ts, "POST", lbZone+"/lbs", `{"name":"casing","type":"lb-s","ip_ids":[]}`)
	id, _ := body["id"].(string)

	if code, out := do(t, ts, "POST", lbZone+"/lbs/"+id+"/migrate", `{"type":"LB-GP-L"}`); code != http.StatusOK {
		t.Fatalf("a migration in upper case answered %d: %v", code, out)
	}
	_, read := do(t, ts, "GET", lbZone+"/lbs/"+id, "")
	if got, _ := read["type"].(string); got != "lb-gp-l" {
		t.Errorf("type reads %q after migrating to LB-GP-L, want lb-gp-l", got)
	}
}

// TestAMigrationWithoutATypeIsRefused: the SDK's field is not optional, and a
// migration naming no offer asks for nothing. This is the one refusal here that
// is about the REQUEST rather than about a value — a refusal keyed on a value
// would fail corpus:check, which replays synthetic values, and loadbalancer.go
// says so where the handler is.
func TestAMigrationWithoutATypeIsRefused(t *testing.T) {
	ts := newTestServer(t)

	_, body := do(t, ts, "POST", lbZone+"/lbs", `{"name":"empty","type":"lb-s","ip_ids":[]}`)
	id, _ := body["id"].(string)

	for _, payload := range []string{`{}`, `{"type":""}`, `{"type":"   "}`} {
		code, out := do(t, ts, "POST", lbZone+"/lbs/"+id+"/migrate", payload)
		if code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400: %v", payload, code, out)
		}
	}

	// And the balancer is untouched by a refused migration.
	_, read := do(t, ts, "GET", lbZone+"/lbs/"+id, "")
	if got, _ := read["type"].(string); got != "lb-s" {
		t.Errorf("a refused migration changed the type to %q", got)
	}
}

// TestAMigrationOfSomethingThatIsNotThereIs404, which is where a client looks
// next: at the resource rather than at its own request.
func TestAMigrationOfSomethingThatIsNotThereIs404(t *testing.T) {
	ts := newTestServer(t)
	code, _ := do(t, ts, "POST",
		lbZone+"/lbs/11111111-1111-4111-8111-111111111111/migrate", `{"type":"lb-gp-m"}`)
	if code != http.StatusNotFound {
		t.Errorf("migrating an absent balancer answered %d, want 404", code)
	}
}
