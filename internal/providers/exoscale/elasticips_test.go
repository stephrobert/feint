package exoscale_test

import (
	"net/http"
	"reflect"
	"testing"
)

// Elastic IP labels (#703). The `elastic-ip` schema this pack is generated
// against declares `labels`, create-elastic-ip and update-elastic-ip take them,
// and the request structs here declared the field from the start — and
// nothing stored it, so a labelled address read back without labels while its
// description, from the same body, read back fine. Measured on 2026-09-05 with
// the Python SDK against main at 9074c0f, and under Terraform as the one
// resource of twenty-three whose second plan was never empty:
//
//	~ resource "exoscale_elastic_ip" "bastion" {
//	    ~ labels = { + "example" = "temoin0.71.0" }
//	Plan: 0 to add, 1 to change, 0 to destroy.
//
// Every assertion here is on what a client reads after writing, which is the
// only place the defect was visible.

// createTestElasticIP posts an elastic IP and returns the id the operation
// refers to, the way the SDK follows a create.
func createTestElasticIP(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	rec, op := call(t, h, "POST", "/v2/elastic-ip", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("elastic IP create answered %d: %v", rec.Code, op)
	}
	ref, _ := op["reference"].(map[string]any)
	id, _ := ref["id"].(string)
	if id == "" {
		t.Fatalf("the create's operation refers to nothing: %v", op)
	}
	return id
}

// The labels a create carries come back on the get and on the list, and an
// address created without any carries no `labels` key at all — absent rather
// than empty, the omission habit measured on this API and held on the private
// network next door.
func TestAnElasticIPReadsItsLabelsBack(t *testing.T) {
	h := serve(t)
	want := map[string]any{"example": "mesure-labels", "role": "temoin"}
	labelled := createTestElasticIP(t, h,
		`{"description":"mesure-labels","labels":{"example":"mesure-labels","role":"temoin"}}`)
	bare := createTestElasticIP(t, h, `{"description":"no labels"}`)

	rec, got := call(t, h, "GET", "/v2/elastic-ip/"+labelled, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get answered %d: %v", rec.Code, got)
	}
	if got["description"] != "mesure-labels" {
		t.Errorf("the description from the same body did not survive: %v", got)
	}
	if !reflect.DeepEqual(got["labels"], want) {
		t.Errorf("get: labels are %v, want %v", got["labels"], want)
	}

	_, listed := call(t, h, "GET", "/v2/elastic-ip", "")
	seen := 0
	for _, entry := range listed["elastic-ips"].([]any) {
		eip, _ := entry.(map[string]any)
		switch eip["id"] {
		case labelled:
			seen++
			if !reflect.DeepEqual(eip["labels"], want) {
				t.Errorf("list: labels are %v, want %v", eip["labels"], want)
			}
		case bare:
			seen++
			if _, present := eip["labels"]; present {
				t.Errorf("list: an address created without labels carries %v", eip["labels"])
			}
		}
	}
	if seen != 2 {
		t.Fatalf("the list holds %d of the 2 addresses created", seen)
	}
	_, plain := call(t, h, "GET", "/v2/elastic-ip/"+bare, "")
	if _, present := plain["labels"]; present {
		t.Errorf("get: an address created without labels carries %v", plain["labels"])
	}
}

// An update that names labels replaces them; one that does not leaves them
// alone; an empty map clears them, which is how the CLI clears any field on this
// API (an update with an empty value, never the per-field DELETE). Nil and empty
// are two different requests, the distinction #404 asks of every update.
func TestAnElasticIPUpdateReplacesItsLabelsAndSilenceKeepsThem(t *testing.T) {
	h := serve(t)
	id := createTestElasticIP(t, h, `{"labels":{"role":"temoin"}}`)
	read := func() map[string]any {
		t.Helper()
		_, got := call(t, h, "GET", "/v2/elastic-ip/"+id, "")
		return got
	}

	if rec, out := call(t, h, "PUT", "/v2/elastic-ip/"+id, `{"description":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("an update without labels answered %d: %v", rec.Code, out)
	}
	if got := read(); !reflect.DeepEqual(got["labels"], map[string]any{"role": "temoin"}) || got["description"] != "renamed" {
		t.Errorf("an update that did not name labels changed them: %v", got)
	}

	if rec, out := call(t, h, "PUT", "/v2/elastic-ip/"+id, `{"labels":{"role":"web","tier":"edge"}}`); rec.Code != http.StatusOK {
		t.Fatalf("a labels update answered %d: %v", rec.Code, out)
	}
	if got := read(); !reflect.DeepEqual(got["labels"], map[string]any{"role": "web", "tier": "edge"}) {
		t.Errorf("an update naming labels did not replace them: %v", got["labels"])
	}

	if rec, out := call(t, h, "PUT", "/v2/elastic-ip/"+id, `{"labels":{}}`); rec.Code != http.StatusOK {
		t.Fatalf("clearing the labels answered %d: %v", rec.Code, out)
	}
	if got := read(); got["labels"] != nil {
		t.Errorf("an empty map did not clear the labels: %v", got["labels"])
	}
}

// reset-elastic-ip-field's enum is one entry long in the API description:
// description. The pack used to accept labels and healthcheck there too, and
// answered 200 while clearing a labels field it had never stored.
func TestOnlyTheDescriptionOfAnElasticIPIsResettable(t *testing.T) {
	h := serve(t)
	id := createTestElasticIP(t, h, `{"description":"to reset","labels":{"role":"temoin"}}`)

	for _, field := range []string{"labels", "healthcheck", "ip"} {
		if rec, out := call(t, h, "DELETE", "/v2/elastic-ip/"+id+"/"+field, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("resetting %s, which the contract does not declare resettable, answered %d: %v",
				field, rec.Code, out)
		}
	}
	// The declared one works, or the guard refuses everything and proves nothing.
	if rec, out := call(t, h, "DELETE", "/v2/elastic-ip/"+id+"/description", ""); rec.Code != http.StatusOK {
		t.Fatalf("resetting the description answered %d: %v", rec.Code, out)
	}
	_, got := call(t, h, "GET", "/v2/elastic-ip/"+id, "")
	if _, present := got["description"]; present {
		t.Errorf("the description survived its reset: %v", got)
	}
	if !reflect.DeepEqual(got["labels"], map[string]any{"role": "temoin"}) {
		t.Errorf("resetting the description touched the labels: %v", got["labels"])
	}
}
